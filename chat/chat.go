// Package chat 是 ihtml 的 AI 对话后端: 基于 CloudWeGo eino ADK,
// 通过语义工具直接增删改用户的 UI Item / 主题 / KV, 并把过程以流式事件
// 推回页面内核的对话抽屉。线格式与 casino admin AI 一致。
package chat

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/cloudwego/eino/adk"
	adksession "github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/schema"

	"github.com/zdypro888/ihtml"
)

// Provider 取值。openai 指一切 OpenAI 兼容端点(含各类网关/Ollama),
// claude 指 Anthropic 原生协议。
const (
	ProviderOpenAI = "openai"
	ProviderClaude = "claude"
)

// Config 是对话后端配置。
type Config struct {
	Provider string // openai / claude, 必填
	BaseURL  string // API 根地址, 必填(如 https://api.openai.com/v1)
	APIKey   string // 密钥, 本地网关可留空
	Model    string // 默认模型; 前端可另选

	Stream         bool          // 流式输出, 建议 true
	Thinking       bool          // 透出思考过程(需模型支持 reasoning)
	RequestTimeout time.Duration // 单轮超时, 0 不限
	MaxTokens      int
	Temperature    float32

	// SystemPrompt 追加在内置系统提示之后, 用于业务定制(如"你是某某产品的助手")。
	SystemPrompt string
	// ConfirmWrites 为 true 时所有写操作都要人工确认;
	// 默认只有整体替换与回滚需要确认(普通改动有快照兜底)。
	ConfirmWrites bool
	// MaxIterations 是单轮的模型-工具循环上限, 默认 16。
	MaxIterations int
	// ConfirmSecret 是确认卡 HMAC 密钥。不设置则每进程随机——滚动发布/重启
	// 会让已签发的确认卡失效; 生产环境应配置固定密钥。
	ConfirmSecret []byte
	// AllowModelSelect 为 true 时前端展示模型选择器。默认隐藏:
	// 模型是运维概念, 终端用户直接用 Config.Model / 网关首个已加载模型。
	AllowModelSelect bool
	// TurnInterceptor 在每轮模型调用前执行(限流/配额/计费检查),
	// 返回错误则该轮以 turn_error 结束(错误文案直接展示给用户)。
	TurnInterceptor func(ctx context.Context, user string) error
}

func (c *Config) ready() bool {
	return strings.TrimSpace(c.Provider) != "" && strings.TrimSpace(c.BaseURL) != ""
}

// Backend 实现 ihtml.ChatBackend 与 ihtml.ModelLister。
type Backend struct {
	cfg    Config
	signer *confirmSigner
	// 会话历史存储放 Backend 级共享: 前端带稳定 csid 重连时,
	// 同一 SessionID 命中同一份历史, 断线/刷新不失忆。
	store adk.SessionEventStore[*schema.Message]
}

// New 创建对话后端。
func New(cfg Config) *Backend {
	if cfg.MaxIterations <= 0 {
		cfg.MaxIterations = 16
	}
	return &Backend{
		cfg:    cfg,
		signer: newConfirmSigner(cfg.ConfirmSecret),
		store:  adksession.NewInMemoryStore[*schema.Message](nil),
	}
}

var csidPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// NewSession 实现 ihtml.ChatBackend。
func (b *Backend) NewSession(_ context.Context, sctx *ihtml.ChatSessionContext) (ihtml.ChatSession, error) {
	apiBase := ""
	csid := ""
	if sctx.Request != nil {
		if sctx.Request.Host != "" {
			scheme := "http"
			if sctx.Request.TLS != nil {
				scheme = "https"
			}
			apiBase = scheme + "://" + sctx.Request.Host
		}
		if v := sctx.Request.URL.Query().Get("csid"); csidPattern.MatchString(v) {
			csid = v
		}
	}
	sessionID := fmt.Sprintf("ihtml-%s-%d", sctx.User, time.Now().UnixNano())
	if csid != "" {
		// 稳定会话号: 重连恢复上下文("新会话"由前端换 csid 实现)
		sessionID = "ihtml-" + sctx.User + "-" + csid
	}
	return &session{
		backend:   b,
		user:      sctx.User,
		svc:       sctx.Service,
		emit:      sctx.Emit,
		apis:      sctx.APIs,
		apiBase:   apiBase,
		access:    sctx.Access,
		sessionID: sessionID,
		store:     b.store,
	}, nil
}

// session 对应一条 WS 连接: 多轮历史存 ADK SessionStore(内存态),
// 前端每轮只发当前这条 user 消息; 新会话 = 前端重开 WS。
type session struct {
	backend *Backend
	user    string
	svc     ihtml.Service
	emit    func(ihtml.ChatStreamEvent) error
	apis    []ihtml.APISpec
	apiBase string
	access  ihtml.Access

	loopMu     sync.Mutex
	loop       *adk.TurnLoop[*turnItem, *schema.Message]
	loopCancel context.CancelFunc

	manualMu     sync.Mutex
	manualCancel context.CancelFunc
	manualSeq    int64

	sessionID string
	store     adk.SessionEventStore[*schema.Message]
}

type turnItem struct {
	agent        *adk.ChatModelAgent
	cfg          Config
	current      *schema.Message
	emit         func(ihtml.ChatStreamEvent) error
	runCancel    context.CancelFunc
	timeline     *timeline
	toolsEnabled bool
}

// HandleEvent 实现 ihtml.ChatSession。
func (s *session) HandleEvent(ctx context.Context, ev *ihtml.ChatClientEvent) {
	switch strings.TrimSpace(ev.Type) {
	case "user_message":
		s.cancelManual()
		if err := s.enqueueTurn(ctx, ev); err != nil {
			if ctx.Err() != nil {
				return
			}
			_ = s.emit(ihtml.ChatStreamEvent{Type: "turn_error", Error: userFacingError(err), ModelReady: true})
		}
	case "confirm_tool":
		s.startConfirm(ctx, ev)
	case "cancel":
		s.stopActiveWork()
		_ = s.emit(ihtml.ChatStreamEvent{Type: "assistant_status", Status: "已取消当前请求。", ModelReady: true})
	}
}

// Close 实现 ihtml.ChatSession。
func (s *session) Close() { s.stopActiveWork() }

func (s *session) newEmitter(runID, turnID string) func(ihtml.ChatStreamEvent) error {
	var seq int64
	var mu sync.Mutex
	return func(out ihtml.ChatStreamEvent) error {
		mu.Lock()
		defer mu.Unlock()
		seq++
		out.RunID = runID
		out.TurnID = turnID
		out.Seq = seq
		return s.emit(out)
	}
}

func (s *session) enqueueTurn(parent context.Context, ev *ihtml.ChatClientEvent) error {
	runID := fmt.Sprintf("ai_%d", time.Now().UnixNano())
	emit := s.newEmitter(runID, runID+"_turn")
	if err := emit(ihtml.ChatStreamEvent{Type: "turn_start", ModelReady: true}); err != nil {
		return err
	}
	cfg := s.backend.cfg
	if !cfg.ready() {
		return emit(ihtml.ChatStreamEvent{
			Type:       "assistant_done",
			ModelReady: false,
			Message:    "AI 服务还没有配置, 请在服务端设置 Provider 与 BaseURL。",
		})
	}
	if cfg.TurnInterceptor != nil {
		if err := cfg.TurnInterceptor(parent, s.user); err != nil {
			return emit(ihtml.ChatStreamEvent{Type: "turn_error", Error: userFacingError(err), ModelReady: true})
		}
	}
	current, err := currentUserTurn(ev.Messages)
	if err != nil {
		return emit(ihtml.ChatStreamEvent{Type: "turn_error", Error: err.Error(), ModelReady: true})
	}
	model, err := normalizeModel(ev.Model)
	if err != nil {
		return emit(ihtml.ChatStreamEvent{Type: "turn_error", Error: err.Error(), ModelReady: true})
	}
	if model != "" {
		cfg.Model = model
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return emit(ihtml.ChatStreamEvent{Type: "assistant_done", ModelReady: true, Message: "请先在助手顶部选择或填写模型名。"})
	}
	toolsEnabled := true
	if ev.ToolsEnabled != nil {
		toolsEnabled = *ev.ToolsEnabled
	}
	if err := emit(ihtml.ChatStreamEvent{Type: "assistant_status", Status: "正在连接模型。", ModelReady: true}); err != nil {
		return err
	}
	chatModel, err := newChatModel(parent, cfg)
	if err != nil {
		return emit(ihtml.ChatStreamEvent{Type: "turn_error", Error: userFacingError(err), ModelReady: true})
	}
	tl := &timeline{emit: emit}
	rt := &runtime{svc: s.svc, user: s.user, cfg: cfg, signer: s.backend.signer, timeline: tl, apis: s.apis, apiBase: s.apiBase, access: s.access}
	agentCfg := &adk.ChatModelAgentConfig{
		Name:        "ihtml_ui_agent",
		Description: "ihtml page building assistant.",
		// 系统提示注入"页面现状"清单: 省一次 ui_item_list 往返, 也防盲写撞 id
		Instruction: buildSystemPrompt(cfg, ev.ClientContext, s.apis) + inventoryPrompt(parent, s.svc, s.user),
		Model:       chatModel,
		// 自定义拼装避免默认 f-string 模板: 系统提示里有大量 {} 代码示例。
		GenModelInput: func(_ context.Context, instruction string, input *adk.AgentInput) ([]*schema.Message, error) {
			out := make([]*schema.Message, 0, len(input.Messages)+1)
			if instruction != "" {
				out = append(out, schema.SystemMessage(instruction))
			}
			return append(out, input.Messages...), nil
		},
		MaxIterations: cfg.MaxIterations,
	}
	if toolsEnabled {
		agentCfg.ToolsConfig = adk.ToolsConfig{ToolsNodeConfig: toolsNodeConfig(rt)}
	}
	agent, err := adk.NewChatModelAgent(parent, agentCfg)
	if err != nil {
		return emit(ihtml.ChatStreamEvent{Type: "turn_error", Error: userFacingError(err), ModelReady: true})
	}
	if err := emit(ihtml.ChatStreamEvent{Type: "assistant_status", Status: "正在理解需求。", ModelReady: true}); err != nil {
		return err
	}
	item := &turnItem{
		agent:        agent,
		cfg:          cfg,
		current:      current,
		emit:         emit,
		timeline:     tl,
		toolsEnabled: toolsEnabled,
	}
	loop := s.ensureLoop(parent)
	ok, _ := loop.Push(item, adk.WithPreemptTimeout[*turnItem, *schema.Message](adk.AnySafePoint, 2*time.Second))
	if !ok {
		return emit(ihtml.ChatStreamEvent{Type: "turn_error", Error: "会话已结束, 请重新打开助手。", ModelReady: true})
	}
	return nil
}

func (s *session) ensureLoop(parent context.Context) *adk.TurnLoop[*turnItem, *schema.Message] {
	s.loopMu.Lock()
	if s.loop != nil {
		loop := s.loop
		s.loopMu.Unlock()
		return loop
	}
	loopCtx, cancel := context.WithCancel(parent)
	loop := adk.NewTurnLoop[*turnItem, *schema.Message](adk.TurnLoopConfig[*turnItem, *schema.Message]{
		GenInput:      s.genInput,
		PrepareAgent:  s.prepareAgent,
		OnAgentEvents: s.onAgentEvents,
		SessionID:     s.sessionID,
		SessionStore:  s.store,
	})
	s.loop = loop
	s.loopCancel = cancel
	s.loopMu.Unlock()

	loop.Run(loopCtx)
	go func() {
		_ = loop.Wait()
		s.loopMu.Lock()
		if s.loop == loop {
			s.loop = nil
			s.loopCancel = nil
		}
		s.loopMu.Unlock()
	}()
	return loop
}

func (s *session) genInput(ctx context.Context, _ *adk.TurnLoop[*turnItem, *schema.Message], items []*turnItem) (*adk.GenInputResult[*turnItem, *schema.Message], error) {
	if len(items) == 0 {
		return nil, fmt.Errorf("turn item is required")
	}
	item := items[0]
	runCtx := ctx
	if item.cfg.RequestTimeout > 0 {
		runCtx, item.runCancel = context.WithTimeout(ctx, item.cfg.RequestTimeout)
	}
	return &adk.GenInputResult[*turnItem, *schema.Message]{
		RunCtx: runCtx,
		Input: &adk.TypedAgentInput[*schema.Message]{
			Messages:        []*schema.Message{item.current},
			EnableStreaming: item.cfg.Stream || item.toolsEnabled,
		},
		Consumed:  []*turnItem{item},
		Remaining: items[1:],
	}, nil
}

func (s *session) prepareAgent(_ context.Context, _ *adk.TurnLoop[*turnItem, *schema.Message], consumed []*turnItem) (adk.TypedAgent[*schema.Message], error) {
	if len(consumed) == 0 || consumed[0] == nil || consumed[0].agent == nil {
		return nil, fmt.Errorf("turn agent is required")
	}
	return consumed[0].agent, nil
}

func (s *session) onAgentEvents(ctx context.Context, tc *adk.TurnContext[*turnItem, *schema.Message], events *adk.AsyncIterator[*adk.AgentEvent]) error {
	if len(tc.Consumed) == 0 || tc.Consumed[0] == nil {
		return nil
	}
	item := tc.Consumed[0]
	if item.runCancel != nil {
		defer item.runCancel()
	}
	resp, err := collectEvents(ctx, events, item.cfg, item.timeline, item.emit)
	if err != nil {
		select {
		case <-tc.Preempted:
			return nil
		case <-tc.Stopped:
			return nil
		default:
		}
		if ctx.Err() != nil {
			return nil
		}
		_ = item.emit(ihtml.ChatStreamEvent{Type: "turn_error", Error: userFacingError(err), ModelReady: true})
		return nil
	}
	if strings.TrimSpace(resp.Message) != "" && !resp.Streamed {
		if err := item.emit(ihtml.ChatStreamEvent{Type: "assistant_delta", Delta: resp.Message, ModelReady: true, ToolCalls: resp.ToolCalls}); err != nil {
			return nil
		}
	}
	_ = item.emit(ihtml.ChatStreamEvent{Type: "assistant_done", Message: resp.Message, ModelReady: true, ToolCalls: resp.ToolCalls})
	return nil
}

// startConfirm 执行一张已确认的工具卡(独立于模型轮次)。
func (s *session) startConfirm(parent context.Context, ev *ihtml.ChatClientEvent) {
	s.stopLoop()
	ctx, cancel := context.WithCancel(parent)
	s.manualMu.Lock()
	s.manualSeq++
	seq := s.manualSeq
	s.manualCancel = cancel
	s.manualMu.Unlock()

	go func() {
		defer cancel()
		defer s.clearManual(seq)
		runID := fmt.Sprintf("ai_%d", time.Now().UnixNano())
		emit := s.newEmitter(runID, runID+"_confirm")
		if err := emit(ihtml.ChatStreamEvent{Type: "turn_start", ModelReady: true}); err != nil {
			return
		}
		callCtx := ctx
		if s.backend.cfg.RequestTimeout > 0 {
			var callCancel context.CancelFunc
			callCtx, callCancel = context.WithTimeout(ctx, s.backend.cfg.RequestTimeout)
			defer callCancel()
		}
		rt := &runtime{svc: s.svc, user: s.user, cfg: s.backend.cfg, signer: s.backend.signer, timeline: &timeline{emit: emit}, apis: s.apis, apiBase: s.apiBase, access: s.access}
		if err := executeConfirmedTool(callCtx, rt, ev.ToolCall, emit); err != nil && ctx.Err() == nil {
			_ = emit(ihtml.ChatStreamEvent{Type: "turn_error", Error: userFacingError(err), ModelReady: true})
		}
	}()
}

func (s *session) stopActiveWork() {
	s.stopLoop()
	s.cancelManual()
}

func (s *session) stopLoop() {
	s.loopMu.Lock()
	loop := s.loop
	cancel := s.loopCancel
	s.loop = nil
	s.loopCancel = nil
	s.loopMu.Unlock()
	if loop != nil {
		loop.Stop(adk.WithImmediate(), adk.WithSkipCheckpoint())
	}
	if cancel != nil {
		cancel()
	}
}

func (s *session) cancelManual() {
	s.manualMu.Lock()
	cancel := s.manualCancel
	s.manualCancel = nil
	s.manualMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *session) clearManual(seq int64) {
	s.manualMu.Lock()
	if s.manualSeq == seq {
		s.manualCancel = nil
	}
	s.manualMu.Unlock()
}

func currentUserTurn(messages []ihtml.ChatMessage) (*schema.Message, error) {
	if len(messages) > 80 {
		return nil, errors.New("消息条数超出限制")
	}
	total := 0
	for i := len(messages) - 1; i >= 0; i-- {
		total += len(messages[i].Content)
		if total > 64000 {
			return nil, errors.New("消息内容过长")
		}
	}
	for i := len(messages) - 1; i >= 0; i-- {
		content := strings.TrimSpace(messages[i].Content)
		if messages[i].Role == "user" && content != "" {
			if len(content) > 16000 {
				return nil, errors.New("单条消息过长")
			}
			return schema.UserMessage(content), nil
		}
	}
	return nil, errors.New("缺少用户消息")
}

func normalizeModel(model string) (string, error) {
	model = strings.TrimSpace(model)
	if len(model) > 256 {
		return "", errors.New("模型名过长")
	}
	if strings.ContainsFunc(model, unicode.IsControl) {
		return "", errors.New("模型名含非法字符")
	}
	return model, nil
}

func userFacingError(err error) string {
	if err == nil {
		return "未知错误"
	}
	msg := err.Error()
	if len(msg) > 400 {
		msg = msg[:400] + "…"
	}
	return msg
}
