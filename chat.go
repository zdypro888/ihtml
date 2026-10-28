package ihtml

import (
	"context"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"

	"github.com/zdypro888/ihtml/internal/nilcheck"
)

// 对话走 WebSocket, 线格式与 casino admin AI 助手保持一致,
// 便于既有的 eino/ADK 后端与前端经验直接平移。

const (
	// ChatProtocolVersion identifies the additive WebSocket/capabilities contract.
	ChatProtocolVersion = 1
	// MaxChatOutboundFrameBytes is the public wire ceiling for one server-to-client
	// chat event. Backends that pre-build signed or otherwise atomic payloads
	// should reserve envelope headroom below this value.
	MaxChatOutboundFrameBytes = (1 << 20) - 1

	ChatClientUserMessage = "user_message"
	ChatClientConfirmTool = "confirm_tool"
	// ChatClientCancelTool revokes one signed confirmation card without running
	// it. The backend verifies the card and atomically consumes its nonce, so a
	// later confirm_tool replay is rejected.
	ChatClientCancelTool = "cancel_tool"
	ChatClientCancel     = "cancel"
	// ChatClientInspectResult 是内核对 inspect_request 的应答帧,
	// 携带 inspect_id 与实时页面渲染状态(不可信数据)。
	ChatClientInspectResult = "inspect_result"

	ChatStreamTurnStart         = "turn_start"
	ChatStreamAssistantStatus   = "assistant_status"
	ChatStreamAssistantThinking = "assistant_thinking"
	ChatStreamAssistantDelta    = "assistant_delta"
	// ChatStreamAssistantSnapshot 是后台重连的完整文本快照，替换该轮已有文本，不是追加。
	ChatStreamAssistantSnapshot = "assistant_snapshot"
	// ChatStreamAssistantReset discards assistant deltas already projected for
	// the current turn. The built-in backend uses it before a streaming retry;
	// legacy clients may ignore it because assistant_done remains authoritative.
	ChatStreamAssistantReset       = "assistant_reset"
	ChatStreamAssistantPlan        = "assistant_plan"
	ChatStreamAssistantAgent       = "assistant_agent"
	ChatStreamAssistantBudget      = "assistant_budget"
	ChatStreamToolStart            = "tool_start"
	ChatStreamConfirmationRequired = "confirmation_required"
	// ChatStreamCancelToolResult is the authoritative acknowledgement for one
	// cancel_tool request. ToolID and ConfirmationNonce correlate the result to
	// the exact card without echoing its signed token or arguments.
	ChatStreamCancelToolResult = "cancel_tool_result"
	ChatStreamToolResult       = "tool_result"
	ChatStreamAssistantDone    = "assistant_done"
	ChatStreamTurnError        = "turn_error"
	// TurnPaused ends the transport segment, not the durable Agent execution.
	ChatStreamTurnPaused = "turn_paused"
	// ChatStreamInspectRequest 由服务端(ui_page_inspect 工具)发起, 请求内核
	// 回传当前页面的实时渲染状态; 内核以 inspect_result 帧应答同一 inspect_id。
	ChatStreamInspectRequest = "inspect_request"

	ChatToolPending = "pending"
	ChatToolRunning = "running"
	ChatToolSuccess = "success"
	// ChatToolAccepted 表示异步请求已接收，尚未确认业务完成。
	ChatToolAccepted  = "accepted"
	ChatToolError     = "error"
	ChatToolCancelled = "cancelled"
	ChatToolUnknown   = "unknown"

	// ChatToolBudget* 是 assistant_budget 快照和内置语义工具
	// 返回的稳定机器可读值；客户端不应匹配本地化错误文案。
	ChatToolBudgetExhaustedCode   = "tool_call_budget_exhausted"
	ChatToolBudgetStatusExhausted = "budget_exhausted"
	ChatToolBudgetLimitTotal      = "total_tool_calls"
	ChatToolBudgetLimitPerTool    = "per_tool_calls"
	ChatToolBudgetLimitIdentical  = "identical_arguments"

	ChatRiskLow     = "low"
	ChatRiskMedium  = "medium"
	ChatRiskHigh    = "high"
	ChatRiskBlocked = "blocked"

	// ChatContentFormat* describe assistant answer bodies. Status, reasoning,
	// errors and tool summaries remain plain text regardless of this field.
	ChatContentFormatText     = "text"
	ChatContentFormatMarkdown = "markdown"
	ChatContentFormatHTML     = "html"
)

// ChatMessage 是一条对话消息。
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatClientViewport 描述前端视口, 供 AI 做屏幕自适应。
type ChatClientViewport struct {
	Width            int    `json:"width,omitempty"`
	Height           int    `json:"height,omitempty"`
	DevicePixelRatio any    `json:"device_pixel_ratio,omitempty"`
	Mode             string `json:"mode,omitempty"` // pc / mobile
}

// ChatClientPage 描述当前页面状态(路径/标题/可见文本), 属不可信数据。
type ChatClientPage struct {
	QueryFilters []ChatQueryFilter `json:"query_filters,omitempty"`
	Path         string            `json:"path,omitempty"`
	Title        string            `json:"title,omitempty"`
	VisibleText  string            `json:"visible_text,omitempty"`
}

type ChatQueryFilter struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ChatClientContext 是随 user_message 上送的页面上下文。
type ChatClientContext struct {
	Viewport  ChatClientViewport `json:"viewport"`
	Page      ChatClientPage     `json:"page"`
	Locale    string             `json:"locale,omitempty"`
	TimeZone  string             `json:"time_zone,omitempty"`
	UserAgent string             `json:"user_agent,omitempty"`
}

// ChatClientInspectionItem 是内核回传的单个 Item 实时渲染状态(不可信数据)。
type ChatClientInspectionItem struct {
	ID      string `json:"id"`
	Type    string `json:"type,omitempty"`
	Page    string `json:"page,omitempty"`
	Slot    string `json:"slot,omitempty"`
	Present bool   `json:"present"`           // 是否有已连接的 DOM 节点
	Visible bool   `json:"visible,omitempty"` // 是否实际占据可见空间
	Width   int    `json:"width,omitempty"`
	Height  int    `json:"height,omitempty"`
	Text    string `json:"text,omitempty"`  // 渲染文本摘要(截断)
	Error   string `json:"error,omitempty"` // 该 Item 最近一条运行错误
}

// ChatClientInspection 是内核对 inspect_request 的完整应答载荷(不可信数据)。
type ChatClientInspection struct {
	Page        string                     `json:"page,omitempty"` // 当前页面名, 空 = 单页/根
	Viewport    ChatClientViewport         `json:"viewport"`
	ThemeMode   string                     `json:"theme_mode,omitempty"` // light / dark
	Total       int                        `json:"total,omitempty"`      // 过滤后的 Item 总数
	Offset      int                        `json:"offset,omitempty"`
	Limit       int                        `json:"limit,omitempty"`
	NextOffset  int                        `json:"next_offset,omitempty"`
	Truncated   bool                       `json:"truncated,omitempty"` // 仍有后续分页
	Items       []ChatClientInspectionItem `json:"items,omitempty"`
	Errors      []string                   `json:"errors,omitempty"`       // 最近页面错误摘要
	VisibleText string                     `json:"visible_text,omitempty"` // 全页可见文本(截断)
}

// ChatClientEvent 是客户端发来的 WS 事件:
// type = user_message | confirm_tool | cancel_tool | cancel | inspect_result。
type ChatClientEvent struct {
	// RequestID is a client idempotency hint, not identity or authorization.
	// Hosts must namespace it by authenticated user and validate its length.
	RequestID string        `json:"request_id,omitempty"`
	Type      string        `json:"type"`
	Messages  []ChatMessage `json:"messages,omitempty"`
	Model     string        `json:"model,omitempty"`
	// AgentMode 为空或 fast 时走低延迟单 Agent；deep 仅在服务端显式开启后可用。
	AgentMode     string            `json:"agent_mode,omitempty"`
	ToolsEnabled  *bool             `json:"tools_enabled,omitempty"`
	ClientContext ChatClientContext `json:"client_context"`
	ToolCall      *ProposedToolCall `json:"tool_call,omitempty"`
	// InspectID/Inspect 仅用于 inspect_result: 应答服务端的同 ID inspect_request。
	InspectID string                `json:"inspect_id,omitempty"`
	Inspect   *ChatClientInspection `json:"inspect,omitempty"`
}

// ChatPlanItem 是 Deep 模式向前端投影的一条结构化任务。
type ChatPlanItem struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Status  string `json:"status"` // pending / in_progress / completed
}

// ChatAgentStage 描述主 Agent 或只读子 Agent 的阶段状态。它独立于工具卡，
// 前端可据此渲染层级任务，而无需解析模型文本。
type ChatAgentStage struct {
	Mode         string `json:"mode"`  // fast / deep
	Agent        string `json:"agent"` // root / ui_inspector / ui_reviewer
	TaskID       string `json:"task_id,omitempty"`
	ParentTaskID string `json:"parent_task_id,omitempty"`
	Phase        string `json:"phase,omitempty"` // turn / plan / inspect / review / approval
	Status       string `json:"status"`          // running / completed / paused / error
	Summary      string `json:"summary,omitempty"`
	// ApprovalResume 明示确认后的恢复能力：manual_tool_only 只执行确认卡，
	// checkpoint 由宿主持久化审批并恢复原计划，不由客户端猜测。
	ApprovalResume string `json:"approval_resume,omitempty"`
}

// ChatUsage is the normalized token accounting reported by the model provider.
// Providers may omit individual counters; zero therefore means either zero usage
// or unavailable data. Usage is observational and must never be used as an
// authorization or billing source without reconciliation at the provider.
type ChatUsage struct {
	InputTokens       int64 `json:"input_tokens,omitempty"`
	OutputTokens      int64 `json:"output_tokens,omitempty"`
	TotalTokens       int64 `json:"total_tokens,omitempty"`
	CachedInputTokens int64 `json:"cached_input_tokens,omitempty"`
	ReasoningTokens   int64 `json:"reasoning_tokens,omitempty"`
}

// ChatTurnBudget 是一轮共享预算的快照；Deep 根 Agent 与所有子 Agent 共用。
type ChatTurnBudget struct {
	ModelCalls           int64 `json:"model_calls"`
	MaxModelCalls        int64 `json:"max_model_calls,omitempty"`
	Subtasks             int64 `json:"subtasks"`
	MaxSubtasks          int64 `json:"max_subtasks,omitempty"`
	ToolCalls            int64 `json:"tool_calls"`
	MaxToolCalls         int64 `json:"max_tool_calls,omitempty"`
	MaxToolCallsPerTool  int64 `json:"max_tool_calls_per_tool,omitempty"`
	MaxRepeatedToolCalls int64 `json:"max_repeated_tool_calls,omitempty"`
	// ToolBudgetCode/Limit/Tool 仅在语义工具预算已锁存耗尽时设置。
	ToolBudgetCode  string `json:"tool_budget_code,omitempty"`
	ToolBudgetLimit string `json:"tool_budget_limit,omitempty"`
	ToolBudgetTool  string `json:"tool_budget_tool,omitempty"`
	DeadlineUnixMS  int64  `json:"deadline_unix_ms,omitempty"`
}

// ChatStreamEvent 是服务端推给前端的流式事件, type 取值:
// turn_start / assistant_status / assistant_thinking / assistant_delta /
// assistant_reset /
// assistant_plan / assistant_agent / assistant_budget / tool_start /
// confirmation_required / cancel_tool_result / tool_result / assistant_done /
// turn_error。
type ChatStreamEvent struct {
	// RestoredApproval marks a server-restored pending run, not client history.
	RestoredApproval bool `json:"restored_approval,omitempty"`
	// ContinuationPending 表示中间回执后将进入受管续办；不是整个请求的终态。
	ContinuationPending bool `json:"continuation_pending,omitempty"`
	// BackgroundSession/Seq 只标识可信后台回放，不代表客户端授权。
	BackgroundSession string `json:"background_session,omitempty"`
	BackgroundSeq     uint64 `json:"background_seq,omitempty"`
	BackgroundReplay  bool   `json:"background_replay,omitempty"`
	Type              string `json:"type"`
	RunID             string `json:"run_id,omitempty"`
	TurnID            string `json:"turn_id,omitempty"`
	Seq               int64  `json:"seq,omitempty"`
	Delta             string `json:"delta,omitempty"`
	// ContentFormat applies only to assistant_delta.delta and
	// assistant_done.message. Empty preserves the legacy auto-detected format;
	// clients must render unknown values as plain text. HTML is always untrusted
	// model output: third-party clients must parse it inertly and rebuild it
	// through a strict element/attribute/URL allowlist before attaching to DOM.
	ContentFormat string `json:"content_format,omitempty"`
	Thinking      string `json:"thinking,omitempty"`
	// ThinkingID groups the incremental reasoning events produced by one model
	// output. Legacy servers and clients may omit or ignore it.
	ThinkingID string `json:"thinking_id,omitempty"`
	// ThinkingDone marks the last event in a reasoning segment. A non-streamed
	// reasoning response can carry both Thinking and ThinkingDone in one event.
	ThinkingDone bool               `json:"thinking_done,omitempty"`
	Status       string             `json:"status,omitempty"`
	Message      string             `json:"message,omitempty"`
	ModelReady   bool               `json:"model_ready,omitempty"`
	ToolCalls    []ProposedToolCall `json:"tool_calls,omitempty"`
	ToolCall     *ProposedToolCall  `json:"tool_call,omitempty"`
	// ToolID/ConfirmationNonce are used by cancel_tool_result. They deliberately
	// expose only the identifiers the client already sent, never the signed card.
	ToolID            string          `json:"tool_id,omitempty"`
	ConfirmationNonce string          `json:"confirmation_nonce,omitempty"`
	ToolResult        json.RawMessage `json:"tool_result,omitempty"`
	ToolName          string          `json:"tool_name,omitempty"`
	ToolTitle         string          `json:"tool_title,omitempty"`
	ToolStatus        string          `json:"tool_status,omitempty"`
	ToolInput         string          `json:"tool_input,omitempty"`
	ToolSummary       string          `json:"tool_summary,omitempty"`
	ToolDebug         json.RawMessage `json:"tool_debug,omitempty"`
	Plan              []ChatPlanItem  `json:"plan,omitempty"`
	Agent             *ChatAgentStage `json:"agent,omitempty"`
	Budget            *ChatTurnBudget `json:"budget,omitempty"`
	// FinishReason and Usage are populated on assistant_done when the provider
	// exposes them. FinishReason is provider-defined; clients should display it
	// only as diagnostic metadata and must tolerate unknown values.
	FinishReason string     `json:"finish_reason,omitempty"`
	Usage        *ChatUsage `json:"usage,omitempty"`
	// Repaired reports that the visible answer was safely regenerated from
	// already-completed tool evidence after the original final answer was empty
	// or truncated. No tool is re-executed during this repair.
	Repaired bool   `json:"repaired,omitempty"`
	Error    string `json:"error,omitempty"`
	// InspectID 仅用于 inspect_request: 内核应答 inspect_result 时原样带回。
	InspectID     string   `json:"inspect_id,omitempty"`
	InspectOffset int      `json:"inspect_offset,omitempty"`
	InspectLimit  int      `json:"inspect_limit,omitempty"`
	InspectIDs    []string `json:"inspect_ids,omitempty"`
}

// ProposedToolCall 是一次工具调用的描述; 需人工确认的调用带 HMAC 签名的
// confirmation_token, 前端确认后原样带回执行。
// OperationReview is the host-provided, redacted business preview. It grants no
// authority; servers must revalidate the operation and live permissions.
type OperationReview struct {
	Scope          string `json:"scope"`
	Before         any    `json:"before"`
	After          any    `json:"after"`
	BlockingReason string `json:"blocking_reason,omitempty"`
}

// ToolCallPresentation customizes a host-issued confirmation card using plain
// text only. Empty fields use the localized SDK defaults. Presentation never
// grants authority; include it in the signed call and revalidate live access.
type ToolCallPresentation struct {
	ConfirmText string `json:"confirm_text,omitempty"`
	CancelText  string `json:"cancel_text,omitempty"`
	ReasonLabel string `json:"reason_label,omitempty"`
}

type ProposedToolCall struct {
	Presentation         *ToolCallPresentation `json:"presentation,omitempty"`
	Review               *OperationReview      `json:"review,omitempty"`
	ID                   string                `json:"id,omitempty"`
	Name                 string                `json:"name,omitempty"`
	Title                string                `json:"title,omitempty"`
	Method               string                `json:"method"`
	Path                 string                `json:"path"`
	Query                map[string]any        `json:"query,omitempty"`
	Body                 any                   `json:"body,omitempty"`
	Risk                 string                `json:"risk"` // low / medium / high / blocked
	RequiresConfirmation bool                  `json:"requires_confirmation"`
	Reason               string                `json:"reason"`
	AgentMode            string                `json:"agent_mode,omitempty"`
	ApprovalResume       string                `json:"approval_resume,omitempty"`
	ConfirmationToken    string                `json:"confirmation_token,omitempty"`
	ConfirmationNonce    string                `json:"confirmation_nonce,omitempty"`
	// ConfirmationExpires 在可信宿主 Preview 中可指定业务期限上限；签发后是实际签名到期时间（Unix秒）。
	ConfirmationExpires int64 `json:"confirmation_expires,omitempty"`
}

// ChatModels 是 GET /api/chat/models 的响应。
type ChatModels struct {
	ProtocolVersion int    `json:"protocol_version"`
	Provider        string `json:"provider,omitempty"`
	ModelReady      bool   `json:"model_ready"`
	// PreviewOnly 表示后端只提供确定性的界面预览，不代表真实模型已连接。
	// 客户端可继续开放输入以演示交互，但必须向用户说明回答并非来自 AI。
	PreviewOnly  bool     `json:"preview_only,omitempty"`
	CurrentModel string   `json:"current_model"`
	Models       []string `json:"models"`
	AgentModes   []string `json:"agent_modes,omitempty"`
	// DeepApprovalResume 由实际后端声明：manual_tool_only 仅执行当前卡片；
	// managed_turn 在回执保存后进入新受管轮次，后续写操作仍需分别确认；
	// checkpoint 要求原生中断/恢复和宿主持久化审批均已接通。
	DeepApprovalResume string `json:"deep_approval_resume,omitempty"`
	// Connection-scoped cards are invalidated on disconnect; the default is
	// session-scoped. The backend must enforce the same ownership rule.
	ConfirmationScope string `json:"confirmation_scope,omitempty"`
	// BackgroundExecution 表示传输断开不会取消后台轮次，重连会回放原执行进度。
	BackgroundExecution bool  `json:"background_execution,omitempty"`
	RequestTimeoutMS    int64 `json:"request_timeout_ms"`
	// ModelSelect 为 true 时前端展示模型选择器(默认隐藏——
	// 模型是运维概念, 不该暴露给终端用户)。
	ModelSelect bool `json:"model_select,omitempty"`
}

// APISpec 描述宿主提供的一个业务 API, 用于告知 AI "有哪些接口可用"。
type APISpec struct {
	Name        string `json:"name"`            // 标识, 如 player_search
	Title       string `json:"title,omitempty"` // 一句话名称
	Method      string `json:"method"`          // 目前探测工具只放行 GET
	Path        string `json:"path"`            // 页面同源绝对路径, 如 /capi/players
	Description string `json:"description"`     // 参数与返回结构说明(给 AI 看)
}

// ChatSessionContext 是为一条 WS 连接建立会话时交给后端的上下文。
type ChatSessionContext struct {
	// User 是 UserResolver 解析出的用户。
	User string
	// Service 用于读改 UI 数据。为兼容已有 ChatBackend 保留；新实现应优先
	// 使用 ScopedService，避免意外传入其他用户标识。
	Service Service
	// ScopedService 已绑定并持续校验当前用户，不暴露跨租户 user 参数。
	ScopedService ScopedService
	// Emit 向前端推流式事件, 并发安全; 连接断开后返回错误。
	Emit func(ChatStreamEvent) error
	// Request 是发起 WS 的原始请求, 供后端取额外参数/推导同源地址。
	Request *http.Request
	// APIs 是 WithAPIs 声明的业务 API 目录。
	APIs []APISpec
	// Access 是建立会话时的访问策略快照(只读时后端应隐藏/
	// 拒绝写工具)。Service 还会在每次调用时动态重验，防止撤权空窗。
	Access Access
}

// ChatBackend 为每条 WS 连接创建一个会话。子包 chat 提供基于 eino 的实现。
type ChatBackend interface {
	NewSession(ctx context.Context, sctx *ChatSessionContext) (ChatSession, error)
}

// ChatSession 处理一条连接上的客户端事件。实现应自行做好并发控制:
// HandleEvent 会在读循环 goroutine 里被顺序调用, 不应长时间阻塞。
type ChatSession interface {
	HandleEvent(ctx context.Context, ev *ChatClientEvent)
	Close()
}

// ModelLister 是 ChatBackend 的可选扩展: 提供可用模型列表。
type ModelLister interface {
	Models(ctx context.Context) (ChatModels, error)
}

const (
	chatWSWriteWait = 5 * time.Second
	chatWSPongWait  = 70 * time.Second
	chatWSPingEvery = 25 * time.Second

	// Keep every server frame strictly below the inbound one-MiB ceiling. The
	// remaining quotas bound a third-party backend for both one logical turn and
	// the lifetime of a connection. Turn boundaries are advanced only by the
	// handler after it accepts a client user_message; backend-provided IDs are
	// deliberately not trusted for accounting.
	chatWSMaxFrameBytes       = MaxChatOutboundFrameBytes
	chatWSMaxTurnEvents       = 4096
	chatWSMaxTurnBytes        = 4 << 20
	chatWSMaxConnectionEvents = 65536
	chatWSMaxConnectionBytes  = 64 << 20
	chatWSMaxJSONDepth        = 32
	chatWSMaxJSONNodes        = 65536
)

var (
	errChatWSOutputLimit  = errors.New("chat websocket output limit exceeded")
	errChatWSOutputClosed = errors.New("chat websocket output is closed")
	errChatWSUnsafeJSON   = errors.New("chat websocket output is not bounded JSON")

	chatWSJSONMarshalerType = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	chatWSTextMarshalerType = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
	chatWSTimeType          = reflect.TypeOf(time.Time{})
)

// chatWSWriter is the narrow gorilla connection surface used by the egress
// guard. Keeping it small makes the safety boundary testable without opening a
// network socket; *websocket.Conn implements it directly.
type chatWSWriter interface {
	SetWriteDeadline(time.Time) error
	WriteMessage(int, []byte) error
	WriteControl(int, []byte, time.Time) error
	Close() error
}

type chatWSEgressLimits struct {
	maxFrameBytes       int
	maxTurnEvents       uint64
	maxTurnBytes        uint64
	maxConnectionEvents uint64
	maxConnectionBytes  uint64
	maxJSONDepth        int
	maxJSONNodes        uint64
}

var defaultChatWSEgressLimits = chatWSEgressLimits{
	maxFrameBytes:       chatWSMaxFrameBytes,
	maxTurnEvents:       chatWSMaxTurnEvents,
	maxTurnBytes:        chatWSMaxTurnBytes,
	maxConnectionEvents: chatWSMaxConnectionEvents,
	maxConnectionBytes:  chatWSMaxConnectionBytes,
	maxJSONDepth:        chatWSMaxJSONDepth,
	maxJSONNodes:        chatWSMaxJSONNodes,
}

// chatWSEgressGuard is the final backend-agnostic boundary before an event is
// serialized to the wire. Counters and writes are protected by writeMu, which
// is shared with ping/control writes; closed is an atomic terminal fast path.
// Once a limit is crossed the guard emits one fixed, small error frame, closes
// the socket, and future calls fail before inspecting attacker-controlled values.
type chatWSEgressGuard struct {
	writer  chatWSWriter
	writeMu *sync.Mutex
	limits  chatWSEgressLimits

	closed           atomic.Bool
	terminalWritten  bool
	activeTurnKey    uint64
	turnActive       bool
	pendingTurns     uint64
	turnEvents       uint64
	turnBytes        uint64
	connectionEvents uint64
	connectionBytes  uint64
}

func newChatWSEgressGuard(writer chatWSWriter, writeMu *sync.Mutex) *chatWSEgressGuard {
	return newChatWSEgressGuardWithLimits(writer, writeMu, defaultChatWSEgressLimits)
}

func newChatWSEgressGuardWithLimits(writer chatWSWriter, writeMu *sync.Mutex, limits chatWSEgressLimits) *chatWSEgressGuard {
	if limits.maxFrameBytes <= 0 {
		limits.maxFrameBytes = defaultChatWSEgressLimits.maxFrameBytes
	}
	if limits.maxTurnEvents == 0 {
		limits.maxTurnEvents = defaultChatWSEgressLimits.maxTurnEvents
	}
	if limits.maxTurnBytes == 0 {
		limits.maxTurnBytes = defaultChatWSEgressLimits.maxTurnBytes
	}
	if limits.maxConnectionEvents == 0 {
		limits.maxConnectionEvents = defaultChatWSEgressLimits.maxConnectionEvents
	}
	if limits.maxConnectionBytes == 0 {
		limits.maxConnectionBytes = defaultChatWSEgressLimits.maxConnectionBytes
	}
	if limits.maxJSONDepth <= 0 {
		limits.maxJSONDepth = defaultChatWSEgressLimits.maxJSONDepth
	}
	if limits.maxJSONNodes == 0 {
		limits.maxJSONNodes = defaultChatWSEgressLimits.maxJSONNodes
	}
	return &chatWSEgressGuard{writer: writer, writeMu: writeMu, limits: limits}
}

func (g *chatWSEgressGuard) beginTurn() error {
	if g.closed.Load() {
		return errChatWSOutputClosed
	}
	g.writeMu.Lock()
	defer g.writeMu.Unlock()
	if g.closed.Load() {
		return errChatWSOutputClosed
	}
	if g.turnActive {
		// An asynchronous backend may still be producing the previous answer.
		// Queue the accepted client turn, but never reset its active quota until
		// a backend terminal event is successfully written.
		if g.pendingTurns == ^uint64(0) {
			return g.rejectLimitLocked("pending turn count")
		}
		g.pendingTurns++
		return nil
	}
	g.activateTurnLocked()
	return nil
}

func (g *chatWSEgressGuard) activateTurnLocked() {
	// The key is intentionally server-owned and never derived from RunID or
	// TurnID. Wrapping is harmless for equality, but avoiding zero keeps the
	// initial pre-turn bucket distinct for diagnostics and tests.
	g.activeTurnKey++
	if g.activeTurnKey == 0 {
		g.activeTurnKey = 1
	}
	g.turnActive = true
	g.turnEvents = 0
	g.turnBytes = 0
}

func (g *chatWSEgressGuard) emit(event ChatStreamEvent) error {
	return g.emitEvent(event, true)
}

// emitInternal writes handler-owned diagnostics without letting an internal
// turn_error complete or reset an asynchronous backend turn.
func (g *chatWSEgressGuard) emitInternal(event ChatStreamEvent) error {
	return g.emitEvent(event, false)
}

func (g *chatWSEgressGuard) emitEvent(event ChatStreamEvent, backendEvent bool) error {
	// This atomic fast path is intentionally before the mutex: once a rejecting
	// writer has latched the connection closed, other backend goroutines must not
	// wait behind its terminal network write or inspect their untrusted values.
	if g.closed.Load() {
		return errChatWSOutputClosed
	}
	g.writeMu.Lock()
	defer g.writeMu.Unlock()

	if g.closed.Load() {
		return errChatWSOutputClosed
	}
	if g.turnEvents >= g.limits.maxTurnEvents || g.connectionEvents >= g.limits.maxConnectionEvents {
		return g.rejectLimitLocked("event count")
	}
	if g.turnBytes >= g.limits.maxTurnBytes || g.connectionBytes >= g.limits.maxConnectionBytes {
		return g.rejectLimitLocked("byte count")
	}

	remaining := uint64(g.limits.maxFrameBytes)
	if turnRemaining := g.limits.maxTurnBytes - g.turnBytes; turnRemaining < remaining {
		remaining = turnRemaining
	}
	if connectionRemaining := g.limits.maxConnectionBytes - g.connectionBytes; connectionRemaining < remaining {
		remaining = connectionRemaining
	}
	if remaining == 0 {
		return g.rejectLimitLocked("byte count")
	}

	// Preflight arbitrary ToolCall.Query/Body graphs before json.Marshal. This
	// rejects cycles, excessive depth/node counts, custom marshalers, and values
	// whose encoded upper bound cannot fit the remaining frame/turn budget.
	if err := preflightChatStreamEvent(event, remaining, g.limits); err != nil {
		return g.rejectLimitLocked(err.Error())
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return g.rejectLimitLocked("JSON encoding")
	}
	payloadBytes := uint64(len(payload))
	if payloadBytes > uint64(g.limits.maxFrameBytes) ||
		payloadBytes > g.limits.maxTurnBytes-g.turnBytes ||
		payloadBytes > g.limits.maxConnectionBytes-g.connectionBytes {
		return g.rejectLimitLocked("encoded byte count")
	}

	_ = g.writer.SetWriteDeadline(time.Now().Add(chatWSWriteWait))
	if err := g.writer.WriteMessage(websocket.TextMessage, payload); err != nil {
		g.closed.Store(true)
		_ = g.writer.Close()
		return err
	}
	g.turnEvents++
	g.turnBytes += payloadBytes
	g.connectionEvents++
	g.connectionBytes += payloadBytes
	if backendEvent && (event.Type == ChatStreamAssistantDone || event.Type == ChatStreamTurnError || event.Type == ChatStreamTurnPaused) {
		g.completeTurnLocked()
	}
	return nil
}

func (g *chatWSEgressGuard) completeTurnLocked() {
	if !g.turnActive {
		return
	}
	if g.pendingTurns == 0 {
		g.turnActive = false
		return
	}
	g.pendingTurns--
	g.activateTurnLocked()
}

func (g *chatWSEgressGuard) rejectLimitLocked(reason string) error {
	if g.closed.Load() {
		return errChatWSOutputClosed
	}
	// Latch before network I/O so concurrent future Emit calls take the atomic
	// fast path even if the peer has stopped reading.
	g.closed.Store(true)
	if !g.terminalWritten {
		g.terminalWritten = true
		deadline := time.Now().Add(chatWSWriteWait)
		_ = g.writer.SetWriteDeadline(deadline)
		// Never route this through emit: emit owns this mutex and the rejected
		// event may itself be unsafe. The fixed frame is well below every quota.
		_ = g.writer.WriteMessage(websocket.TextMessage, []byte(`{"type":"turn_error","model_ready":true,"error":"AI 输出超过安全限制，连接已关闭。"}`))
		_ = g.writer.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseMessageTooBig, "chat output limit"), deadline)
	}
	_ = g.writer.Close()
	return fmt.Errorf("%w: %s", errChatWSOutputLimit, reason)
}

type chatWSJSONBudget struct {
	remaining int64
}

func newChatWSJSONBudget(limit uint64) (*chatWSJSONBudget, error) {
	if limit == 0 || limit > uint64(^uint(0)>>1) {
		return nil, errChatWSUnsafeJSON
	}
	return &chatWSJSONBudget{remaining: int64(limit)}, nil
}

func (b *chatWSJSONBudget) add(n int64) error {
	if n < 0 || n > b.remaining {
		return errChatWSUnsafeJSON
	}
	b.remaining -= n
	return nil
}

// addString accounts for encoding/json's default string escaping without
// allocating a second escaped copy. It can reject an obviously overlong string
// in O(1), and otherwise scans at most the configured sub-MiB frame budget.
func (b *chatWSJSONBudget) addString(value string) error {
	if int64(len(value)) > b.remaining-2 {
		return errChatWSUnsafeJSON
	}
	if err := b.add(2); err != nil { // surrounding quotes
		return err
	}
	for index := 0; index < len(value); {
		char := value[index]
		if char < utf8.RuneSelf {
			encoded := int64(1)
			switch char {
			case '\\', '"', '\b', '\f', '\n', '\r', '\t':
				encoded = 2
			case '<', '>', '&':
				encoded = 6
			default:
				if char < 0x20 {
					encoded = 6
				}
			}
			if err := b.add(encoded); err != nil {
				return err
			}
			index++
			continue
		}
		r, size := utf8.DecodeRuneInString(value[index:])
		encoded := int64(size)
		if r == utf8.RuneError && size == 1 {
			encoded = 6 // encoding/json replaces invalid UTF-8 with \ufffd
		} else if r == '\u2028' || r == '\u2029' {
			encoded = 6
		}
		if err := b.add(encoded); err != nil {
			return err
		}
		index += size
	}
	return nil
}

func (b *chatWSJSONBudget) addRaw(value json.RawMessage) error {
	// RawMessage is compacted and HTML-escaped by encoding/json. Six bytes per
	// input byte is a safe bound for the worst-case \u00XX expansion.
	length := int64(len(value))
	if length > b.remaining/6 {
		return errChatWSUnsafeJSON
	}
	return b.add(length * 6)
}

type chatWSJSONVisit struct {
	typeName reflect.Type
	pointer  uintptr
}

type chatWSJSONWalker struct {
	budget   *chatWSJSONBudget
	active   map[chatWSJSONVisit]struct{}
	nodes    uint64
	maxNodes uint64
	maxDepth int
}

func (w *chatWSJSONWalker) walk(value reflect.Value, depth int) error {
	if depth > w.maxDepth {
		return errChatWSUnsafeJSON
	}
	w.nodes++
	if w.nodes > w.maxNodes {
		return errChatWSUnsafeJSON
	}
	if !value.IsValid() {
		return w.budget.add(4)
	}

	if value.CanInterface() {
		switch typed := value.Interface().(type) {
		case json.RawMessage:
			return w.budget.addRaw(typed)
		case *json.RawMessage:
			if typed == nil {
				return w.budget.add(4)
			}
			return w.budget.addRaw(*typed)
		case time.Time:
			return w.budget.addString(typed.Format(time.RFC3339Nano))
		case *time.Time:
			if typed == nil {
				return w.budget.add(4)
			}
			return w.budget.addString(typed.Format(time.RFC3339Nano))
		}
	}
	// A value reached through an unexported anonymous field may not be
	// interfaceable even though encoding/json can still discover its methods.
	// Type checks do not require Interface and keep that path fail-closed.
	if value.Type() == chatWSTimeType {
		return w.budget.add(128)
	}
	if value.Type().Implements(chatWSJSONMarshalerType) || value.Type().Implements(chatWSTextMarshalerType) {
		return errChatWSUnsafeJSON
	}
	if value.CanAddr() &&
		(value.Addr().Type().Implements(chatWSJSONMarshalerType) || value.Addr().Type().Implements(chatWSTextMarshalerType)) {
		return errChatWSUnsafeJSON
	}

	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return w.budget.add(4)
		}
		return w.walk(value.Elem(), depth+1)
	case reflect.Pointer:
		if value.IsNil() {
			return w.budget.add(4)
		}
		leave, err := w.enter(value)
		if err != nil {
			return err
		}
		defer leave()
		return w.walk(value.Elem(), depth+1)
	case reflect.Bool:
		return w.budget.add(5)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		return w.budget.add(32)
	case reflect.String:
		return w.budget.addString(value.String())
	case reflect.Slice:
		if value.IsNil() {
			return w.budget.add(4)
		}
		if value.Type().Elem().Kind() == reflect.Uint8 {
			length := int64(value.Len())
			if length > w.budget.remaining {
				return errChatWSUnsafeJSON
			}
			return w.budget.add(4*((length+2)/3) + 2)
		}
		leave, err := w.enter(value)
		if err != nil {
			return err
		}
		defer leave()
		return w.walkSequence(value, depth)
	case reflect.Array:
		return w.walkSequence(value, depth)
	case reflect.Map:
		if value.IsNil() {
			return w.budget.add(4)
		}
		keyType := value.Type().Key()
		keyPointerType := reflect.PointerTo(keyType)
		if keyType.Kind() != reflect.String ||
			keyType.Implements(chatWSJSONMarshalerType) || keyType.Implements(chatWSTextMarshalerType) ||
			keyPointerType.Implements(chatWSJSONMarshalerType) || keyPointerType.Implements(chatWSTextMarshalerType) {
			return errChatWSUnsafeJSON
		}
		leave, err := w.enter(value)
		if err != nil {
			return err
		}
		defer leave()
		if err := w.budget.add(2); err != nil {
			return err
		}
		iterator := value.MapRange()
		for iterator.Next() {
			if err := w.budget.add(2); err != nil { // comma + colon
				return err
			}
			if err := w.budget.addString(iterator.Key().String()); err != nil {
				return err
			}
			if err := w.walk(iterator.Value(), depth+1); err != nil {
				return err
			}
		}
		return nil
	case reflect.Struct:
		if err := w.budget.add(2); err != nil {
			return err
		}
		valueType := value.Type()
		for index := 0; index < value.NumField(); index++ {
			fieldType := valueType.Field(index)
			if fieldType.PkgPath != "" { // unexported
				fieldKind := fieldType.Type.Kind()
				if fieldKind == reflect.Pointer {
					fieldKind = fieldType.Type.Elem().Kind()
				}
				// encoding/json can promote exported children from an unexported
				// anonymous struct, so it must still be traversed.
				if !fieldType.Anonymous || fieldKind != reflect.Struct {
					continue
				}
			}
			tagParts := strings.Split(fieldType.Tag.Get("json"), ",")
			name := tagParts[0]
			if name == "-" {
				continue
			}
			// The ,string option performs a second layer of string escaping.
			// Reject it rather than underestimate an attacker-controlled field.
			for _, option := range tagParts[1:] {
				if option == "string" {
					return errChatWSUnsafeJSON
				}
			}
			if name == "" {
				name = fieldType.Name
			}
			if err := w.budget.add(2); err != nil {
				return err
			}
			// Account for the Go field name as well as a tag name. Invalid tags
			// are ignored by encoding/json; counting both is a bounded upper
			// estimate without duplicating its private tag validator.
			if name != fieldType.Name {
				if err := w.budget.addString(fieldType.Name); err != nil {
					return err
				}
			}
			if err := w.budget.addString(name); err != nil {
				return err
			}
			if err := w.walk(value.Field(index), depth+1); err != nil {
				return err
			}
		}
		return nil
	default:
		return errChatWSUnsafeJSON
	}
}

func (w *chatWSJSONWalker) walkSequence(value reflect.Value, depth int) error {
	if err := w.budget.add(2); err != nil {
		return err
	}
	for index := 0; index < value.Len(); index++ {
		if err := w.budget.add(1); err != nil {
			return err
		}
		if err := w.walk(value.Index(index), depth+1); err != nil {
			return err
		}
	}
	return nil
}

func (w *chatWSJSONWalker) enter(value reflect.Value) (func(), error) {
	var pointer uintptr
	switch value.Kind() {
	case reflect.Map:
		pointer = uintptr(value.UnsafePointer())
	case reflect.Pointer, reflect.Slice:
		pointer = value.Pointer()
	}
	if pointer == 0 {
		return func() {}, nil
	}
	visit := chatWSJSONVisit{typeName: value.Type(), pointer: pointer}
	if _, exists := w.active[visit]; exists {
		return nil, errChatWSUnsafeJSON
	}
	w.active[visit] = struct{}{}
	return func() { delete(w.active, visit) }, nil
}

func preflightChatStreamEvent(event ChatStreamEvent, maxBytes uint64, limits chatWSEgressLimits) error {
	budget, err := newChatWSJSONBudget(maxBytes)
	if err != nil {
		return err
	}
	// Bound iteration even when a backend constructs enormous slices containing
	// empty values whose byte cost alone would otherwise take longer to exhaust.
	if uint64(len(event.InspectIDs)) > limits.maxJSONNodes ||
		uint64(len(event.Plan)) > limits.maxJSONNodes ||
		uint64(len(event.ToolCalls)) > limits.maxJSONNodes {
		return errChatWSUnsafeJSON
	}
	// Field names, punctuation, booleans and numeric fields of the fixed event
	// envelope. Variable-length values are accounted exactly below.
	if err := budget.add(2048); err != nil {
		return err
	}
	for _, value := range []string{
		event.Type, event.RunID, event.TurnID, event.Delta, event.ContentFormat,
		event.Thinking, event.ThinkingID, event.Status, event.Message, event.ToolID,
		event.ConfirmationNonce, event.ToolName, event.ToolTitle, event.ToolStatus,
		event.ToolInput, event.ToolSummary, event.FinishReason, event.Error, event.InspectID,
	} {
		if err := budget.addString(value); err != nil {
			return err
		}
	}
	if err := budget.addRaw(event.ToolResult); err != nil {
		return err
	}
	if err := budget.addRaw(event.ToolDebug); err != nil {
		return err
	}
	for _, value := range event.InspectIDs {
		if err := budget.add(1); err != nil {
			return err
		}
		if err := budget.addString(value); err != nil {
			return err
		}
	}
	for _, item := range event.Plan {
		if err := budget.add(64); err != nil {
			return err
		}
		for _, value := range []string{item.ID, item.Content, item.Status} {
			if err := budget.addString(value); err != nil {
				return err
			}
		}
	}
	if event.Agent != nil {
		if err := budget.add(256); err != nil {
			return err
		}
		for _, value := range []string{
			event.Agent.Mode, event.Agent.Agent, event.Agent.TaskID, event.Agent.ParentTaskID,
			event.Agent.Phase, event.Agent.Status, event.Agent.Summary, event.Agent.ApprovalResume,
		} {
			if err := budget.addString(value); err != nil {
				return err
			}
		}
	}
	if event.Budget != nil {
		if err := budget.add(512); err != nil {
			return err
		}
		for _, value := range []string{event.Budget.ToolBudgetCode, event.Budget.ToolBudgetLimit, event.Budget.ToolBudgetTool} {
			if err := budget.addString(value); err != nil {
				return err
			}
		}
	}
	if event.Usage != nil {
		if err := budget.add(256); err != nil {
			return err
		}
	}

	walker := &chatWSJSONWalker{
		budget:   budget,
		active:   make(map[chatWSJSONVisit]struct{}),
		maxNodes: limits.maxJSONNodes,
		maxDepth: limits.maxJSONDepth,
	}
	if event.ToolCall != nil {
		if err := preflightProposedToolCall(budget, walker, *event.ToolCall); err != nil {
			return err
		}
	}
	for _, call := range event.ToolCalls {
		if err := preflightProposedToolCall(budget, walker, call); err != nil {
			return err
		}
	}
	return nil
}

func preflightProposedToolCall(budget *chatWSJSONBudget, walker *chatWSJSONWalker, call ProposedToolCall) error {
	if call.Presentation != nil {
		for _, value := range []string{call.Presentation.ConfirmText, call.Presentation.CancelText, call.Presentation.ReasonLabel} {
			if err := budget.addString(value); err != nil {
				return err
			}
		}
	}
	if err := budget.add(512); err != nil {
		return err
	}
	for _, value := range []string{
		call.ID, call.Name, call.Title, call.Method, call.Path, call.Risk, call.Reason,
		call.AgentMode, call.ApprovalResume, call.ConfirmationToken, call.ConfirmationNonce,
	} {
		if err := budget.addString(value); err != nil {
			return err
		}
	}
	if call.Query != nil {
		if err := walker.walk(reflect.ValueOf(call.Query), 0); err != nil {
			return err
		}
	}
	if call.Body != nil {
		if err := walker.walk(reflect.ValueOf(call.Body), 0); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handler) handleChatModels(w http.ResponseWriter, r *http.Request, user string) {
	if h.accessFor(r, user).ChatDisabled {
		h.writeError(w, http.StatusForbidden, fmt.Errorf("chat disabled for this user"))
		return
	}
	if h.opts.chat == nil {
		writeJSON(w, http.StatusOK, ChatModels{ProtocolVersion: ChatProtocolVersion, Models: []string{}})
		return
	}
	if lister, ok := h.opts.chat.(ModelLister); ok {
		out, err := lister.Models(r.Context())
		if err != nil {
			h.opts.logger.Warn("ihtml: chat model discovery failed", "user", user, "err", err)
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": "model discovery unavailable"})
			return
		}
		if out.Models == nil {
			out.Models = []string{}
		}
		out.ProtocolVersion = ChatProtocolVersion
		// PreviewOnly is a stricter capability than ModelReady. Normalize a
		// contradictory third-party response so clients never present a
		// deterministic preview as a real model answer.
		if out.PreviewOnly {
			out.ModelReady = false
		}
		// ModelSelect=false 时模型是纯服务端运维配置。隐藏工具栏
		// 不等于隐藏 API，因此响应本身也不得泄露名称/目录。
		if !out.ModelSelect {
			out.Provider = ""
			out.CurrentModel = ""
			out.Models = []string{}
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	writeJSON(w, http.StatusOK, ChatModels{ProtocolVersion: ChatProtocolVersion, ModelReady: true, Models: []string{}})
}

func (h *Handler) sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // 非浏览器客户端
	}
	if h.originAllowed(origin) { // WithCORS 白名单内的宿主域
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	requestScheme := "http"
	if r.TLS != nil {
		requestScheme = "https"
	}
	return u.Scheme == requestScheme && strings.EqualFold(u.Host, r.Host)
}

func (h *Handler) chatConnectionAllowed(r *http.Request, user string) bool {
	if !h.requestUserValid(r, user) {
		return false
	}
	return !h.accessFor(r, user).ChatDisabled
}

func (h *Handler) clientModelSelectionAllowed(ctx context.Context) bool {
	lister, ok := h.opts.chat.(ModelLister)
	if !ok {
		return false
	}
	models, err := lister.Models(ctx)
	return err == nil && models.ModelSelect
}

func (h *Handler) handleChatWS(w http.ResponseWriter, r *http.Request, user string) {
	if h.opts.chat == nil {
		h.writeError(w, http.StatusNotImplemented, errChatDisabled)
		return
	}
	if h.closed.Load() {
		h.writeError(w, http.StatusServiceUnavailable, fmt.Errorf("shutting down"))
		return
	}
	access := h.accessFor(r, user)
	if access.ChatDisabled {
		h.writeError(w, http.StatusForbidden, fmt.Errorf("chat disabled for this user"))
		return
	}
	release := h.acquireConn("ws:"+user, h.opts.limits.MaxWSPerUser)
	if release == nil {
		h.metric("rate_limited", 1)
		h.writeError(w, http.StatusTooManyRequests, fmt.Errorf("too many chat sessions"))
		return
	}
	defer release()

	check := h.opts.checkOrigin
	if check == nil {
		check = h.sameOrigin
	}
	upgrader := websocket.Upgrader{CheckOrigin: check}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	h.wsMu.Lock()
	if h.closed.Load() {
		h.wsMu.Unlock()
		_ = conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseGoingAway, "server shutting down"),
			time.Now().Add(chatWSWriteWait))
		return
	}
	h.wsConns[conn] = struct{}{}
	h.wsMu.Unlock()
	defer func() {
		h.wsMu.Lock()
		delete(h.wsConns, conn)
		h.wsMu.Unlock()
	}()
	h.metric("ws_sessions", 1)
	defer h.metric("ws_sessions", -1)

	ctx := r.Context()
	allowClientModel := h.clientModelSelectionAllowed(ctx)
	var writeMu sync.Mutex
	egress := newChatWSEgressGuard(conn, &writeMu)
	emit := egress.emit
	governed := &governedService{h: h, request: r, user: user, source: "chat", requireChat: true}
	scoped, scopeErr := ScopeService(governed, user)
	if scopeErr != nil {
		h.opts.logger.Warn("ihtml: chat service scope failed", "user", user, "err", scopeErr)
		_ = egress.emitInternal(ChatStreamEvent{Type: "turn_error", Error: "AI 会话初始化失败", ModelReady: true})
		return
	}
	sessionContext := &ChatSessionContext{
		User:          user,
		Service:       governed,
		ScopedService: scoped,
		Emit:          emit,
		Request:       r,
		APIs:          h.opts.apis,
		Access:        access,
	}
	session, err := h.opts.chat.NewSession(ctx, sessionContext)
	if err != nil || nilcheck.IsNil(session) {
		if err == nil {
			err = errors.New("chat backend returned a nil session")
		}
		h.opts.logger.Warn("ihtml: chat session init failed", "user", user, "err", err)
		_ = egress.emitInternal(ChatStreamEvent{Type: "turn_error", Error: "AI 会话初始化失败", ModelReady: true})
		return
	}
	defer session.Close()

	conn.SetReadLimit(1 << 20)
	_ = conn.SetReadDeadline(time.Now().Add(chatWSPongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(chatWSPongWait))
	})
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(chatWSPingEvery)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				// 长连接不能永久保留建连时的权限。认证会话
				// 失效或 ChatDisabled 撤销后，最迟在下一次 ping 关闭。
				if !h.chatConnectionAllowed(r, user) {
					writeMu.Lock()
					_ = conn.SetWriteDeadline(time.Now().Add(chatWSWriteWait))
					_ = conn.WriteControl(websocket.CloseMessage,
						websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "access revoked"),
						time.Now().Add(chatWSWriteWait))
					writeMu.Unlock()
					_ = conn.Close()
					return
				}
				writeMu.Lock()
				_ = conn.SetWriteDeadline(time.Now().Add(chatWSWriteWait))
				err := conn.WriteMessage(websocket.PingMessage, nil)
				writeMu.Unlock()
				if err != nil {
					_ = conn.Close()
					return
				}
			}
		}
	}()

	for {
		ev := &ChatClientEvent{}
		if err := readChatClientEvent(conn, ev); err != nil {
			return
		}
		if !h.chatConnectionAllowed(r, user) {
			_ = egress.emitInternal(ChatStreamEvent{Type: "turn_error", Error: "当前会话权限已撤销。", ModelReady: true})
			return
		}
		// 服务端未显式开放模型选择时，忽略客户端伪造的
		// model 字段，后端继续使用运维配置的默认模型。
		if !allowClientModel {
			ev.Model = ""
		}
		// 对话轮次限流: 模型调用是真金白银, 频率闸门在库层守住
		if ev.Type == "user_message" {
			if !h.allowRate("chat:"+user, h.opts.limits.ChatTurnsPerMinute) {
				h.metric("rate_limited", 1)
				_ = egress.emitInternal(ChatStreamEvent{Type: "turn_error", Error: "对话过于频繁, 请稍后再试。", ModelReady: true})
				continue
			}
			// Only this accepted client event advances the server-owned turn
			// bucket. A backend cannot evade quotas with missing or forged IDs.
			if err := egress.beginTurn(); err != nil {
				return
			}
			h.metric("chat_turns", 1)
		}
		session.HandleEvent(ctx, ev)
	}
}

func readChatClientEvent(conn *websocket.Conn, ev *ChatClientEvent) error {
	kind, reader, err := conn.NextReader()
	if err != nil {
		return err
	}
	if kind != websocket.TextMessage {
		return fmt.Errorf("chat events must be text JSON messages")
	}
	dec := json.NewDecoder(reader)
	if err := dec.Decode(ev); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("chat message must contain exactly one JSON value")
		}
		return fmt.Errorf("invalid trailing chat data: %w", err)
	}
	return nil
}

var errChatDisabled = errors.New("chat backend not configured")
