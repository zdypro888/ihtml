package ihtml

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// 对话走 WebSocket, 线格式与 casino admin AI 助手保持一致,
// 便于既有的 eino/ADK 后端与前端经验直接平移。

const (
	// ChatProtocolVersion identifies the additive WebSocket/capabilities contract.
	ChatProtocolVersion = 1

	ChatClientUserMessage = "user_message"
	ChatClientConfirmTool = "confirm_tool"
	ChatClientCancel      = "cancel"
	// ChatClientInspectResult 是内核对 inspect_request 的应答帧,
	// 携带 inspect_id 与实时页面渲染状态(不可信数据)。
	ChatClientInspectResult = "inspect_result"

	ChatStreamTurnStart            = "turn_start"
	ChatStreamAssistantStatus      = "assistant_status"
	ChatStreamAssistantThinking    = "assistant_thinking"
	ChatStreamAssistantDelta       = "assistant_delta"
	ChatStreamAssistantPlan        = "assistant_plan"
	ChatStreamAssistantAgent       = "assistant_agent"
	ChatStreamAssistantBudget      = "assistant_budget"
	ChatStreamToolStart            = "tool_start"
	ChatStreamConfirmationRequired = "confirmation_required"
	ChatStreamToolResult           = "tool_result"
	ChatStreamAssistantDone        = "assistant_done"
	ChatStreamTurnError            = "turn_error"
	// ChatStreamInspectRequest 由服务端(ui_page_inspect 工具)发起, 请求内核
	// 回传当前页面的实时渲染状态; 内核以 inspect_result 帧应答同一 inspect_id。
	ChatStreamInspectRequest = "inspect_request"

	ChatToolPending   = "pending"
	ChatToolRunning   = "running"
	ChatToolSuccess   = "success"
	ChatToolError     = "error"
	ChatToolCancelled = "cancelled"

	ChatRiskLow     = "low"
	ChatRiskMedium  = "medium"
	ChatRiskHigh    = "high"
	ChatRiskBlocked = "blocked"
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
	Path        string `json:"path,omitempty"`
	Title       string `json:"title,omitempty"`
	VisibleText string `json:"visible_text,omitempty"`
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
	Items       []ChatClientInspectionItem `json:"items,omitempty"`
	Errors      []string                   `json:"errors,omitempty"`       // 最近页面错误摘要
	VisibleText string                     `json:"visible_text,omitempty"` // 全页可见文本(截断)
}

// ChatClientEvent 是客户端发来的 WS 事件:
// type = user_message | confirm_tool | cancel | inspect_result。
type ChatClientEvent struct {
	Type     string        `json:"type"`
	Messages []ChatMessage `json:"messages,omitempty"`
	Model    string        `json:"model,omitempty"`
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
	// ApprovalResume 明示确认后的恢复能力。当前 deep 使用 manual_tool_only：
	// 只执行确认卡，不会静默恢复或宣称整个计划完成。
	ApprovalResume string `json:"approval_resume,omitempty"`
}

// ChatTurnBudget 是一轮共享预算的快照；Deep 根 Agent 与所有子 Agent 共用。
type ChatTurnBudget struct {
	ModelCalls     int64 `json:"model_calls"`
	MaxModelCalls  int64 `json:"max_model_calls,omitempty"`
	Subtasks       int64 `json:"subtasks"`
	MaxSubtasks    int64 `json:"max_subtasks,omitempty"`
	DeadlineUnixMS int64 `json:"deadline_unix_ms,omitempty"`
}

// ChatStreamEvent 是服务端推给前端的流式事件, type 取值:
// turn_start / assistant_status / assistant_thinking / assistant_delta /
// assistant_plan / assistant_agent / assistant_budget / tool_start /
// confirmation_required / tool_result / assistant_done / turn_error。
type ChatStreamEvent struct {
	Type     string `json:"type"`
	RunID    string `json:"run_id,omitempty"`
	TurnID   string `json:"turn_id,omitempty"`
	Seq      int64  `json:"seq,omitempty"`
	Delta    string `json:"delta,omitempty"`
	Thinking string `json:"thinking,omitempty"`
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
	ToolResult   json.RawMessage    `json:"tool_result,omitempty"`
	ToolName     string             `json:"tool_name,omitempty"`
	ToolTitle    string             `json:"tool_title,omitempty"`
	ToolStatus   string             `json:"tool_status,omitempty"`
	ToolInput    string             `json:"tool_input,omitempty"`
	ToolSummary  string             `json:"tool_summary,omitempty"`
	ToolDebug    json.RawMessage    `json:"tool_debug,omitempty"`
	Plan         []ChatPlanItem     `json:"plan,omitempty"`
	Agent        *ChatAgentStage    `json:"agent,omitempty"`
	Budget       *ChatTurnBudget    `json:"budget,omitempty"`
	Error        string             `json:"error,omitempty"`
	// InspectID 仅用于 inspect_request: 内核应答 inspect_result 时原样带回。
	InspectID string `json:"inspect_id,omitempty"`
}

// ProposedToolCall 是一次工具调用的描述; 需人工确认的调用带 HMAC 签名的
// confirmation_token, 前端确认后原样带回执行。
type ProposedToolCall struct {
	ID                   string         `json:"id,omitempty"`
	Name                 string         `json:"name,omitempty"`
	Title                string         `json:"title,omitempty"`
	Method               string         `json:"method"`
	Path                 string         `json:"path"`
	Query                map[string]any `json:"query,omitempty"`
	Body                 any            `json:"body,omitempty"`
	Risk                 string         `json:"risk"` // low / medium / high / blocked
	RequiresConfirmation bool           `json:"requires_confirmation"`
	Reason               string         `json:"reason"`
	AgentMode            string         `json:"agent_mode,omitempty"`
	ApprovalResume       string         `json:"approval_resume,omitempty"`
	ConfirmationToken    string         `json:"confirmation_token,omitempty"`
	ConfirmationNonce    string         `json:"confirmation_nonce,omitempty"`
	ConfirmationExpires  int64          `json:"confirmation_expires,omitempty"`
}

// ChatModels 是 GET /api/chat/models 的响应。
type ChatModels struct {
	ProtocolVersion int      `json:"protocol_version"`
	Provider        string   `json:"provider,omitempty"`
	ModelReady      bool     `json:"model_ready"`
	CurrentModel    string   `json:"current_model"`
	Models          []string `json:"models"`
	AgentModes      []string `json:"agent_modes,omitempty"`
	// DeepApprovalResume 当前为 manual_tool_only；未来安全接入 ADK checkpoint
	// 后可升级为 checkpoint，而无需客户端猜测。
	DeepApprovalResume string `json:"deep_approval_resume,omitempty"`
	RequestTimeoutMS   int64  `json:"request_timeout_ms"`
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
)

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
	emit := func(ev ChatStreamEvent) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(chatWSWriteWait))
		return conn.WriteJSON(ev)
	}
	governed := &governedService{h: h, request: r, user: user, source: "chat", requireChat: true}
	scoped, scopeErr := ScopeService(governed, user)
	if scopeErr != nil {
		h.opts.logger.Warn("ihtml: chat service scope failed", "user", user, "err", scopeErr)
		_ = emit(ChatStreamEvent{Type: "turn_error", Error: "AI 会话初始化失败", ModelReady: true})
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
	if err != nil {
		h.opts.logger.Warn("ihtml: chat session init failed", "user", user, "err", err)
		_ = emit(ChatStreamEvent{Type: "turn_error", Error: "AI 会话初始化失败", ModelReady: true})
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
			_ = emit(ChatStreamEvent{Type: "turn_error", Error: "当前会话权限已撤销。", ModelReady: true})
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
				_ = emit(ChatStreamEvent{Type: "turn_error", Error: "对话过于频繁, 请稍后再试。", ModelReady: true})
				continue
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
