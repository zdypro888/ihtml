package assistant

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/zdypro888/ihtml"
)

// BackgroundConfig 把已有会话执行器与连接分离；业务授权、持久化和通知仍由宿主回调负责。
// Key 必须由服务端认证身份、能力代次及对话号生成，禁止直接信任客户端 owner。
type BackgroundConfig struct {
	Backend        ihtml.ChatBackend
	Lifetime       context.Context
	Key            func(context.Context, *ihtml.ChatSessionContext) (string, error)
	Retain         func(context.Context, *ihtml.ChatSessionContext) (func(), error)
	Observe        func(context.Context, *ihtml.ChatSessionContext, ihtml.ChatStreamEvent)
	IdleTTL        time.Duration
	MaxSessions    int
	MaxReplayBytes int
}

type BackgroundBackend struct {
	cfg      BackgroundConfig
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	sessions map[string]*backgroundSession
	closed   bool
	done     chan struct{}
}

type backgroundSession struct {
	publishMu sync.Mutex
	hub       *BackgroundBackend
	key       string
	streamID  string
	ctx       context.Context
	cancel    context.CancelFunc
	source    ihtml.ChatSessionContext
	inner     ihtml.ChatSession
	release   func()
	mu        sync.Mutex
	clients   map[*backgroundAttachment]bool
	events    []ihtml.ChatStreamEvent
	bytes     int
	text      map[string]string
	active    map[string]bool
	stopping  map[string]bool
	touched   time.Time
	sequence  uint64
}

type backgroundAttachment struct {
	session *backgroundSession
	emit    func(ihtml.ChatStreamEvent) error
	mu      sync.Mutex
	closed  bool
}

// NewBackgroundBackend 只包装现有 Backend，不另起模型或工具执行器；断线只移除订阅。
func NewBackgroundBackend(cfg BackgroundConfig) (*BackgroundBackend, error) {
	if isNil(cfg.Backend) || cfg.Lifetime == nil || cfg.Key == nil || cfg.Lifetime.Err() != nil {
		return nil, errors.New("assistant: background backend requires a live lifetime, backend and trusted key")
	}
	if cfg.IdleTTL == 0 {
		cfg.IdleTTL = 24 * time.Hour
	}
	if cfg.MaxSessions == 0 {
		cfg.MaxSessions = 256
	}
	if cfg.MaxReplayBytes == 0 {
		cfg.MaxReplayBytes = 2 << 20
	}
	if cfg.IdleTTL < time.Minute || cfg.MaxSessions < 1 || cfg.MaxReplayBytes < 1<<20 {
		return nil, errors.New("assistant: invalid background retention settings")
	}
	ctx, cancel := context.WithCancel(cfg.Lifetime)
	b := &BackgroundBackend{cfg: cfg, ctx: ctx, cancel: cancel, sessions: map[string]*backgroundSession{}, done: make(chan struct{})}
	go b.sweep()
	return b, nil
}

func (b *BackgroundBackend) Models(ctx context.Context) (ihtml.ChatModels, error) {
	lister, ok := b.cfg.Backend.(ihtml.ModelLister)
	if !ok {
		return ihtml.ChatModels{}, errors.New("assistant: background backend requires model capabilities")
	}
	models, err := lister.Models(ctx)
	models.BackgroundExecution = true
	models.ConfirmationScope = "session"
	return models, err
}

func (b *BackgroundBackend) NewSession(ctx context.Context, sc *ihtml.ChatSessionContext) (ihtml.ChatSession, error) {
	if ctx == nil || ctx.Err() != nil || sc == nil || sc.Emit == nil {
		return nil, errors.New("assistant: live authenticated attachment required")
	}
	key, err := b.cfg.Key(ctx, sc)
	if err != nil || strings.TrimSpace(key) == "" || len(key) > 1024 {
		return nil, ihtml.ErrAccessDenied
	}
	b.mu.Lock()
	if b.closed || b.ctx.Err() != nil {
		b.mu.Unlock()
		return nil, ErrSessionRunnerClosed
	}
	e := b.sessions[key]
	if e == nil {
		var release func()
		if b.cfg.Retain != nil {
			release, err = b.cfg.Retain(ctx, sc)
			if err != nil {
				b.mu.Unlock()
				return nil, err
			}
		}
		if len(b.sessions) >= b.cfg.MaxSessions {
			b.mu.Unlock()
			if release != nil {
				release()
			}
			return nil, errors.New("assistant: background session capacity reached")
		}
		// 保留宿主绑定值，取消仅跟随后端生命周期，不能跟随某一条 HTTP/WS 连接。
		values := context.WithoutCancel(ctx)
		root, cancel := context.WithCancel(values)
		stop := context.AfterFunc(b.ctx, cancel)
		source := *sc
		if sc.Request != nil {
			source.Request = sc.Request.Clone(root)
		} else {
			source.Request = (&http.Request{}).WithContext(root)
		}
		stream := make([]byte, 16)
		if _, err := rand.Read(stream); err != nil {
			b.mu.Unlock()
			cancel()
			stop()
			if release != nil {
				release()
			}
			return nil, errors.New("assistant: background stream unavailable")
		}
		e = &backgroundSession{hub: b, key: key, streamID: hex.EncodeToString(stream), ctx: root, cancel: func() { stop(); cancel() }, source: source, release: release, clients: map[*backgroundAttachment]bool{}, text: map[string]string{}, active: map[string]bool{}, stopping: map[string]bool{}, touched: time.Now()}
		source.Emit = e.publish
		e.source.Emit = e.publish
		e.inner, err = b.cfg.Backend.NewSession(root, &source)
		if err != nil {
			b.mu.Unlock()
			e.cancel()
			if release != nil {
				release()
			}
			return nil, err
		}
		b.sessions[key] = e
	}
	b.mu.Unlock()
	client := &backgroundAttachment{session: e, emit: sc.Emit}
	// 先锁订阅再登记，实时事件不能越过历史回放，避免 Seq 去重误丢尚未回放的内容。
	client.mu.Lock()
	e.mu.Lock()
	e.clients[client] = true
	e.touched = time.Now()
	replay := append([]ihtml.ChatStreamEvent(nil), e.events...)
	seen := map[string]bool{}
	prefix := []ihtml.ChatStreamEvent{}
	for _, event := range replay {
		if event.RunID != "" && !seen[event.RunID] {
			seen[event.RunID] = true
			if event.Type != ihtml.ChatStreamTurnStart {
				prefix = append(prefix, ihtml.ChatStreamEvent{Type: ihtml.ChatStreamTurnStart, RunID: event.RunID, BackgroundSession: e.streamID})
			}
		}
	}
	replay = append(prefix, replay...)
	e.mu.Unlock()
	for _, event := range replay {
		event.BackgroundReplay = true
		if err = client.emit(event); err != nil {
			client.closed = true
			break
		}
	}
	client.mu.Unlock()
	if err != nil {
		client.Close()
	}
	return client, nil
}

func (e *backgroundSession) publish(event ihtml.ChatStreamEvent) error {
	// 观察者可能持久化；序号、回放与实时投递必须同序，不能被另一条流事件超越。
	e.publishMu.Lock()
	defer e.publishMu.Unlock()
	// JSON 克隆隔离宿主切片/卡片，运行中只在内存保存签名，宿主观察者须自行脱敏持久化。
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	var stored ihtml.ChatStreamEvent
	if json.Unmarshal(raw, &stored) != nil {
		return errors.New("assistant: invalid background event")
	}
	e.mu.Lock()
	if event.Type == ihtml.ChatStreamTurnError && e.stopping[event.RunID] {
		event.FinishReason = "cancelled"
		stored.FinishReason = "cancelled"
		delete(e.stopping, event.RunID)
	}
	e.sequence++
	event.BackgroundSession = e.streamID
	event.BackgroundSeq = e.sequence
	stored.BackgroundSession = e.streamID
	stored.BackgroundSeq = e.sequence
	e.touched = time.Now()
	if event.Type == ihtml.ChatStreamTurnStart {
		e.active[event.RunID] = true
	}
	if event.Type == ihtml.ChatStreamAssistantDone || event.Type == ihtml.ChatStreamTurnError {
		delete(e.active, event.RunID)
		delete(e.text, event.RunID)
	}
	if event.Type == ihtml.ChatStreamAssistantReset {
		e.text[event.RunID] = ""
	}
	if event.Type == ihtml.ChatStreamAssistantDelta {
		e.text[event.RunID] += event.Delta
		stored.Type = ihtml.ChatStreamAssistantSnapshot
		stored.Delta = e.text[event.RunID]
	}
	// 流式文本合并成可替换快照；重连已看过部分文本时不会重复追加前缀。
	if n := len(e.events); n > 0 && stored.Type == ihtml.ChatStreamAssistantSnapshot && e.events[n-1].Type == stored.Type && e.events[n-1].RunID == stored.RunID {
		old, _ := json.Marshal(e.events[n-1])
		e.bytes -= len(old)
		e.events = e.events[:n-1]
	}
	encoded, _ := json.Marshal(stored)
	e.events = append(e.events, stored)
	e.bytes += len(encoded)
	// 回放预算只影响旧展示轨迹，不取消业务执行；最终文本及业务回执由宿主持久化。
	for e.bytes > e.hub.cfg.MaxReplayBytes && len(e.events) > 1 {
		old, _ := json.Marshal(e.events[0])
		e.bytes -= len(old)
		e.events[0] = ihtml.ChatStreamEvent{}
		e.events = e.events[1:]
	}
	clients := make([]*backgroundAttachment, 0, len(e.clients))
	for c := range e.clients {
		clients = append(clients, c)
	}
	e.mu.Unlock()
	if e.hub.cfg.Observe != nil {
		func() { defer func() { _ = recover() }(); e.hub.cfg.Observe(e.ctx, &e.source, event) }()
	}
	for _, client := range clients {
		client.mu.Lock()
		if !client.closed {
			if client.emit(event) != nil {
				client.closed = true
			}
		}
		closed := client.closed
		client.mu.Unlock()
		if closed {
			client.Close()
		}
	}
	return nil // 单个连接的投递失败不能取消后台模型轮次。
}

func (c *backgroundAttachment) HandleEvent(ctx context.Context, event *ihtml.ChatClientEvent) {
	if ctx == nil || ctx.Err() != nil {
		return
	}
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return
	}
	if event != nil && event.Type == ihtml.ChatClientUserMessage && len(event.Messages) > 0 {
		last := event.Messages[len(event.Messages)-1]
		if last.Role == "user" && len(last.Content) <= 65536 {
			c.session.mu.Lock()
			c.session.sequence++
			echo := ihtml.ChatStreamEvent{Type: "background_user_message", Message: last.Content, BackgroundSession: c.session.streamID, BackgroundSeq: c.session.sequence}
			c.session.events = append(c.session.events, echo)
			encoded, _ := json.Marshal(echo)
			c.session.bytes += len(encoded)
			c.session.mu.Unlock()
		}
	}
	c.session.mu.Lock()
	c.session.touched = time.Now()
	c.session.mu.Unlock()
	if event != nil && event.Type == ihtml.ChatClientCancel {
		c.session.mu.Lock()
		for id := range c.session.active {
			c.session.stopping[id] = true
		}
		c.session.mu.Unlock()
	}
	c.session.inner.HandleEvent(c.session.ctx, event)
}

func (c *backgroundAttachment) Close() {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	c.session.mu.Lock()
	delete(c.session.clients, c)
	c.session.touched = time.Now()
	c.session.mu.Unlock()
}

func (b *BackgroundBackend) sweep() {
	defer close(b.done)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-b.ctx.Done():
			return
		case now := <-ticker.C:
			retired := []*backgroundSession{}
			b.mu.Lock()
			for key, e := range b.sessions {
				e.mu.Lock()
				idle := len(e.active) == 0 && len(e.clients) == 0 && now.Sub(e.touched) > b.cfg.IdleTTL
				e.mu.Unlock()
				if idle {
					delete(b.sessions, key)
					retired = append(retired, e)
				}
			}
			b.mu.Unlock()
			for _, e := range retired {
				e.close()
			}
		}
	}
}

func (e *backgroundSession) close() {
	e.cancel()
	e.inner.Close()
	if e.release != nil {
		e.release()
	}
}

// Close 是宿主服务退出，才取消所有后台会话并等待已有执行器真实收尾。
func (b *BackgroundBackend) Close() {
	b.cancel()
	<-b.done
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	entries := b.sessions
	b.sessions = map[string]*backgroundSession{}
	b.mu.Unlock()
	for _, e := range entries {
		e.close()
	}
}
