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
	einoModel "github.com/cloudwego/eino/components/model"
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
	// ModelFactory optionally supplies a host's model transport (for example
	// agent/eino.NewChatModel with Responses). Agent, budgets, UI tools and
	// confirmation still belong to this Backend; do not create a second Agent.
	ModelFactory func(context.Context, Config) (einoModel.ToolCallingChatModel, error)
	Provider     string // openai / claude, 必填
	BaseURL      string // API 根地址, 必填(如 https://api.openai.com/v1)
	APIKey       string // OpenAI Bearer 或 Anthropic x-api-key；本地网关可留空
	AuthToken    string // Anthropic Authorization Bearer；仅 claude provider 使用
	Model        string // 默认模型；仅 AllowModelSelect=true 时允许前端另选

	Stream         bool          // 流式输出, 建议 true
	Thinking       bool          // 透出思考过程(需模型支持 reasoning)
	RequestTimeout time.Duration // 单轮超时, 0 不限
	MaxTokens      int
	// MaxProviderResponseBytes 是每次 provider HTTP 响应体的硬上限，
	// 默认 8 MiB。它限制真实读取量，不依赖 provider 是否遵守 MaxTokens；
	// 0 使用安全默认值，负数无效，不能关闭。
	MaxProviderResponseBytes int64
	// MaxModelOutputBytesPerCall / MaxModelOutputBytesPerTurn 分别限制单次
	// 模型调用与整轮共享的输出字节。计入正文、reasoning 和 tool-call
	// 字符串；默认 1 MiB / 4 MiB。0 用默认值，负数无效。
	MaxModelOutputBytesPerCall int64
	MaxModelOutputBytesPerTurn int64
	// MaxModelOutputEventsPerTurn 限制整轮所有模型流 chunk 和非流响应数，
	// 默认 4096，防止恶意 provider 用海量空/小 chunk 消耗 CPU。
	MaxModelOutputEventsPerTurn int
	// MaxAssistantMessageBytes 是最终可见回答快照的 UTF-8 字节上限，
	// 默认 256 KiB。MaxReasoningBytesPerTurn 是整轮投影到客户端的
	// reasoning 总上限，默认 256 KiB，且每个 segment 最多 64 KiB。
	// 二者均会在超限时产生明确的截断标记；0 用默认值，负数无效。
	MaxAssistantMessageBytes int64
	MaxReasoningBytesPerTurn int64
	// Temperature is the legacy non-zero override. Use TemperatureOverride when
	// the provider must receive an explicit zero value.
	Temperature float32
	// TemperatureOverride, when non-nil, takes precedence over Temperature and
	// preserves the distinction between "unset" and an explicit 0.
	TemperatureOverride *float32
	// ThinkingBudgetTokens 是 Claude extended thinking 的 token 预算。
	// Thinking=true 且该值为 0 时使用 1024；MaxTokens 必须更大。
	ThinkingBudgetTokens int
	// RuntimeConfigProvider 可由宿主注入每轮生成参数覆盖。它只允许调整模型、
	// thinking、token 与 temperature；网络、凭据、提示词、权限和预算仍由本
	// Config 控制。实现必须支持并发调用。
	RuntimeConfigProvider RuntimeConfigProvider

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
	// DisableDynamicToolSearch 关闭 Deep 模式默认启用的按需工具发现。
	// Fast 始终直接绑定工具以保持低延迟；Deep 默认先通过 tool_search 选择
	// 相关语义工具，避免把整个工具目录永久塞入每次模型调用。
	DisableDynamicToolSearch bool
	// DisableContextSummarization 关闭默认启用的安全会话压缩。压缩只作用于
	// Agent 上下文，不改变业务数据，也不会给摘要模型开放工具。
	DisableContextSummarization bool
	// SummarizeAfterTokens / SummarizeAfterMessages 任一阈值超过时压缩历史。
	// 0 分别使用 24000 tokens 与 80 条消息；只有明确关闭时才完全禁用。
	SummarizeAfterTokens   int
	SummarizeAfterMessages int
	// MaxModelCallsPerTurn 是根 Agent 与全部子 Agent 共享的模型调用上限，
	// 默认 24；负数表示不限。fast 仍同时受 MaxIterations 限制。
	MaxModelCallsPerTurn int
	// MaxModelRetries 是每次 Agent 模型调用遇到瞬态网络、限流或上游错误时
	// 的最大重试次数，默认 1；-1 禁用。认证、参数、策略与主动取消错误
	// 永不重试，所有重试仍计入 MaxModelCallsPerTurn。
	MaxModelRetries int
	// MaxSubtasksPerTurn 是 Deep 模式每轮最多启动的只读子任务数，默认 3；
	// 负数表示不限。fast 不启动子任务。
	MaxSubtasksPerTurn int
	// MaxToolCallsPerTurn 是根 Agent 与全部 Deep 子 Agent 共享的
	// 语义工具调用总上限，默认 32；负数表示不限。内部计划与
	// 子任务委派不计入，它们分别受计划校验和 MaxSubtasksPerTurn 约束。
	MaxToolCallsPerTurn int
	// MaxToolCallsPerTool 是单个语义工具每轮的调用上限，默认 8；
	// 负数表示不限。
	MaxToolCallsPerTool int
	// MaxRepeatedToolCalls 是同一工具使用完全相同的规范化 JSON
	// 参数可调用的次数，默认 2；负数表示不限。
	MaxRepeatedToolCalls int
	// MaxTurnDuration 是一轮（含全部子 Agent）的总墙钟预算，默认 2 分钟；
	// 负数表示不限。若 RequestTimeout 更短，则采用更短值。
	MaxTurnDuration time.Duration
	// MaxOutstandingHostCallbacks 是单个 Backend 同时仍未真实返回的关键宿主
	// 回调上限（RuntimeConfigProvider、拦截器与确认 nonce store），默认 32。
	// 仅接受 0（默认）或 1..4096；TurnObserver 使用独立的观察池。
	MaxOutstandingHostCallbacks int
	// MaxOutstandingObservations 是单个 Backend 同时仍未真实返回的
	// TurnObserver 回调上限，默认 32。观察过载时快照按 best-effort 丢弃，
	// 不会占用关键宿主回调额度；仅接受 0（默认）或 1..4096。
	MaxOutstandingObservations int
	// MaxOutstandingOperations 是单个 Backend 同时仍未真实返回的模型流与
	// Service 工具调用上限，默认 64；用于约束忽略 context 的第三方实现，
	// 仅接受 0（默认）或 1..4096。
	MaxOutstandingOperations int
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
	// SessionNamespace 是宿主应用/租户的稳定命名空间。使用外部
	// SessionStore 或 ConfirmNonceStore 时必须显式设置：它同时隔离持久化
	// 历史与确认卡的应用域。它不会以明文写入 Store key。
	SessionNamespace string
	// SessionCapabilityVersion 是宿主在非结构化能力变化时主动递增的版本，
	// 例如 ProbeRequest、WriteInterceptor 或自定义 Service 的授权语义改变。
	// 版本变化会安全轮换历史作用域。
	SessionCapabilityVersion string
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
	// TurnInterceptor 在每轮模型调用前执行(限流/配额/计费检查)。普通错误
	// 会被泛化；只有显式通过 NewUserFacingError 标记的安全文案才展示给用户。
	TurnInterceptor func(ctx context.Context, user string) error
	// TurnObserver 接收不含消息正文、工具参数和原始错误的结构化生命周期快照。
	// 回调错误或 panic 不会改变对话结果；实现必须支持并发调用。
	TurnObserver TurnObserver
}

// UserFacingError 是宿主明确声明可安全展示给终端用户的错误。不得把底层
// HTTP/SQL/凭据/堆栈错误直接实现为该接口；普通 error 会被统一泛化。
type UserFacingError interface {
	error
	UserFacingMessage() string
}

type userFacingHostError struct{ message string }

func (e userFacingHostError) Error() string             { return e.message }
func (e userFacingHostError) UserFacingMessage() string { return e.message }

// NewUserFacingError 把已经过宿主审查的短文案标记为可展示错误。
func NewUserFacingError(message string) error {
	message = sanitizeUserFacingMessage(message, 400)
	if message == "" {
		message = "操作未能完成。"
	}
	return userFacingHostError{message: message}
}

func (c *Config) ready() bool {
	return strings.TrimSpace(c.Provider) != "" && strings.TrimSpace(c.BaseURL) != ""
}

// Backend 实现 ihtml.ChatBackend 与 ihtml.ModelLister。
type Backend struct {
	cfg           Config
	signer        *confirmSigner
	writeLimiter  *userWriteLimiter
	hostCallbacks *hostCallbackLimiter
	observations  *hostCallbackLimiter
	operations    *operationLimiter
	// 会话历史存储放 Backend 级共享: 前端带稳定 csid 重连时,
	// 同一 SessionID 命中同一份历史, 断线/刷新不失忆。
	store   adk.SessionEventStore[*schema.Message]
	history *boundedSessionStore

	// ownerMu makes one Backend the single in-process owner of a scoped
	// SessionID. A reconnect replaces the map entry immediately, then waits on
	// the previous session's real shutdown barrier without blocking the
	// WebSocket reader. Cross-process ownership remains the host's concern.
	ownerMu sync.Mutex
	owners  map[string]*session
	// ownerBarriers keeps the original active predecessor barrier while the
	// latest connection is still waiting to activate. It flattens rapid
	// A -> B -> C takeovers instead of chaining through every idle successor.
	ownerBarriers map[string]<-chan struct{}
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
	if nilcheck.IsNil(cfg.RuntimeConfigProvider) {
		cfg.RuntimeConfigProvider = nil
	}
	if nilcheck.IsNil(cfg.TurnObserver) {
		cfg.TurnObserver = nil
	}
	if (cfg.SessionStore != nil || cfg.ConfirmNonceStore != nil) && strings.TrimSpace(cfg.SessionNamespace) == "" {
		// New remains the source-compatible unchecked constructor, but sharing an
		// unscoped external store must still fail safe. An ephemeral namespace
		// prevents cross-application history/confirmation reuse; use NewChecked with
		// an explicit stable namespace for replica or restart continuity.
		cfg.SessionNamespace = ephemeralSessionNamespace()
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
	if cfg.MaxProviderResponseBytes < minMaxProviderResponseBytes || cfg.MaxProviderResponseBytes > maxMaxProviderResponseBytes {
		cfg.MaxProviderResponseBytes = defaultMaxProviderResponseBytes
	}
	if cfg.MaxModelOutputBytesPerCall < minMaxModelOutputBytesPerCall || cfg.MaxModelOutputBytesPerCall > maxMaxModelOutputBytesPerCall {
		cfg.MaxModelOutputBytesPerCall = defaultMaxModelOutputBytesPerCall
	}
	if cfg.MaxModelOutputBytesPerTurn < minMaxModelOutputBytesPerTurn || cfg.MaxModelOutputBytesPerTurn > maxMaxModelOutputBytesPerTurn {
		cfg.MaxModelOutputBytesPerTurn = defaultMaxModelOutputBytesPerTurn
	}
	if cfg.MaxModelOutputBytesPerTurn < cfg.MaxModelOutputBytesPerCall {
		cfg.MaxModelOutputBytesPerCall = defaultMaxModelOutputBytesPerCall
		cfg.MaxModelOutputBytesPerTurn = defaultMaxModelOutputBytesPerTurn
	}
	if cfg.MaxModelOutputEventsPerTurn < minMaxModelOutputEventsPerTurn || cfg.MaxModelOutputEventsPerTurn > maxMaxModelOutputEventsPerTurn {
		cfg.MaxModelOutputEventsPerTurn = defaultMaxModelOutputEventsPerTurn
	}
	if cfg.MaxAssistantMessageBytes < minMaxAssistantMessageBytes || cfg.MaxAssistantMessageBytes > maxMaxAssistantMessageBytes {
		cfg.MaxAssistantMessageBytes = defaultMaxAssistantMessageBytes
	}
	if cfg.MaxReasoningBytesPerTurn < minMaxReasoningBytesPerTurn || cfg.MaxReasoningBytesPerTurn > maxMaxReasoningBytesPerTurn {
		cfg.MaxReasoningBytesPerTurn = defaultMaxReasoningBytesPerTurn
	}
	if cfg.MaxModelCallsPerTurn == 0 {
		cfg.MaxModelCallsPerTurn = 24
	}
	if cfg.MaxModelRetries == 0 {
		cfg.MaxModelRetries = 1
	}
	if cfg.SummarizeAfterTokens == 0 {
		cfg.SummarizeAfterTokens = defaultSummarizeAfterTokens
	}
	if cfg.SummarizeAfterMessages == 0 {
		cfg.SummarizeAfterMessages = defaultSummarizeAfterMessages
	}
	if cfg.MaxSubtasksPerTurn == 0 {
		cfg.MaxSubtasksPerTurn = 3
	}
	if cfg.MaxToolCallsPerTurn == 0 {
		cfg.MaxToolCallsPerTurn = defaultMaxToolCallsPerTurn
	}
	if cfg.MaxToolCallsPerTool == 0 {
		cfg.MaxToolCallsPerTool = defaultMaxToolCallsPerTool
	}
	if cfg.MaxRepeatedToolCalls == 0 {
		cfg.MaxRepeatedToolCalls = defaultMaxRepeatedToolCalls
	}
	if cfg.MaxTurnDuration == 0 {
		cfg.MaxTurnDuration = 2 * time.Minute
	}
	if cfg.MaxOutstandingHostCallbacks <= 0 || cfg.MaxOutstandingHostCallbacks > maxConfiguredOutstandingWork {
		cfg.MaxOutstandingHostCallbacks = defaultMaxOutstandingHostCallbacks
	}
	if cfg.MaxOutstandingObservations <= 0 || cfg.MaxOutstandingObservations > maxConfiguredOutstandingWork {
		cfg.MaxOutstandingObservations = defaultMaxOutstandingObservations
	}
	if cfg.MaxOutstandingOperations <= 0 || cfg.MaxOutstandingOperations > maxConfiguredOutstandingWork {
		cfg.MaxOutstandingOperations = defaultMaxOutstandingOperations
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
		cfg:           cfg,
		signer:        newConfirmSigner(cfg.ConfirmSecret, cfg.ConfirmNonceStore),
		writeLimiter:  newUserWriteLimiter(cfg.WritesPerMinute),
		hostCallbacks: newHostCallbackLimiter(cfg.MaxOutstandingHostCallbacks),
		observations:  newHostCallbackLimiter(cfg.MaxOutstandingObservations),
		operations:    newOperationLimiter(cfg.MaxOutstandingOperations),
		store:         store,
		history:       history,
	}
}

var csidPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// NewSession 实现 ihtml.ChatBackend。
func (b *Backend) NewSession(_ context.Context, sctx *ihtml.ChatSessionContext) (ihtml.ChatSession, error) {
	if sctx == nil {
		return nil, errors.New("chat session context is required")
	}
	if strings.TrimSpace(sctx.User) == "" || (nilcheck.IsNil(sctx.Service) && nilcheck.IsNil(sctx.ScopedService)) || sctx.Emit == nil {
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
	capabilityScope := sessionCapabilityFingerprint(b.cfg, sctx.Access, sctx.APIs)
	sessionID := secureSessionIDScoped(sctx.User, csid, capabilityScope)
	service := sctx.Service
	if sctx.ScopedService != nil {
		// Prefer the request-bound, continuously governed capability. The adapter
		// ignores every legacy user argument, so the model runtime cannot
		// accidentally cross a tenant even if a caller supplies the wrong value.
		adapter := scopedServiceAdapter{ScopedService: sctx.ScopedService}
		service = adapter
		if publisher, ok := sctx.ScopedService.(ihtml.ScopedPagePublicationService); ok {
			service = scopedPublishingServiceAdapter{scopedServiceAdapter: adapter, publisher: publisher}
		}
	}
	created := &session{
		backend:         b,
		user:            sctx.User,
		svc:             service,
		emit:            sctx.Emit,
		apis:            append([]ihtml.APISpec(nil), sctx.APIs...),
		apiBase:         strings.TrimSpace(b.cfg.ProbeBaseURL),
		inbound:         inboundHeaders,
		inboundQuery:    inboundQuery,
		access:          sctx.Access,
		sessionID:       sessionID,
		capabilityScope: capabilityScope,
		store:           b.store,
		inspect:         newInspectBroker(),
		ownerReady:      make(chan struct{}),
		ownerWaitCancel: make(chan struct{}),
		shutdownDone:    make(chan struct{}),
	}
	if b.history == nil {
		// External stores are arbitrary host code. Bind their callbacks to this
		// session so takeover/Close waits for a context-insensitive implementation
		// to really return, and share the Backend-wide operation ceiling across csids.
		created.store = guardExternalSessionStore(created, b.store)
	}
	if b.history != nil {
		// Acquire the successor's independent pin before revoking its predecessor,
		// so the bounded entry cannot become evictable during hand-off.
		b.history.acquire(sessionID)
	}
	b.claimSessionOwner(created)
	return created, nil
}

var sessionIDFallback atomic.Uint64

func ephemeralSessionNamespace() string {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err == nil {
		return fmt.Sprintf("ephemeral-%x", nonce[:])
	}
	return fmt.Sprintf("ephemeral-%d-%d", time.Now().UnixNano(), sessionIDFallback.Add(1))
}

// secureSessionID 用长度无歧义的哈希隔离 user/csid，避免字符串拼接造成跨用户
// 会话碰撞，也不把用户标识直接泄漏到外部 SessionStore 的键中。
func secureSessionID(user, csid string) string {
	return secureSessionIDScoped(user, csid, "")
}

func secureSessionIDScoped(user, csid, capabilityScope string) string {
	seed := csid
	if seed == "" {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err == nil {
			seed = fmt.Sprintf("ephemeral:%x", nonce[:])
		} else {
			seed = fmt.Sprintf("ephemeral:%d:%d", time.Now().UnixNano(), sessionIDFallback.Add(1))
		}
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf(
		"%d:%s:%d:%s:%d:%s",
		len(user), user, len(seed), seed, len(capabilityScope), capabilityScope,
	)))
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

	// workMu serializes lifecycle transitions between the ADK loop and manual
	// confirmation execution. Stop is intentionally non-blocking, so the loop
	// remains installed until Wait returns and queued turns cannot start a new
	// loop against the same SessionStore concurrently. manualDone extends the
	// same completion barrier to tools whose Service implementation ignores ctx.
	workMu       sync.Mutex
	loop         *adk.TurnLoop[*turnItem, *schema.Message]
	loopCancel   context.CancelFunc
	loopDone     chan struct{}
	loopStopping bool

	manualQueue            []pendingConfirmation
	manualDone             chan struct{}
	manualWorker           bool
	manualActiveCancel     context.CancelFunc
	manualActiveCancelOnly bool
	pendingIngress         []pendingUserMessage
	ingressDraining        bool
	ingressDone            chan struct{}
	ingressActiveCancel    context.CancelFunc
	pendingTurns           []pendingTurn
	activeOps              int
	opsDone                chan struct{}
	workEpoch              uint64
	closed                 bool
	approvalWindow         time.Time
	approvalEvents         int

	sessionID       string
	capabilityScope string
	store           adk.SessionEventStore[*schema.Message]
	inspect         *inspectBroker
	closeOnce       sync.Once

	// ownerReady closes only after the original active predecessor (including
	// any model, Store or Service that ignored cancellation) has fully returned.
	// Pending successors inherit ownerBarrier directly; ownerWaitCancel lets a
	// superseded pending connection exit without retaining a waiter chain.
	ownerReady      chan struct{}
	ownerBarrier    <-chan struct{}
	ownerWaitCancel chan struct{}
	ownerActivated  bool // guarded by workMu
	shutdownDone    chan struct{}
}

type turnItem struct {
	agent        adk.Agent
	repairModel  einoModel.ToolCallingChatModel
	cfg          Config
	mode         string
	budget       *turnBudget
	current      *schema.Message
	emit         func(ihtml.ChatStreamEvent) error
	runCancel    context.CancelFunc
	timeline     *timeline
	toolsEnabled bool
	observation  *turnObservationState
}

type pendingTurn struct {
	parent context.Context
	item   *turnItem
}

type pendingUserMessage struct {
	parent context.Context
	event  ihtml.ChatClientEvent
}

type pendingConfirmation struct {
	ctx        context.Context
	cancel     context.CancelFunc
	event      ihtml.ChatClientEvent
	loopDone   <-chan struct{}
	cancelOnly bool
}

const (
	maxPendingTurns         = 16
	maxPendingConfirmations = 8
	maxApprovalEventsMinute = 60
)

var errPendingTurnQueueFull = NewUserFacingError("待处理消息过多，请等待当前操作完成后再试。")

// HandleEvent 实现 ihtml.ChatSession。
func (s *session) HandleEvent(ctx context.Context, ev *ihtml.ChatClientEvent) {
	if ev == nil {
		_ = s.emit(ihtml.ChatStreamEvent{Type: "turn_error", Error: "缺少对话事件。", ModelReady: true})
		return
	}
	switch strings.TrimSpace(ev.Type) {
	case "user_message":
		s.cancelManual()
		if err := s.enqueueUserMessage(ctx, ev); err != nil {
			if ctx.Err() != nil {
				return
			}
			_ = s.emit(ihtml.ChatStreamEvent{Type: "turn_error", Error: userFacingError(err), ModelReady: true})
		}
	case "confirm_tool":
		if s.takeApprovalEvent() {
			s.startConfirm(ctx, ev)
		} else {
			s.rejectConfirmation(ev, "确认操作过于频繁，操作未执行，请稍后再试。")
		}
	case ihtml.ChatClientCancelTool:
		if s.takeApprovalEvent() {
			s.startCancel(ctx, ev)
		} else {
			s.emitCancellationFailure(ev, "确认操作过于频繁，请稍后再试。")
		}
	case "inspect_result":
		// 内核对 inspect_request 的应答: 路由给等待中的 ui_page_inspect 调用。
		if s.inspect != nil && strings.TrimSpace(ev.InspectID) != "" {
			s.inspect.deliver(strings.TrimSpace(ev.InspectID), ev.Inspect)
		}
	case "cancel":
		s.stopActiveWork()
		_ = s.emit(ihtml.ChatStreamEvent{
			Type:       ihtml.ChatStreamAssistantStatus,
			Status:     "已请求停止当前处理；已开始的外部操作可能仍在收尾，请核对最终结果。",
			ModelReady: true,
		})
	}
}

func (s *session) takeApprovalEvent() bool {
	if s == nil {
		return false
	}
	s.workMu.Lock()
	defer s.workMu.Unlock()
	// Sample time inside the serialized section. Sampling before the lock lets
	// an older timestamp acquire after a newer one and falsely look like a clock
	// rollback, resetting the window under concurrent approval frames.
	now := time.Now()
	if s.approvalWindow.IsZero() || now.Sub(s.approvalWindow) >= time.Minute || now.Before(s.approvalWindow) {
		s.approvalWindow = now
		s.approvalEvents = 0
	}
	if s.approvalEvents >= maxApprovalEventsMinute {
		return false
	}
	s.approvalEvents++
	return true
}

// Close 实现 ihtml.ChatSession。
func (s *session) Close() {
	s.closeOnce.Do(func() {
		s.closeActiveWork()
		s.workMu.Lock()
		activated := s.ownerActivated || s.ownerIsReadyLocked()
		ownerWaitCancel := s.ownerWaitCancel
		s.workMu.Unlock()
		if ownerWaitCancel != nil {
			close(ownerWaitCancel)
		}
		// Stop is deliberately non-blocking. Keep the bounded history entry and
		// ownership lease until the old loop and even context-insensitive host
		// calls have really returned; otherwise a successor could overlap them.
		// A connection superseded before activation owns no host work and skips
		// the inherited barrier, so rapid reconnects cannot form a waiter chain.
		go func() {
			if activated {
				s.waitForWorkShutdown()
			}
			s.completeShutdown()
		}()
	})
}

func (s *session) completeShutdown() {
	if s == nil {
		return
	}
	if s.backend != nil {
		s.backend.releaseSessionOwner(s)
		if s.backend.history != nil {
			s.backend.history.release(s.sessionID)
		}
	}
	if s.shutdownDone != nil {
		close(s.shutdownDone)
		if s.backend != nil {
			s.backend.clearClosedOwnerBarrier(s.sessionID, s.shutdownDone)
		}
	}
}

func (s *session) newEmitter(runID, turnID string) func(ihtml.ChatStreamEvent) error {
	var seq int64
	var mu sync.Mutex
	return func(out ihtml.ChatStreamEvent) error {
		mu.Lock()
		defer mu.Unlock()
		out = boundedToolStreamEvent(out)
		seq++
		out.RunID = runID
		out.TurnID = turnID
		out.Seq = seq
		if out.Type == ihtml.ChatStreamAssistantDelta || out.Type == ihtml.ChatStreamAssistantDone {
			// Model output follows the built-in HTML response contract. Programmatic
			// answers explicitly declare text and must retain that safer format.
			if out.ContentFormat == "" {
				out.ContentFormat = ihtml.ChatContentFormatHTML
			}
		} else {
			out.ContentFormat = ""
		}
		return s.emit(out)
	}
}

func (s *session) enqueueTurn(parent context.Context, ev *ihtml.ChatClientEvent) (returnErr error) {
	epoch := s.snapshotWorkEpoch()
	runID := fmt.Sprintf("ai_%d", time.Now().UnixNano())
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
	observation := newTurnObservationState(cfg.TurnObserver, runID, runID+"_turn", s.sessionID, mode, toolsEnabled, cfg, s.backend.observations)
	baseEmit := s.newEmitter(runID, runID+"_turn")
	emit := func(event ihtml.ChatStreamEvent) error {
		err := baseEmit(event)
		observation.observeEvent(event)
		return err
	}
	defer func() {
		if returnErr != nil {
			observation.fail(TurnFailureInternal)
		}
	}()
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
		observation.fail(TurnFailureConfiguration)
		return emit(ihtml.ChatStreamEvent{
			Type:          ihtml.ChatStreamAssistantDone,
			ModelReady:    false,
			Message:       "AI 服务还没有配置, 请在服务端设置 Provider 与 BaseURL。",
			ContentFormat: ihtml.ChatContentFormatText,
		})
	}
	if cfg.TurnInterceptor != nil {
		if err := runHostInterceptorBounded(parent, func(callCtx context.Context) error {
			return cfg.TurnInterceptor(callCtx, s.user)
		}, s.backend.hostCallbacks); err != nil {
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
	observation.updateConfig(cfg)
	// Client selection is validated first; the trusted host provider then gets
	// final authority over effective generation settings for this turn.
	resolveCtx, resolveCancel := runtimeConfigResolveContext(parent, budget)
	cfg, err = resolveRuntimeConfigBounded(resolveCtx, cfg.RuntimeConfigProvider, cfg, runtimeConfigRequest(cfg, s.user, s.sessionID, mode, toolsEnabled), s.backend.hostCallbacks)
	resolveCancel()
	if err != nil {
		return emit(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamTurnError, Error: "本轮模型配置不可用，请稍后重试。", ModelReady: true})
	}
	observation.updateConfig(cfg)
	observation.start()
	if strings.TrimSpace(cfg.Model) == "" {
		observation.fail(TurnFailureConfiguration)
		message := "服务端尚未配置默认模型。"
		if cfg.AllowModelSelect {
			message = "请先在助手顶部选择或填写模型名。"
		}
		return emit(ihtml.ChatStreamEvent{
			Type: ihtml.ChatStreamAssistantDone, ModelReady: true,
			Message: message, ContentFormat: ihtml.ChatContentFormatText,
		})
	}
	if err := emit(ihtml.ChatStreamEvent{Type: "assistant_status", Status: "正在连接模型。", ModelReady: true}); err != nil {
		return err
	}
	chatModel, err := newChatModel(parent, cfg)
	if err != nil {
		return emit(ihtml.ChatStreamEvent{Type: "turn_error", Error: userFacingError(err), ModelReady: true})
	}
	chatModel = withTurnBudget(chatModel, budget)
	chatModel = trackChatModel(s, chatModel)
	tl := &timeline{emit: emit, mode: mode}
	rt := &runtime{svc: s.svc, user: s.user, confirmation: newConfirmationScope(s.sessionID, s.capabilityScope), cfg: cfg, mode: mode, signer: s.backend.signer, writeLimiter: s.backend.writeLimiter, hostCallbacks: s.backend.hostCallbacks, timeline: tl, apis: s.apis, apiBase: s.apiBase, inbound: s.inbound, inboundQuery: s.inboundQuery, access: s.access, inspect: s.inspect, budget: budget, session: s}
	// 系统提示注入"页面现状"清单: 省一次 ui_item_list 往返, 也防盲写撞 id。
	// 所有模式指令和动态上下文都放在最终 HTML 回答契约之前。
	trustedPrompt := []string{inventoryPrompt(parent, s.svc, s.user)}
	if mode == AgentModeDeep {
		trustedPrompt = append(trustedPrompt, deepModePrompt)
	}
	instruction := buildSystemPrompt(cfg, ev.ClientContext, s.apis, trustedPrompt...)
	agent, err := buildTurnAgent(parent, mode, instruction, chatModel, rt, budget, cfg, toolsEnabled)
	if err != nil {
		return emit(ihtml.ChatStreamEvent{Type: "turn_error", Error: userFacingError(err), ModelReady: true})
	}
	if err := emit(ihtml.ChatStreamEvent{Type: "assistant_status", Status: "正在理解需求。", ModelReady: true}); err != nil {
		return err
	}
	item := &turnItem{
		agent:        agent,
		repairModel:  chatModel,
		cfg:          cfg,
		mode:         mode,
		budget:       budget,
		current:      current,
		emit:         emit,
		timeline:     tl,
		toolsEnabled: toolsEnabled,
		observation:  observation,
	}
	if err := s.queueTurnAtEpoch(parent, item, epoch); err != nil {
		if errors.Is(err, errTurnSuperseded) {
			observation.fail(TurnFailureCanceled)
			return nil
		}
		return err
	}
	return nil
}

var errTurnSuperseded = errors.New("chat turn was superseded")

func (s *session) queueTurn(parent context.Context, item *turnItem) error {
	return s.queueTurnAtEpoch(parent, item, s.snapshotWorkEpoch())
}

func (s *session) queueTurnAtEpoch(parent context.Context, item *turnItem, epoch uint64) error {
	if parent == nil {
		parent = context.Background()
	}
	s.workMu.Lock()
	if epoch != s.workEpoch {
		s.workMu.Unlock()
		return errTurnSuperseded
	}
	if s.closed {
		s.workMu.Unlock()
		return errors.New("chat session is closed")
	}
	if !s.ownerIsReadyLocked() {
		if !s.appendPendingTurnLocked(pendingTurn{parent: parent, item: item}) {
			s.workMu.Unlock()
			return errPendingTurnQueueFull
		}
		s.workMu.Unlock()
		return nil
	}
	if s.manualDone != nil || s.loopStopping || (s.loop == nil && s.activeOps > 0) {
		if !s.appendPendingTurnLocked(pendingTurn{parent: parent, item: item}) {
			s.workMu.Unlock()
			return errPendingTurnQueueFull
		}
		s.workMu.Unlock()
		return nil
	}
	loop := s.loop
	if loop == nil {
		loop = s.startLoopLocked(parent)
	}
	ok, _ := loop.Push(item, adk.WithPreemptTimeout[*turnItem, *schema.Message](adk.AnySafePoint, 2*time.Second))
	if ok {
		s.workMu.Unlock()
		return nil
	}
	// The loop may have observed parent cancellation immediately before Push.
	// Keep the item locally and wait for Wait rather than creating a competing
	// loop while the old one can still append to SessionStore.
	if !s.appendPendingTurnLocked(pendingTurn{parent: parent, item: item}) {
		loop, cancel, _ := s.markLoopStoppingLocked()
		s.workMu.Unlock()
		stopTurnLoop(loop, cancel)
		return errPendingTurnQueueFull
	}
	loop, cancel, _ := s.markLoopStoppingLocked()
	s.workMu.Unlock()
	stopTurnLoop(loop, cancel)
	return nil
}

func (s *session) appendPendingTurnLocked(turn pendingTurn) bool {
	if len(s.pendingTurns)+len(s.pendingIngress) >= maxPendingTurns {
		return false
	}
	s.pendingTurns = append(s.pendingTurns, turn)
	return true
}

func (s *session) snapshotWorkEpoch() uint64 {
	s.workMu.Lock()
	epoch := s.workEpoch
	s.workMu.Unlock()
	return epoch
}

// startLoopLocked installs a loop while workMu is held. The loop is not removed
// until Wait has returned, which is the single-flight boundary for SessionStore
// and for context-insensitive model/tool implementations.
func (s *session) startLoopLocked(parent context.Context) *adk.TurnLoop[*turnItem, *schema.Message] {
	loopCtx, cancel := context.WithCancel(parent)
	loop := adk.NewTurnLoop[*turnItem, *schema.Message](adk.TurnLoopConfig[*turnItem, *schema.Message]{
		GenInput:      s.genInput,
		PrepareAgent:  s.prepareAgent,
		OnAgentEvents: s.onAgentEvents,
		SessionID:     s.sessionID,
		SessionStore:  s.store,
	})
	done := make(chan struct{})
	s.loop = loop
	s.loopCancel = cancel
	s.loopDone = done
	s.loopStopping = false
	loop.Run(loopCtx)
	go s.waitLoop(loop, done)
	return loop
}

func (s *session) waitLoop(loop *adk.TurnLoop[*turnItem, *schema.Message], done chan struct{}) {
	_ = loop.Wait()
	s.workMu.Lock()
	if s.loop == loop {
		s.loop = nil
		s.loopCancel = nil
		s.loopDone = nil
		s.loopStopping = false
	}
	close(done)
	s.workMu.Unlock()
	s.drainPendingTurns()
}

// drainPendingTurns is called only after a lifecycle barrier completes. It
// preserves arrival order and never blocks the WebSocket event handler waiting
// for a model or tool that ignores cancellation.
func (s *session) drainPendingTurns() {
	var canceled []*turnItem
	var stopLoop *adk.TurnLoop[*turnItem, *schema.Message]
	var stopCancel context.CancelFunc

	s.workMu.Lock()
	if s.closed {
		for _, pending := range s.pendingTurns {
			canceled = append(canceled, pending.item)
		}
		s.pendingTurns = nil
		s.workMu.Unlock()
		failCanceledTurns(canceled)
		return
	}
	if !s.ownerIsReadyLocked() || s.manualDone != nil || s.loopStopping || (s.loop == nil && s.activeOps > 0) || len(s.pendingTurns) == 0 {
		s.workMu.Unlock()
		return
	}

	pending := s.pendingTurns
	s.pendingTurns = nil
	for index, turn := range pending {
		if turn.item == nil || turn.parent == nil || turn.parent.Err() != nil {
			canceled = append(canceled, turn.item)
			continue
		}
		loop := s.loop
		if loop == nil {
			loop = s.startLoopLocked(turn.parent)
		}
		ok, _ := loop.Push(turn.item, adk.WithPreemptTimeout[*turnItem, *schema.Message](adk.AnySafePoint, 2*time.Second))
		if ok {
			continue
		}
		// Preserve this item and all later arrivals for the successor loop.
		s.pendingTurns = append(s.pendingTurns, pending[index:]...)
		stopLoop, stopCancel, _ = s.markLoopStoppingLocked()
		break
	}
	s.workMu.Unlock()

	failCanceledTurns(canceled)
	stopTurnLoop(stopLoop, stopCancel)
}

func failCanceledTurns(items []*turnItem) {
	for _, item := range items {
		if item != nil && item.observation != nil {
			item.observation.fail(TurnFailureCanceled)
		}
	}
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
			EnableStreaming: item.cfg.Stream,
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
			item.observation.fail(TurnFailureCanceled)
			return nil
		case <-tc.Stopped:
			item.observation.fail(TurnFailureCanceled)
			return nil
		default:
		}
		if ctx.Err() != nil {
			kind := TurnFailureCanceled
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				kind = TurnFailureTimeout
			}
			item.observation.fail(kind)
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
		resp = item.repairVisibleAnswer(ctx, resp)
		_ = item.emit(ihtml.ChatStreamEvent{
			Type: "assistant_agent", ModelReady: true, Budget: item.budget.snapshot(),
			Agent: &ihtml.ChatAgentStage{Mode: item.mode, Agent: "root", TaskID: "root", Phase: "turn", Status: "completed"},
		})
	}
	if strings.TrimSpace(resp.Message) != "" && !resp.Streamed {
		if err := emitBatchedAssistantDeltaEvent(item.emit, &thinkingState{}, resp.Message, resp.ContentFormat, resp.ToolCalls); err != nil {
			item.observation.fail(TurnFailureInternal)
			return nil
		}
	}
	// The budget wrapper sees root, summary, repair and Deep subagent calls, so
	// its aggregate is the complete provider usage for this turn. Event-level
	// collection remains a fallback for adapters that expose no model metadata.
	if usage := item.budget.usageSnapshot(); usage != nil {
		resp.Usage = usage
	}
	_ = item.emit(ihtml.ChatStreamEvent{
		Type: ihtml.ChatStreamAssistantDone, Message: resp.Message,
		ContentFormat: resp.ContentFormat, ModelReady: true,
		ToolCalls: resp.ToolCalls, Budget: item.budget.snapshot(),
		FinishReason: resp.FinishReason, Usage: resp.Usage, Repaired: resp.Repaired,
	})
	return nil
}

func (item *turnItem) repairVisibleAnswer(ctx context.Context, resp collectResp) collectResp {
	if item == nil || item.current == nil || item.emit == nil || !resp.NeedsRepair || len(resp.ToolCalls) > 0 || item.timeline == nil {
		return resp
	}
	evidence := item.timeline.completedEvidence()
	if len(evidence) == 0 {
		return resp
	}
	_ = item.emit(ihtml.ChatStreamEvent{
		Type: ihtml.ChatStreamAssistantStatus, Status: "工具已经完成，正在整理最终答复。", ModelReady: true,
	})
	repaired, err := repairFinalAnswer(ctx, item.repairModel, item.cfg, item.current.Content, evidence)
	if err != nil {
		return resp
	}
	if err := emitAssistantReset(item.emit); err != nil {
		return resp
	}
	resp.Message = repaired.Message
	resp.Streamed = false
	resp.ContentFormat = ihtml.ChatContentFormatHTML
	resp.FinishReason = repaired.FinishReason
	resp.NeedsRepair = false
	resp.Repaired = true
	if repaired.Usage != nil {
		if resp.Usage == nil {
			resp.Usage = &ihtml.ChatUsage{}
		}
		addChatUsage(resp.Usage, repaired.Usage)
	}
	return resp
}

// startConfirm and startCancel enter one ordered approval lane. The websocket
// reader never calls a host nonce store directly, and confirm/cancel frames
// cannot overtake one another at the nonce CAS boundary.
func (s *session) startConfirm(parent context.Context, ev *ihtml.ChatClientEvent) {
	s.enqueueApproval(parent, ev, false)
}

func (s *session) startCancel(parent context.Context, ev *ihtml.ChatClientEvent) {
	s.enqueueApproval(parent, ev, true)
}

func (s *session) enqueueApproval(parent context.Context, ev *ihtml.ChatClientEvent, cancelOnly bool) {
	if ev == nil {
		if cancelOnly {
			s.emitCancellationFailure(ev, "撤销请求无效，执行状态未确认。")
		}
		return
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)

	// Reserve the manual-execution barrier before stopping the loop. This closes
	// the old gap in which a new user turn could install another loop after Stop
	// but before confirmation execution became visible.
	s.workMu.Lock()
	if s.closed {
		s.workMu.Unlock()
		cancel()
		if cancelOnly {
			s.emitCancellationFailure(ev, "会话已关闭，无法确认撤销。")
		} else {
			s.rejectConfirmation(ev, "会话已关闭，操作未执行。")
		}
		return
	}
	if len(s.manualQueue) >= maxPendingConfirmations {
		s.workMu.Unlock()
		cancel()
		if cancelOnly {
			s.emitCancellationFailure(ev, "待处理确认操作过多，无法确认撤销。")
		} else {
			s.rejectConfirmation(ev, "待确认操作过多，操作未执行，请等待当前操作完成后再试。")
		}
		return
	}
	if s.manualDone == nil {
		s.manualDone = make(chan struct{})
	}
	var pending []pendingTurn
	var loop *adk.TurnLoop[*turnItem, *schema.Message]
	var loopCancel context.CancelFunc
	var loopDone <-chan struct{}
	var ingressCancel context.CancelFunc
	if !cancelOnly {
		s.workEpoch++
		pending = s.pendingTurns
		s.pendingTurns = nil
		// Raw turns waiting for a predecessor owner have not emitted anything and
		// are invalidated by the epoch just like already-built pending turns.
		s.pendingIngress = nil
		ingressCancel = s.ingressActiveCancel
		loop, loopCancel, loopDone = s.markLoopStoppingLocked()
	}
	eventCopy := *ev
	if ev.ToolCall != nil {
		toolCopy := *ev.ToolCall
		if ev.ToolCall.Query != nil {
			toolCopy.Query = make(map[string]any, len(ev.ToolCall.Query))
			for key, value := range ev.ToolCall.Query {
				toolCopy.Query[key] = value
			}
		}
		eventCopy.ToolCall = &toolCopy
	}
	s.manualQueue = append(s.manualQueue, pendingConfirmation{
		ctx: ctx, cancel: cancel, event: eventCopy, loopDone: loopDone, cancelOnly: cancelOnly,
	})
	startWorker := !s.manualWorker
	if startWorker {
		s.manualWorker = true
	}
	s.workMu.Unlock()

	if !cancelOnly {
		if ingressCancel != nil {
			ingressCancel()
		}
		failPendingTurns(pending)
		stopTurnLoop(loop, loopCancel)
	}

	if startWorker {
		go s.runConfirmationWorker()
	}
}

func (s *session) runConfirmationWorker() {
	// Ownership hand-off is asynchronous so confirm_tool never blocks the WS
	// reader, but execution cannot cross the predecessor's shutdown barrier.
	_ = s.waitForOwnerReady()
	for {
		s.workMu.Lock()
		if len(s.manualQueue) == 0 {
			done := s.manualDone
			s.manualDone = nil
			s.manualWorker = false
			s.manualActiveCancel = nil
			s.manualActiveCancelOnly = false
			if done != nil {
				close(done)
			}
			s.workMu.Unlock()
			s.drainPendingTurns()
			return
		}
		pending := s.manualQueue[0]
		s.manualQueue[0] = pendingConfirmation{}
		s.manualQueue = s.manualQueue[1:]
		s.manualActiveCancel = pending.cancel
		s.manualActiveCancelOnly = pending.cancelOnly
		s.workMu.Unlock()

		if pending.cancelOnly {
			// Cancellation does not need the model loop or active Service operations;
			// it only retains FIFO order with earlier approval frames.
			if pending.ctx.Err() == nil && !s.isClosed() {
				callCtx := pending.ctx
				if timeout := confirmationExecutionTimeout(s.backend.cfg); timeout > 0 {
					var callCancel context.CancelFunc
					callCtx, callCancel = context.WithTimeout(pending.ctx, timeout)
					s.cancelConfirmationWithAck(callCtx, &pending.event)
					callCancel()
				} else {
					s.cancelConfirmationWithAck(callCtx, &pending.event)
				}
			}
		} else {
			// Always wait for real predecessor completion even after cancellation. A
			// provider or Service may ignore ctx; the next confirmation must not overlap.
			if pending.loopDone != nil {
				<-pending.loopDone
			}
			s.waitOperationsIdle()
			if pending.ctx.Err() == nil && !s.isClosed() {
				s.executeQueuedConfirmation(pending.ctx, &pending.event)
			}
		}
		pending.cancel()

		s.workMu.Lock()
		s.manualActiveCancel = nil
		s.manualActiveCancelOnly = false
		s.workMu.Unlock()
	}
}

func (s *session) executeQueuedConfirmation(ctx context.Context, ev *ihtml.ChatClientEvent) {
	runID := fmt.Sprintf("ai_%d", time.Now().UnixNano())
	emit := s.newEmitter(runID, runID+"_confirm")
	if err := emit(ihtml.ChatStreamEvent{Type: "turn_start", ModelReady: true}); err != nil {
		return
	}
	done, err := s.tryBeginOperation()
	if err != nil {
		message := "AI 服务当前繁忙，操作未执行，请稍后重试。"
		if !s.emitConfirmationFailure(emit, ev, message) {
			_ = emit(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamTurnError, Error: message, ModelReady: true})
		}
		return
	}
	defer done()
	callCtx := ctx
	if timeout := confirmationExecutionTimeout(s.backend.cfg); timeout > 0 {
		var callCancel context.CancelFunc
		callCtx, callCancel = context.WithTimeout(ctx, timeout)
		defer callCancel()
	}
	mode := AgentModeFast
	if ev.ToolCall != nil && ev.ToolCall.AgentMode == AgentModeDeep {
		mode = AgentModeDeep
	}
	rt := &runtime{svc: s.svc, user: s.user, confirmation: newConfirmationScope(s.sessionID, s.capabilityScope), cfg: s.backend.cfg, mode: mode, signer: s.backend.signer, writeLimiter: s.backend.writeLimiter, hostCallbacks: s.backend.hostCallbacks, timeline: &timeline{emit: emit, mode: mode}, apis: s.apis, apiBase: s.apiBase, inbound: s.inbound, inboundQuery: s.inboundQuery, access: s.access, inspect: s.inspect, session: s}
	if err := executeConfirmedTool(callCtx, rt, ev.ToolCall, emit); err != nil && ctx.Err() == nil {
		_ = emit(ihtml.ChatStreamEvent{Type: "turn_error", Error: userFacingError(err), ModelReady: true})
	}
}

func confirmationExecutionTimeout(cfg Config) time.Duration {
	duration := cfg.MaxTurnDuration
	if duration < 0 {
		duration = 0
	}
	if cfg.RequestTimeout > 0 && (duration == 0 || cfg.RequestTimeout < duration) {
		duration = cfg.RequestTimeout
	}
	return duration
}

func (s *session) stopActiveWork() {
	s.stopWork(false)
}

func (s *session) stopLoop() {
	s.workMu.Lock()
	s.workEpoch++
	pending := s.pendingTurns
	s.pendingTurns = nil
	loop, cancel, _ := s.markLoopStoppingLocked()
	s.workMu.Unlock()
	failPendingTurns(pending)
	stopTurnLoop(loop, cancel)
}

func (s *session) cancelManual() {
	s.workMu.Lock()
	cancels := make([]context.CancelFunc, 0, len(s.manualQueue)+1)
	if s.manualActiveCancel != nil && !s.manualActiveCancelOnly {
		cancels = append(cancels, s.manualActiveCancel)
	}
	for _, pending := range s.manualQueue {
		if pending.cancel != nil && !pending.cancelOnly {
			cancels = append(cancels, pending.cancel)
		}
	}
	s.workMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (s *session) isClosed() bool {
	s.workMu.Lock()
	closed := s.closed
	s.workMu.Unlock()
	return closed
}

func (s *session) waitForWorkShutdown() {
	// A session superseded before it became ready still inherits its
	// predecessor's barrier. Waiting here preserves A -> B -> C serialization.
	_ = s.waitForOwnerReady()
	for {
		s.workMu.Lock()
		if s.loop == nil && s.manualDone == nil && s.activeOps == 0 && !s.ingressDraining {
			s.workMu.Unlock()
			return
		}
		loopDone := s.loopDone
		manualDone := s.manualDone
		opsDone := s.opsDone
		ingressDone := s.ingressDone
		s.workMu.Unlock()

		if loopDone != nil {
			<-loopDone
		}
		if manualDone != nil {
			<-manualDone
		}
		if opsDone != nil {
			<-opsDone
		}
		if ingressDone != nil {
			<-ingressDone
		}
	}
}

func (s *session) closeActiveWork() {
	s.stopWork(true)
}

func (s *session) stopWork(closeSession bool) {
	s.workMu.Lock()
	s.workEpoch++
	if closeSession {
		s.closed = true
	}
	pending := s.pendingTurns
	s.pendingTurns = nil
	s.pendingIngress = nil
	ingressCancel := s.ingressActiveCancel
	manualCancels := make([]context.CancelFunc, 0, len(s.manualQueue)+1)
	if s.manualActiveCancel != nil {
		manualCancels = append(manualCancels, s.manualActiveCancel)
	}
	for _, pending := range s.manualQueue {
		if pending.cancel != nil {
			manualCancels = append(manualCancels, pending.cancel)
		}
	}
	s.manualQueue = nil
	loop, loopCancel, _ := s.markLoopStoppingLocked()
	s.workMu.Unlock()

	if ingressCancel != nil {
		ingressCancel()
	}
	for _, cancel := range manualCancels {
		cancel()
	}
	failPendingTurns(pending)
	stopTurnLoop(loop, loopCancel)
}

// markLoopStoppingLocked leaves the loop installed until its Wait goroutine
// closes loopDone. Callers must hold workMu and invoke stopTurnLoop after unlock.
func (s *session) markLoopStoppingLocked() (*adk.TurnLoop[*turnItem, *schema.Message], context.CancelFunc, <-chan struct{}) {
	if s.loop == nil {
		return nil, nil, nil
	}
	s.loopStopping = true
	return s.loop, s.loopCancel, s.loopDone
}

func stopTurnLoop(loop *adk.TurnLoop[*turnItem, *schema.Message], cancel context.CancelFunc) {
	if loop != nil {
		loop.Stop(adk.WithImmediate(), adk.WithSkipCheckpoint())
	}
	if cancel != nil {
		cancel()
	}
}

func failPendingTurns(turns []pendingTurn) {
	items := make([]*turnItem, 0, len(turns))
	for _, turn := range turns {
		items = append(items, turn.item)
	}
	failCanceledTurns(items)
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

func userFacingError(err error) (message string) {
	// Host interceptors and SDK extension points can return arbitrary error
	// implementations. A broken Error/Is/As/UserFacingMessage method must not
	// take down the session while we are already handling a failure.
	defer func() {
		if recover() != nil {
			message = "操作失败，请稍后重试。"
		}
	}()
	if err == nil {
		return "未知错误"
	}
	if errors.Is(err, context.Canceled) {
		return "操作已取消。"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "模型响应超时，请稍后重试。"
	}
	var public UserFacingError
	if errors.As(err, &public) {
		if message := sanitizeUserFacingMessage(public.UserFacingMessage(), 400); message != "" {
			return message
		}
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
	for _, safe := range []string{
		"Deep 模式需要启用工具", "服务端未开启 Deep 模式", "服务端不允许客户端覆盖模型",
	} {
		if msg == safe {
			return safe
		}
	}
	return "操作失败，请稍后重试。"
}

func sanitizeUserFacingMessage(message string, maxBytes int) string {
	message = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, message)
	message = strings.TrimSpace(message)
	if len(message) > maxBytes {
		message = clipRunes(message, maxBytes) + "…"
	}
	return message
}
