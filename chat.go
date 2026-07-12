package ihtml

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// 对话走 WebSocket, 线格式与 casino admin AI 助手保持一致,
// 便于既有的 eino/ADK 后端与前端经验直接平移。

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

// ChatClientEvent 是客户端发来的 WS 事件:
// type = user_message | confirm_tool | cancel。
type ChatClientEvent struct {
	Type          string            `json:"type"`
	Messages      []ChatMessage     `json:"messages,omitempty"`
	Model         string            `json:"model,omitempty"`
	ToolsEnabled  *bool             `json:"tools_enabled,omitempty"`
	ClientContext ChatClientContext `json:"client_context"`
	ToolCall      *ProposedToolCall `json:"tool_call,omitempty"`
}

// ChatStreamEvent 是服务端推给前端的流式事件, type 取值:
// turn_start / assistant_status / assistant_thinking / assistant_delta /
// tool_start / confirmation_required / tool_result / assistant_done / turn_error。
type ChatStreamEvent struct {
	Type        string             `json:"type"`
	RunID       string             `json:"run_id,omitempty"`
	TurnID      string             `json:"turn_id,omitempty"`
	Seq         int64              `json:"seq,omitempty"`
	Delta       string             `json:"delta,omitempty"`
	Thinking    string             `json:"thinking,omitempty"`
	Status      string             `json:"status,omitempty"`
	Message     string             `json:"message,omitempty"`
	ModelReady  bool               `json:"model_ready,omitempty"`
	ToolCalls   []ProposedToolCall `json:"tool_calls,omitempty"`
	ToolCall    *ProposedToolCall  `json:"tool_call,omitempty"`
	ToolResult  json.RawMessage    `json:"tool_result,omitempty"`
	ToolName    string             `json:"tool_name,omitempty"`
	ToolTitle   string             `json:"tool_title,omitempty"`
	ToolStatus  string             `json:"tool_status,omitempty"`
	ToolInput   string             `json:"tool_input,omitempty"`
	ToolSummary string             `json:"tool_summary,omitempty"`
	ToolDebug   json.RawMessage    `json:"tool_debug,omitempty"`
	Error       string             `json:"error,omitempty"`
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
	ConfirmationToken    string         `json:"confirmation_token,omitempty"`
	ConfirmationNonce    string         `json:"confirmation_nonce,omitempty"`
	ConfirmationExpires  int64          `json:"confirmation_expires,omitempty"`
}

// ChatModels 是 GET /api/chat/models 的响应。
type ChatModels struct {
	ModelReady       bool     `json:"model_ready"`
	CurrentModel     string   `json:"current_model"`
	Models           []string `json:"models"`
	RequestTimeoutMS int64    `json:"request_timeout_ms"`
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
	// Service 用于读改该用户的 UI 数据(Item/页面/主题/KV/快照/页面错误)。
	Service Service
	// Emit 向前端推流式事件, 并发安全; 连接断开后返回错误。
	Emit func(ChatStreamEvent) error
	// Request 是发起 WS 的原始请求, 供后端取额外参数/推导同源地址。
	Request *http.Request
	// APIs 是 WithAPIs 声明的业务 API 目录。
	APIs []APISpec
	// Access 是该用户的访问策略(只读时后端应拒绝写工具)。
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

func (h *Handler) handleChatModels(w http.ResponseWriter, r *http.Request, _ string) {
	if h.opts.chat == nil {
		writeJSON(w, http.StatusOK, ChatModels{Models: []string{}})
		return
	}
	if lister, ok := h.opts.chat.(ModelLister); ok {
		out, err := lister.Models(r.Context())
		if err != nil {
			h.writeError(w, http.StatusBadGateway, err)
			return
		}
		if out.Models == nil {
			out.Models = []string{}
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	writeJSON(w, http.StatusOK, ChatModels{ModelReady: true, Models: []string{}})
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
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
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
	var writeMu sync.Mutex
	emit := func(ev ChatStreamEvent) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(chatWSWriteWait))
		return conn.WriteJSON(ev)
	}
	session, err := h.opts.chat.NewSession(ctx, &ChatSessionContext{
		User:    user,
		Service: h,
		Emit:    emit,
		Request: r,
		APIs:    h.opts.apis,
		Access:  access,
	})
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
				writeMu.Lock()
				_ = conn.SetWriteDeadline(time.Now().Add(chatWSWriteWait))
				err := conn.WriteMessage(websocket.PingMessage, nil)
				writeMu.Unlock()
				if err != nil {
					return
				}
			}
		}
	}()

	for {
		ev := &ChatClientEvent{}
		if err := conn.ReadJSON(ev); err != nil {
			return
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

var errChatDisabled = errors.New("chat backend not configured")
