// Package chat 是 ihtml 的 AI 对话后端: 基于 CloudWeGo eino ADK,
// 通过语义工具直接增删改用户的 UI Item / 主题 / KV, 并把过程以流式事件
// 推回页面内核的对话抽屉。线格式与 casino admin AI 一致。
package chat

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/internal/nilcheck"
)

// Provider 取值。openai 指一切 OpenAI 兼容端点(含各类网关/Ollama),
// claude 指 Anthropic 原生协议。
const (
	ProviderOpenAI = "openai"
	ProviderClaude = "claude"

	AgentModeFast = "fast"
	AgentModeDeep = "deep"
)

// Config 是对话后端配置。
type Config struct {
	Provider  string // openai / claude, 必填
	BaseURL   string // API 根地址, 必填(如 https://api.openai.com/v1)
	APIKey    string // OpenAI Bearer 或 Anthropic x-api-key；本地网关可留空
	AuthToken string // Anthropic Authorization Bearer；仅 claude provider 使用
	Model     string // 默认模型；仅 AllowModelSelect=true 时允许前端另选

	Stream         bool          // 流式输出, 建议 true
	Thinking       bool          // 透出思考过程(需模型支持 reasoning)
	RequestTimeout time.Duration // 单轮超时, 0 不限
	MaxTokens      int
	// Temperature is the legacy non-zero override. Use TemperatureOverride when
	// the provider must receive an explicit zero value.
	Temperature float32
	// TemperatureOverride, when non-nil, takes precedence over Temperature and
	// preserves the distinction between "unset" and an explicit 0.
	TemperatureOverride *float32
	// ThinkingBudgetTokens 是 Claude extended thinking 的 token 预算。
	// Thinking=true 且该值为 0 时使用 1024；MaxTokens 必须更大。
	ThinkingBudgetTokens int

	// SystemPrompt 追加在内置系统提示之后, 用于业务定制(如"你是某某产品的助手")。
	SystemPrompt string
	// ConfirmWrites 为 true 时所有中风险写操作也要人工确认。
	// 无论此项为何值，高风险操作和可能安装 HTML/JS/CSS 的 Item 写入都会确认。
	ConfirmWrites bool
	// AllowUnconfirmedItemWrites 仅供完全信任模型和提示词的受控环境使用。
	// 开启后，普通 ui_item_put 可不经人工确认直接执行；整体替换和回滚仍会确认。
	AllowUnconfirmedItemWrites bool
	// MaxIterations 是单轮的模型-工具循环上限, 默认 16。
	MaxIterations int
	// EnableDeepAgent 显式开放多 Agent 深度模式。默认关闭；客户端只有在该项
	// 开启后才能为某轮请求 agent_mode=deep，空值始终走 fast。
	EnableDeepAgent bool
	// MaxModelCallsPerTurn 是根 Agent 与全部子 Agent 共享的模型调用上限，
	// 默认 24；负数表示不限。fast 仍同时受 MaxIterations 限制。
	MaxModelCallsPerTurn int
	// MaxSubtasksPerTurn 是 Deep 模式每轮最多启动的只读子任务数，默认 3；
	// 负数表示不限。fast 不启动子任务。
	MaxSubtasksPerTurn int
	// MaxTurnDuration 是一轮（含全部子 Agent）的总墙钟预算，默认 2 分钟；
	// 负数表示不限。若 RequestTimeout 更短，则采用更短值。
	MaxTurnDuration time.Duration
	// ConfirmSecret 是确认卡 HMAC 密钥。不设置则每进程随机。
	// 固定密钥只保证跨实例验签；若确认卡还需跨重启/实例一次性核销，必须同时
	// 配置共享的 ConfirmNonceStore。默认 nonce store 是进程内存态。
	ConfirmSecret []byte
	// ConfirmNonceStore 保存并原子核销确认 nonce。多副本部署应提供共享实现。
	ConfirmNonceStore ConfirmNonceStore
	// AllowModelSelect 为 true 时前端展示模型选择器。默认隐藏:
	// 模型是运维概念；选择器隐藏时必须由 Config.Model 明确配置。
	AllowModelSelect bool

	// ProbeBaseURL 是 api_probe 唯一可信的目标源（如 https://app.internal）。
	// 不配置时禁用探测；绝不会从客户端可控的 Request.Host 推导。
	ProbeBaseURL string
	// ProbeHTTPClient 可注入 mTLS、专用 Transport 等。其重定向策略会被安全
	// 策略覆盖：只允许同源且仍命中 API 目录的重定向。
	ProbeHTTPClient *http.Client
	// ProbeForwardHeaders 是从 WebSocket 握手请求转发到探测请求的显式白名单。
	// 默认不转发任何身份；如需当前会话身份，可明确加入 Cookie/Authorization。
	ProbeForwardHeaders []string
	// ProbeForwardQuery 是从握手 URL 转发的查询键白名单（例如旧系统的 uid）。
	// 转发值会覆盖模型提供的同名 query，防止模型借该键冒充其他身份。
	ProbeForwardQuery []string
	// ProbeRequest 在发出探测前注入服务身份。inbound 是握手请求头的副本，
	// outbound 是该探测请求的请求头；回调无法改写方法、URL、Host 或 Body。
	ProbeRequest func(ctx context.Context, user string, inbound, outbound http.Header) error

	// SessionStore 可替换默认的有界内存会话历史。自定义实现需要自行负责持久化、
	// 容量限制和淘汰；默认实现支持 csid 重连并按以下四个上限淘汰。
	SessionStore adk.SessionEventStore[*schema.Message]
	// MaxHistorySessions 默认 128；负数表示不按会话数淘汰。
	MaxHistorySessions int
	// MaxHistoryEvents 默认每会话 256；负数表示不按事件数截断。
	MaxHistoryEvents int
	// MaxHistoryBytes 默认每会话 2 MiB；负数表示不按字节数截断。
	MaxHistoryBytes int64
	// HistoryTTL 默认 12 小时；负数表示不按空闲时间淘汰。
	HistoryTTL time.Duration

	// WritesPerMinute 是 chat 工具的独立写限额，避免工具调用绕过 HTTP 限流。
	// 0 使用默认值 240，负数禁用。批量工具调用按一次写计数。
	WritesPerMinute int
	// WriteInterceptor 在每次真正执行写工具前调用，可接入共享配额/审计网关。
	// 返回错误则拒绝该次写入。
	WriteInterceptor func(ctx context.Context, user, tool string) error
	// TurnInterceptor 在每轮模型调用前执行(限流/配额/计费检查),
	// 返回错误则该轮以 turn_error 结束(错误文案直接展示给用户)。
	TurnInterceptor func(ctx context.Context, user string) error
}

func (c *Config) ready() bool {
	return strings.TrimSpace(c.Provider) != "" && strings.TrimSpace(c.BaseURL) != ""
}

// Backend 实现 ihtml.ChatBackend 与 ihtml.ModelLister。
type Backend struct {
	cfg          Config
	signer       *confirmSigner
	writeLimiter *userWriteLimiter
	// 会话历史存储放 Backend 级共享: 前端带稳定 csid 重连时,
	// 同一 SessionID 命中同一份历史, 断线/刷新不失忆。
	store   adk.SessionEventStore[*schema.Message]
	history *boundedSessionStore
}

// New 创建对话后端。
func New(cfg Config) *Backend {
	// New is retained as the source-compatible unchecked constructor. Normalize
	// typed-nil optional dependencies to the same safe defaults as plain nil;
	// NewChecked/Config.Validate report them as configuration errors instead.
	if nilcheck.IsNil(cfg.SessionStore) {
		cfg.SessionStore = nil
	}
	if nilcheck.IsNil(cfg.ConfirmNonceStore) {
		cfg.ConfirmNonceStore = nil
	}
	if cfg.ProbeHTTPClient != nil {
		client := *cfg.ProbeHTTPClient
		if nilcheck.IsNil(client.Transport) {
			client.Transport = nil
		}
		if nilcheck.IsNil(client.Jar) {
			client.Jar = nil
		}
		cfg.ProbeHTTPClient = &client
	}
	if cfg.MaxIterations <= 0 {
		cfg.MaxIterations = 16
	}
	if cfg.MaxModelCallsPerTurn == 0 {
		cfg.MaxModelCallsPerTurn = 24
	}
	if cfg.MaxSubtasksPerTurn == 0 {
		cfg.MaxSubtasksPerTurn = 3
	}
	if cfg.MaxTurnDuration == 0 {
		cfg.MaxTurnDuration = 2 * time.Minute
	}
	cfg.ProbeForwardHeaders = append([]string(nil), cfg.ProbeForwardHeaders...)
	cfg.ProbeForwardQuery = append([]string(nil), cfg.ProbeForwardQuery...)
	if cfg.TemperatureOverride != nil {
		value := *cfg.TemperatureOverride
		cfg.TemperatureOverride = &value
	}
	store := cfg.SessionStore
	var history *boundedSessionStore
	if store == nil {
		history = newBoundedSessionStore(historyLimitsFromConfig(cfg))
		store = history
	}
	return &Backend{
		cfg:          cfg,
		signer:       newConfirmSigner(cfg.ConfirmSecret, cfg.ConfirmNonceStore),
		writeLimiter: newUserWriteLimiter(cfg.WritesPerMinute),
		store:        store,
		history:      history,
	}
}

var csidPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// NewSession 实现 ihtml.ChatBackend。
func (b *Backend) NewSession(_ context.Context, sctx *ihtml.ChatSessionContext) (ihtml.ChatSession, error) {
	if sctx == nil {
		return nil, errors.New("chat session context is required")
	}
	if strings.TrimSpace(sctx.User) == "" || nilcheck.IsNil(sctx.Service) || sctx.Emit == nil {
		return nil, errors.New("chat session requires user, service and emitter")
	}
	if sctx.ScopedService != nil && nilcheck.IsNil(sctx.ScopedService) {
		return nil, errors.New("chat session scoped service must not contain a typed nil implementation")
	}
	csid := ""
	inboundHeaders := make(http.Header)
	inboundQuery := make(url.Values)
	if sctx.Request != nil {
		if sctx.Request.URL != nil {
			requestQuery := sctx.Request.URL.Query()
			if v := requestQuery.Get("csid"); csidPattern.MatchString(v) {
				csid = v
			}
			for _, rawKey := range b.cfg.ProbeForwardQuery {
				key := strings.TrimSpace(rawKey)
				if key != "" {
					inboundQuery[key] = append([]string(nil), requestQuery[key]...)
				}
			}
		}
		inboundHeaders = sctx.Request.Header.Clone()
	}
	sessionID := secureSessionID(sctx.User, csid)
	if b.history != nil {
		b.history.acquire(sessionID)
	}
	return &session{
		backend:      b,
		user:         sctx.User,
		svc:          sctx.Service,
		emit:         sctx.Emit,
		apis:         append([]ihtml.APISpec(nil), sctx.APIs...),
		apiBase:      strings.TrimSpace(b.cfg.ProbeBaseURL),
		inbound:      inboundHeaders,
		inboundQuery: inboundQuery,
		access:       sctx.Access,
		sessionID:    sessionID,
		store:        b.store,
		inspect:      newInspectBroker(),
	}, nil
}

var sessionIDFallback atomic.Uint64

// secureSessionID 用长度无歧义的哈希隔离 user/csid，避免字符串拼接造成跨用户
// 会话碰撞，也不把用户标识直接泄漏到外部 SessionStore 的键中。
func secureSessionID(user, csid string) string {
	seed := csid
	if seed == "" {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err == nil {
			seed = fmt.Sprintf("ephemeral:%x", nonce[:])
		} else {
			seed = fmt.Sprintf("ephemeral:%d:%d", time.Now().UnixNano(), sessionIDFallback.Add(1))
		}
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%s:%d:%s", len(user), user, len(seed), seed)))
	return fmt.Sprintf("ihtml-%x", sum[:20])
}

// session 对应一条 WS 连接: 多轮历史存 ADK SessionStore(内存态),
// 前端每轮只发当前这条 user 消息; 新会话 = 前端重开 WS。
type session struct {
	backend      *Backend
	user         string
	svc          ihtml.Service
	emit         func(ihtml.ChatStreamEvent) error
	apis         []ihtml.APISpec
	apiBase      string
	inbound      http.Header
	inboundQuery url.Values
	access       ihtml.Access

	loopMu     sync.Mutex
	loop       *adk.TurnLoop[*turnItem, *schema.Message]
	loopCancel context.CancelFunc

	manualMu     sync.Mutex
	manualCancel context.CancelFunc
	manualSeq    int64

	sessionID string
	store     adk.SessionEventStore[*schema.Message]
	inspect   *inspectBroker
	closeOnce sync.Once
}

type turnItem struct {
	agent        adk.Agent
	cfg          Config
	mode         string
	budget       *turnBudget
	current      *schema.Message
	emit         func(ihtml.ChatStreamEvent) error
	runCancel    context.CancelFunc
	timeline     *timeline
	toolsEnabled bool
}

// HandleEvent 实现 ihtml.ChatSession。
func (s *session) HandleEvent(ctx context.Context, ev *ihtml.ChatClientEvent) {
	if ev == nil {
		_ = s.emit(ihtml.ChatStreamEvent{Type: "turn_error", Error: "缺少对话事件。", ModelReady: true})
		return
	}
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
	case "inspect_result":
		// 内核对 inspect_request 的应答: 路由给等待中的 ui_page_inspect 调用。
		if s.inspect != nil && strings.TrimSpace(ev.InspectID) != "" {
			s.inspect.deliver(strings.TrimSpace(ev.InspectID), ev.Inspect)
		}
	case "cancel":
		s.stopActiveWork()
		_ = s.emit(ihtml.ChatStreamEvent{Type: "assistant_status", Status: "已取消当前请求。", ModelReady: true})
	}
}

// Close 实现 ihtml.ChatSession。
func (s *session) Close() {
	s.closeOnce.Do(func() {
		s.stopActiveWork()
		if s.backend.history != nil {
			s.backend.history.release(s.sessionID)
		}
	})
}

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
	cfg := s.backend.cfg
	mode, modeErr := selectAgentMode(cfg, ev.AgentMode)
	if mode == "" {
		mode = AgentModeFast
	}
	toolsEnabled := true
	if ev.ToolsEnabled != nil {
		toolsEnabled = *ev.ToolsEnabled
	}
	if mode == AgentModeDeep && !toolsEnabled && modeErr == nil {
		modeErr = errors.New("deep 模式需要启用工具")
	}
	budget := newTurnBudget(cfg, mode, emit)
	approvalResume := ""
	if mode == AgentModeDeep {
		approvalResume = "manual_tool_only"
	}
	if err := emit(ihtml.ChatStreamEvent{
		Type: "turn_start", ModelReady: true, Budget: budget.snapshot(),
		Agent: &ihtml.ChatAgentStage{Mode: mode, Agent: "root", TaskID: "root", Phase: "turn", Status: "running", ApprovalResume: approvalResume},
	}); err != nil {
		return err
	}
	if modeErr != nil {
		return emit(ihtml.ChatStreamEvent{Type: "turn_error", Error: modeErr.Error(), ModelReady: true, Budget: budget.snapshot()})
	}
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
	cfg, err = selectTurnModel(cfg, ev.Model)
	if err != nil {
		return emit(ihtml.ChatStreamEvent{Type: "turn_error", Error: err.Error(), ModelReady: true})
	}
	if strings.TrimSpace(cfg.Model) == "" {
		message := "服务端尚未配置默认模型。"
		if cfg.AllowModelSelect {
			message = "请先在助手顶部选择或填写模型名。"
		}
		return emit(ihtml.ChatStreamEvent{Type: "assistant_done", ModelReady: true, Message: message})
	}
	if err := emit(ihtml.ChatStreamEvent{Type: "assistant_status", Status: "正在连接模型。", ModelReady: true}); err != nil {
		return err
	}
	chatModel, err := newChatModel(parent, cfg)
	if err != nil {
		return emit(ihtml.ChatStreamEvent{Type: "turn_error", Error: userFacingError(err), ModelReady: true})
	}
	chatModel = withTurnBudget(chatModel, budget)
	tl := &timeline{emit: emit, mode: mode}
	rt := &runtime{svc: s.svc, user: s.user, cfg: cfg, mode: mode, signer: s.backend.signer, writeLimiter: s.backend.writeLimiter, timeline: tl, apis: s.apis, apiBase: s.apiBase, inbound: s.inbound, inboundQuery: s.inboundQuery, access: s.access, inspect: s.inspect}
	// 系统提示注入"页面现状"清单: 省一次 ui_item_list 往返, 也防盲写撞 id。
	instruction := buildSystemPrompt(cfg, ev.ClientContext, s.apis) + inventoryPrompt(parent, s.svc, s.user)
	agent, err := buildTurnAgent(parent, mode, instruction, chatModel, rt, budget, cfg, toolsEnabled)
	if err != nil {
		return emit(ihtml.ChatStreamEvent{Type: "turn_error", Error: userFacingError(err), ModelReady: true})
	}
	if err := emit(ihtml.ChatStreamEvent{Type: "assistant_status", Status: "正在理解需求。", ModelReady: true}); err != nil {
		return err
	}
	item := &turnItem{
		agent:        agent,
		cfg:          cfg,
		mode:         mode,
		budget:       budget,
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
	if item.budget != nil && !item.budget.deadline.IsZero() {
		runCtx, item.runCancel = context.WithDeadline(ctx, item.budget.deadline)
	} else if item.cfg.RequestTimeout > 0 {
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
		_ = item.emit(ihtml.ChatStreamEvent{
			Type: "assistant_agent", ModelReady: true, Budget: item.budget.snapshot(),
			Agent: &ihtml.ChatAgentStage{Mode: item.mode, Agent: "root", TaskID: "root", Phase: "turn", Status: "error", Summary: userFacingError(err)},
		})
		_ = item.emit(ihtml.ChatStreamEvent{Type: "turn_error", Error: userFacingError(err), ModelReady: true, Budget: item.budget.snapshot()})
		return nil
	}
	if item.mode == AgentModeDeep && len(resp.ToolCalls) > 0 {
		// 当前 WS 确认协议仍在模型循环之外核销。Deep 在此强制停止且覆盖
		// 模型措辞，绝不让待确认操作被描述成已经完成。
		resp = enforceDeepConfirmationPause(item.mode, resp, item.timeline)
		_ = item.emit(ihtml.ChatStreamEvent{
			Type: "assistant_agent", ModelReady: true, Budget: item.budget.snapshot(),
			Agent: &ihtml.ChatAgentStage{Mode: item.mode, Agent: "root", TaskID: "root", Phase: "approval", Status: "paused", Summary: deepApprovalPausedMessage, ApprovalResume: "manual_tool_only"},
		})
	} else {
		_ = item.emit(ihtml.ChatStreamEvent{
			Type: "assistant_agent", ModelReady: true, Budget: item.budget.snapshot(),
			Agent: &ihtml.ChatAgentStage{Mode: item.mode, Agent: "root", TaskID: "root", Phase: "turn", Status: "completed"},
		})
	}
	if strings.TrimSpace(resp.Message) != "" && !resp.Streamed {
		if err := item.emit(ihtml.ChatStreamEvent{Type: "assistant_delta", Delta: resp.Message, ModelReady: true, ToolCalls: resp.ToolCalls}); err != nil {
			return nil
		}
	}
	_ = item.emit(ihtml.ChatStreamEvent{Type: "assistant_done", Message: resp.Message, ModelReady: true, ToolCalls: resp.ToolCalls, Budget: item.budget.snapshot()})
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
		mode := AgentModeFast
		if ev.ToolCall != nil && ev.ToolCall.AgentMode == AgentModeDeep {
			mode = AgentModeDeep
		}
		rt := &runtime{svc: s.svc, user: s.user, cfg: s.backend.cfg, mode: mode, signer: s.backend.signer, writeLimiter: s.backend.writeLimiter, timeline: &timeline{emit: emit, mode: mode}, apis: s.apis, apiBase: s.apiBase, inbound: s.inbound, inboundQuery: s.inboundQuery, access: s.access, inspect: s.inspect}
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

func selectAgentMode(cfg Config, requested string) (string, error) {
	requested = strings.ToLower(strings.TrimSpace(requested))
	switch requested {
	case "", AgentModeFast:
		return AgentModeFast, nil
	case AgentModeDeep:
		if !cfg.EnableDeepAgent {
			return "", errors.New("服务端未开启 Deep 模式")
		}
		return AgentModeDeep, nil
	default:
		return "", fmt.Errorf("不支持的 Agent 模式 %q", requested)
	}
}

func selectTurnModel(cfg Config, requested string) (Config, error) {
	configured, err := normalizeModel(cfg.Model)
	if err != nil {
		return cfg, fmt.Errorf("服务端模型配置无效: %w", err)
	}
	requested, err = normalizeModel(requested)
	if err != nil {
		return cfg, err
	}
	cfg.Model = configured
	if cfg.AllowModelSelect {
		if requested != "" {
			cfg.Model = requested
		}
		return cfg, nil
	}
	if requested != "" && configured != "" && requested != configured {
		return cfg, errors.New("服务端不允许客户端覆盖模型")
	}
	return cfg, nil
}

func userFacingError(err error) string {
	if err == nil {
		return "未知错误"
	}
	if errors.Is(err, context.Canceled) {
		return "操作已取消。"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "模型响应超时，请稍后重试。"
	}
	msg := strings.TrimSpace(err.Error())
	lower := strings.ToLower(msg)
	// 对话事件会直接呈现给终端用户，不能把 eino 节点路径、
	// 网关 URL 或底层 dial/TLS 诊断投影进 UI。服务端仍可在自己的模型
	// transport/observability 中记录原始错误。
	switch {
	case strings.Contains(lower, "model_not_found"), strings.Contains(lower, "model not found"),
		strings.Contains(lower, "model does not exist"), strings.Contains(lower, "unknown model"),
		strings.Contains(lower, "模型不存在"):
		return "模型不存在或当前账号无权使用，请检查模型名称。"
	case strings.Contains(lower, "status code: 401"), strings.Contains(lower, "status code: 403"),
		strings.Contains(lower, "status=401"), strings.Contains(lower, "status=403"),
		strings.Contains(lower, "http 401"), strings.Contains(lower, "http 403"),
		strings.Contains(lower, "401 unauthorized"), strings.Contains(lower, "403 forbidden"),
		strings.Contains(lower, "unauthorized"), strings.Contains(lower, "authentication"):
		return "模型服务鉴权失败，请检查服务端配置。"
	case strings.Contains(lower, "status code: 429"), strings.Contains(lower, "status=429"),
		strings.Contains(lower, "http 429"), strings.Contains(lower, "rate limit"), strings.Contains(lower, "too many requests"):
		return "模型服务当前繁忙，请稍后重试。"
	case strings.Contains(lower, "connection refused"), strings.Contains(lower, "no such host"),
		strings.Contains(lower, "dial tcp"), strings.Contains(lower, "tls handshake"),
		strings.Contains(lower, "connection reset"), strings.Contains(lower, "unexpected eof"):
		return "无法连接模型服务，请检查服务端配置或稍后重试。"
	case strings.Contains(lower, "400 bad request"), strings.Contains(lower, "status code: 400"),
		strings.Contains(lower, "status=400"), strings.Contains(lower, "invalid_request_error"):
		return "模型服务拒绝了请求，请检查模型名称与参数配置。"
	case strings.Contains(lower, "noderunerror"), strings.Contains(lower, "node path:"):
		return "模型执行失败，请稍后重试。"
	}
	if cut := strings.Index(msg, "------------------------"); cut >= 0 {
		msg = strings.TrimSpace(msg[:cut])
	}
	if cut := strings.IndexAny(msg, "\r\n"); cut >= 0 {
		msg = strings.TrimSpace(msg[:cut])
	}
	if len(msg) > 400 {
		msg = msg[:400] + "…"
	}
	if msg == "" {
		return "未知错误"
	}
	return msg
}
