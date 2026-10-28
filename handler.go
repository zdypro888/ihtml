package ihtml

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zdypro888/ihtml/internal/nilcheck"
)

// Only production assets belong in downstream binaries. Tests and design
// artifacts must not be pulled in by a directory-wide embed pattern.
//
//go:embed web/index.html web/ihtml-lib.js web/ihtml.js web/ihtml.css web/ihtml-push-sw.js
var webFS embed.FS

// Handler 是库的 HTTP 入口: 服务空壳页面、内核资源与全部 /api/* 接口。
// 可挂在根路径, 也可经 http.StripPrefix 挂在任意子路径(内核用相对路径请求)。
type Handler struct {
	store Store
	opts  options
	mux   *http.ServeMux

	lockShards [64]sync.Mutex // 按用户分桶的读-改-写锁(固定容量, 无泄漏)

	page     []byte // 渲染好的 index.html
	pageETag string

	statsMu sync.Mutex
	stats   map[string]int64

	ratesMu sync.Mutex
	rates   map[string]*rateWindow

	connsMu sync.Mutex
	conns   map[string]int // "sse:<user>" / "ws:<user>" → 当前连接数

	wsMu    sync.Mutex
	wsConns map[io.Closer]struct{} // 活跃对话连接, 供 Close 优雅断开

	closeCh chan struct{} // 同时终止内置/自定义 Broadcaster 上的 SSE
	closed  atomic.Bool
}

type rateWindow struct {
	start int64 // 分钟窗口起点(unix 分钟)
	n     int
}

// ErrInvalidHandlerConfig matches constructor failures reported by NewHandler.
var ErrInvalidHandlerConfig = errors.New("ihtml: invalid handler configuration")

// HandlerConfigError identifies the option or dependency that failed startup
// validation. Callers should use errors.As instead of matching Error strings.
type HandlerConfigError struct {
	Field   string
	Problem string
}

func (e *HandlerConfigError) Error() string {
	if e == nil {
		return ErrInvalidHandlerConfig.Error()
	}
	return fmt.Sprintf("%s: %s: %s", ErrInvalidHandlerConfig, e.Field, e.Problem)
}

func (e *HandlerConfigError) Unwrap() error { return ErrInvalidHandlerConfig }

// New creates a Handler and preserves the original panic-on-programmer-error
// behavior for source compatibility. NewHandler is recommended for SDK users
// that want startup errors returned to their application.
func New(store Store, opts ...Option) *Handler {
	h, err := NewHandler(store, opts...)
	if err != nil {
		panic(err)
	}
	return h
}

// NewHandler validates dependencies and options, then creates a Handler.
func NewHandler(store Store, opts ...Option) (*Handler, error) {
	if nilcheck.IsNil(store) {
		return nil, &HandlerConfigError{Field: "Store", Problem: "is required"}
	}
	o := options{
		resolver:     DefaultUserResolver,
		maxRevisions: DefaultMaxRevisions,
		logger:       slog.Default(),
		pageTitle:    "ihtml",
		limits:       Limits{}.normalized(),
	}
	for index, fn := range opts {
		if fn == nil {
			return nil, &HandlerConfigError{Field: fmt.Sprintf("Option[%d]", index), Problem: "is nil"}
		}
		fn(&o)
	}
	if o.logger == nil {
		return nil, &HandlerConfigError{Field: "Logger", Problem: "is nil"}
	}
	if o.maxRevisions < 0 {
		return nil, &HandlerConfigError{Field: "MaxRevisions", Problem: "cannot be negative"}
	}
	for _, dependency := range []struct {
		field string
		value any
	}{
		{field: "ChatBackend", value: o.chat},
		{field: "Metrics", value: o.metrics},
		{field: "Broadcaster", value: o.broadcaster},
	} {
		// A plain nil means the optional capability was not configured. A typed
		// nil is non-nil at the interface level and must be reported explicitly.
		if dependency.value != nil && nilcheck.IsNil(dependency.value) {
			return nil, &HandlerConfigError{Field: dependency.field, Problem: "is a typed nil"}
		}
	}
	if o.broadcaster == nil {
		o.broadcaster = newHub()
	}
	h := &Handler{
		store:   store,
		opts:    o,
		stats:   make(map[string]int64),
		rates:   make(map[string]*rateWindow),
		conns:   make(map[string]int),
		wsConns: make(map[io.Closer]struct{}),
		closeCh: make(chan struct{}),
	}
	raw, err := webFS.ReadFile("web/index.html")
	if err != nil {
		return nil, fmt.Errorf("%w: embedded index.html: %v", ErrInvalidHandlerConfig, err)
	}
	h.page = bytes.Replace(raw, []byte("{{TITLE}}"), []byte(htmlEscape(o.pageTitle)), 1)
	h.pageETag = etagOf(h.page)
	h.routes()
	return h, nil
}

func etagOf(b []byte) string {
	sum := sha256.Sum256(b)
	return `"` + hex.EncodeToString(sum[:8]) + `"`
}

// metric 更新内置计数并转发给宿主 Metrics(若注册)。
func (h *Handler) metric(name string, delta int64) {
	h.statsMu.Lock()
	h.stats[name] += delta
	h.statsMu.Unlock()
	if h.opts.metrics != nil {
		h.opts.metrics.Add(name, float64(delta))
	}
}

// Stats 返回内置计数快照(sse_connections/ws_sessions/events_published/
// chat_turns/writes/rate_limited 等), 便于零依赖接入 expvar。
func (h *Handler) Stats() map[string]int64 {
	h.statsMu.Lock()
	defer h.statsMu.Unlock()
	out := make(map[string]int64, len(h.stats))
	for k, v := range h.stats {
		out[k] = v
	}
	return out
}

// allowRate 固定分钟窗口限频。perMinute<=0 表示不限。
func (h *Handler) allowRate(key string, perMinute int) bool {
	if perMinute <= 0 {
		return true
	}
	now := time.Now().Unix() / 60
	h.ratesMu.Lock()
	defer h.ratesMu.Unlock()
	w := h.rates[key]
	if w == nil || w.start != now {
		// 顺带控制表大小: 窗口切换时清理陈旧条目
		if len(h.rates) > 4096 {
			for k, v := range h.rates {
				if v.start != now {
					delete(h.rates, k)
				}
			}
		}
		h.rates[key] = &rateWindow{start: now, n: 1}
		return true
	}
	if w.n >= perMinute {
		return false
	}
	w.n++
	return true
}

// acquireConn 连接数限额; 成功时返回释放函数, 超限返回 nil。
func (h *Handler) acquireConn(key string, limit int) func() {
	h.connsMu.Lock()
	defer h.connsMu.Unlock()
	if limit > 0 && h.conns[key] >= limit {
		return nil
	}
	h.conns[key]++
	var once sync.Once
	return func() {
		once.Do(func() {
			h.connsMu.Lock()
			if h.conns[key]--; h.conns[key] <= 0 {
				delete(h.conns, key)
			}
			h.connsMu.Unlock()
		})
	}
}

func (h *Handler) accessDecision(r *http.Request, user string) (Access, error) {
	if h.opts.access == nil {
		return Access{}, nil
	}
	return h.opts.access(r, user)
}

// accessFor is used after withUser has already rejected policy backend errors.
// Long-lived paths also fail closed by treating a new error as no write/chat.
func (h *Handler) accessFor(r *http.Request, user string) Access {
	access, err := h.accessDecision(r, user)
	if err != nil {
		return Access{ReadOnly: true, ChatDisabled: true}
	}
	return access
}

// Close 优雅停机: 拒绝新连接、断开全部 SSE/WS。应在 http.Server.Shutdown
// 前调用(WS 是被劫持的连接, Server.Shutdown 不会等待它们)。
func (h *Handler) Close() error {
	if h.closed.Swap(true) {
		return nil
	}
	close(h.closeCh)
	if hb, ok := h.opts.broadcaster.(*hub); ok {
		hb.close()
	}
	h.wsMu.Lock()
	for c := range h.wsConns {
		_ = c.Close()
	}
	h.wsConns = map[io.Closer]struct{}{}
	h.wsMu.Unlock()
	return nil
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

// UIAssets serves only the trusted SDK script and styles. Hosts embedding
// mountChat can reuse the UI without installing a workspace or its write APIs.
// Mount behind the host's own authentication if desired.
func UIAssets() http.Handler {
	h := &Handler{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ihtml.js", h.handleBundle("text/javascript; charset=utf-8", "web/ihtml-lib.js", "web/ihtml.js"))
	mux.HandleFunc("GET /ihtml.css", h.handleAsset("web/ihtml.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET /ihtml-push-sw.js", h.handleAsset("web/ihtml-push-sw.js", "text/javascript; charset=utf-8"))
	return mux
}

func (h *Handler) routes() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", h.handlePage)
	// /ihtml.js = 标准库 + 内核 拼接下发(宿主只需引入一个脚本)
	assets := UIAssets()
	mux.Handle("GET /ihtml.js", assets)
	mux.Handle("GET /ihtml.css", assets)
	mux.Handle("GET /ihtml-push-sw.js", assets)

	mux.HandleFunc("GET /api/config", h.withUser(h.handleConfig))
	mux.HandleFunc("GET /api/items", h.withUser(h.handleListItems))
	mux.HandleFunc("PUT /api/items", h.withJSONWrite(h.handlePutItems))
	mux.HandleFunc("POST /api/items", h.withJSONWrite(h.handlePutItems)) // 等价 PUT
	mux.HandleFunc("DELETE /api/items", h.withWrite(h.handleDeleteItems))
	mux.HandleFunc("POST /api/items/replace", h.withJSONWrite(h.handleReplaceItems))
	mux.HandleFunc("GET /api/pages", h.withUser(h.handleListPages))
	mux.HandleFunc("PUT /api/pages", h.withJSONWrite(h.handleSetPage))
	mux.HandleFunc("PUT /api/pages/{name}/publish", h.withJSONWrite(h.handlePublishPage))
	mux.HandleFunc("DELETE /api/pages/{name}", h.withWrite(h.handleDeletePage))
	h.registerTemplateRoutes(mux)
	mux.HandleFunc("GET /api/revisions", h.withUser(h.handleListRevisions))
	mux.HandleFunc("GET /api/revisions/{id}", h.withUser(h.handleRevisionItems))
	mux.HandleFunc("POST /api/revisions/{id}/rollback", h.withWrite(h.handleRollback))
	mux.HandleFunc("GET /api/kv/{key}", h.withUser(h.handleKVGet))
	mux.HandleFunc("PUT /api/kv/{key}", h.withJSONWrite(h.handleKVSet))
	mux.HandleFunc("DELETE /api/kv/{key}", h.withWrite(h.handleKVDelete))
	mux.HandleFunc("GET /api/export", h.withUser(h.handleExport))
	mux.HandleFunc("GET /api/events", h.withUser(h.handleEvents))
	mux.HandleFunc("GET /api/errors", h.withUser(h.handleListErrors))
	mux.HandleFunc("POST /api/errors", h.withJSONUser(h.handleReportErrors))
	mux.HandleFunc("GET /api/chat/models", h.withUser(h.handleChatModels))
	mux.HandleFunc("GET /api/chat/ws", h.withUser(h.handleChatWS))
	h.mux = mux
}

// withWrite = withUser + 只读策略拦截 + 写频率限制。
func (h *Handler) withWrite(fn func(w http.ResponseWriter, r *http.Request, user string)) http.HandlerFunc {
	return h.withUser(func(w http.ResponseWriter, r *http.Request, user string) {
		if h.accessFor(r, user).ReadOnly {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "read-only access"})
			return
		}
		if !h.allowRate("w:"+user, h.opts.limits.WritesPerMinute) {
			h.metric("rate_limited", 1)
			writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "写操作过于频繁, 请稍后再试"})
			return
		}
		fn(w, r.WithContext(withMutationSource(r.Context(), "api")), user)
	})
}

// withJSONWrite 为消费 JSON 的写端点增加媒体类型约束。
// application/json 不是 CORS safelisted Content-Type，因此这也会
// 阻断把 JSON 伪装成 text/plain 的简单请求 CSRF。
func (h *Handler) withJSONWrite(fn func(w http.ResponseWriter, r *http.Request, user string)) http.HandlerFunc {
	return h.requireJSON(h.withWrite(fn))
}

func (h *Handler) withJSONUser(fn func(w http.ResponseWriter, r *http.Request, user string)) http.HandlerFunc {
	return h.requireJSON(h.withUser(fn))
}

func (h *Handler) requireJSON(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		structuredJSON := strings.HasPrefix(mediaType, "application/") && strings.HasSuffix(mediaType, "+json")
		if err != nil || mediaType != "application/json" && !structuredJSON {
			writeJSON(w, http.StatusUnsupportedMediaType, map[string]any{"error": "Content-Type must be application/json"})
			return
		}
		next(w, r)
	}
}

// originAllowed 判断 Origin 是否在 WithCORS 白名单内。
func (h *Handler) originAllowed(origin string) bool {
	for _, allowed := range h.opts.cors {
		if allowed == "*" || strings.EqualFold(allowed, origin) {
			return true
		}
	}
	return false
}

// ServeHTTP 实现 http.Handler。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	corsAllowed := origin != "" && h.originAllowed(origin)
	// 浏览器的非安全请求只允许同源或 WithCORS 显式白名单。
	// 对没有 Origin 的非浏览器客户端保持兼容；Sec-Fetch-Site
	// 可以覆盖某些浏览器省略 Origin 的边界情况。
	if isUnsafeMethod(r.Method) {
		if origin != "" && !h.sameOrigin(r) && !corsAllowed {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "cross-origin request rejected"})
			return
		}
		if origin == "" && strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site") {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "cross-site request rejected"})
			return
		}
	}

	// CORS: 宿主页面跨域 mount 内核时显式放行。
	if corsAllowed {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Ihtml-User")
			w.Header().Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
	} else if r.Method == http.MethodOptions && origin != "" {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "CORS origin rejected"})
		return
	}
	// 经 StripPrefix("/ui", h) 挂载时, 访问 /ui 会得到空 path;
	// 内核依赖相对路径, 必须重定向到带斜杠的 /ui/。
	if r.URL.Path == "" {
		target := r.RequestURI
		if i := strings.IndexByte(target, '?'); i >= 0 {
			target = target[:i] + "/" + target[i:]
		} else {
			target += "/"
		}
		http.Redirect(w, r, target, http.StatusPermanentRedirect)
		return
	}
	h.mux.ServeHTTP(w, r)
}

func isUnsafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return false
	default:
		return true
	}
}

func (h *Handler) handlePage(w http.ResponseWriter, r *http.Request) {
	// 默认禁止跨域 iframe 嵌入；宿主显式配置 postMessage
	// Origin 时，同一白名单也是唯一可嵌入页面的 frame ancestor。
	frameAncestors := "'self'"
	if len(h.opts.messageOrigins) > 0 {
		if containsString(h.opts.messageOrigins, "*") {
			frameAncestors = "*"
		} else {
			frameAncestors += " " + strings.Join(h.opts.messageOrigins, " ")
		}
	} else {
		// X-Frame-Options 无法表达多个跨域白名单，因此只在
		// 默认同源策略下作为旧浏览器兜底。
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
	}
	w.Header().Set("Content-Security-Policy", "frame-ancestors "+frameAncestors)
	if match := r.Header.Get("If-None-Match"); match != "" && match == h.pageETag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", h.pageETag)
	_, _ = w.Write(h.page)
}

// handleBundle 把多个内嵌资源拼接成单个响应(带 ETag)。
func (h *Handler) handleBundle(contentType string, paths ...string) http.HandlerFunc {
	var buf bytes.Buffer
	for _, p := range paths {
		raw, err := webFS.ReadFile(p)
		if err != nil {
			panic(fmt.Sprintf("ihtml: embedded asset %s missing: %v", p, err))
		}
		buf.Write(raw)
		buf.WriteString("\n;\n")
	}
	body := buf.Bytes()
	etag := etagOf(body)
	return func(w http.ResponseWriter, r *http.Request) {
		if match := r.Header.Get("If-None-Match"); match != "" && match == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("ETag", etag)
		_, _ = w.Write(body)
	}
}

// handleAsset 返回内核静态资源。保持 no-cache 语义(内核是"运行时",
// 旧内核继续跑会与 AI 生成代码脱节), 但支持 ETag 协商缓存: 浏览器
// 每次校验、未变更时 304 零传输。
func (h *Handler) handleAsset(path, contentType string) http.HandlerFunc {
	raw, err := webFS.ReadFile(path)
	if err != nil {
		panic(fmt.Sprintf("ihtml: embedded asset %s missing: %v", path, err))
	}
	etag := etagOf(raw)
	return func(w http.ResponseWriter, r *http.Request) {
		if match := r.Header.Get("If-None-Match"); match != "" && match == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("ETag", etag)
		_, _ = w.Write(raw)
	}
}

// withUser 解析用户并注入处理函数, 失败回 401。
func (h *Handler) withUser(fn func(w http.ResponseWriter, r *http.Request, user string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, err := h.opts.resolver(r)
		if err != nil || strings.TrimSpace(user) == "" {
			h.metric("auth_rejected", 1)
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		if _, err := h.accessDecision(r, strings.TrimSpace(user)); err != nil {
			h.metric("access_policy_errors", 1)
			h.opts.logger.Error("ihtml: access policy failed", "user", strings.TrimSpace(user), "err", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "access policy unavailable"})
			return
		}
		fn(w, r, strings.TrimSpace(user))
	}
}

// requestUserValid 用于长连接定期重验宿主会话，并确保会话
// 中途切换账号时不会延续旧用户的订阅/对话权限。
func (h *Handler) requestUserValid(r *http.Request, user string) bool {
	resolved, err := h.opts.resolver(r)
	return err == nil && strings.TrimSpace(resolved) == user
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *Handler) writeError(w http.ResponseWriter, status int, err error) {
	if errors.Is(err, ErrNotFound) {
		status = http.StatusNotFound
	}
	writeJSON(w, status, map[string]any{"error": err.Error()})
}

func readJSON(w http.ResponseWriter, r *http.Request, limit int64, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		return err
	}
	// Decoder.Decode 本身会在第一个 JSON 值后停止。必须再读一次，
	// 否则 {"ok":true}{"smuggled":true} 这类 trailing data 会被静默忽略。
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("request body must contain exactly one JSON value")
		}
		return fmt.Errorf("invalid trailing data: %w", err)
	}
	return nil
}

func (h *Handler) handleConfig(w http.ResponseWriter, r *http.Request, user string) {
	access := h.accessFor(r, user)
	out := map[string]any{
		"user":      user,
		"chat":      h.opts.chat != nil && !access.ChatDisabled,
		"read_only": access.ReadOnly,
		"templates": true,
	}
	if len(h.opts.messageOrigins) > 0 {
		out["message_origins"] = append([]string(nil), h.opts.messageOrigins...)
	}
	if h.opts.pageConfig != nil {
		if params := h.opts.pageConfig(r, user); params != nil {
			out["params"] = params
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) handleExport(w http.ResponseWriter, r *http.Request, user string) {
	export, err := h.ExportUser(r.Context(), user)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+exportFilename(user)+`"`)
	writeJSON(w, http.StatusOK, export)
}

// exportFilename 只保留 Content-Disposition quoted-string 内安全的
// ASCII 字符，防止 resolver 返回的用户名注入 CRLF/额外响应头。
func exportFilename(user string) string {
	var b strings.Builder
	b.WriteString("ihtml-")
	for _, r := range user {
		if b.Len() >= 72 {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == len("ihtml-") {
		b.WriteString("export")
	}
	b.WriteString(".json")
	return b.String()
}

// handleListItems 支持页面过滤:
//
//	GET /api/items                → 全部(AI/管理视角)
//	GET /api/items?page=bills     → 全局 + bills 页(内核启动)
//	GET /api/items?page=bills&global=0 → 仅 bills 页(内核切页)
func (h *Handler) handleListItems(w http.ResponseWriter, r *http.Request, user string) {
	items, err := h.ListItems(r.Context(), user)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	q := r.URL.Query()
	if q.Has("page") {
		page := q.Get("page")
		includeGlobal := q.Get("global") != "0"
		filtered := make([]Item, 0, len(items))
		for _, it := range items {
			if it.Page == page || (includeGlobal && it.Page == "") {
				filtered = append(filtered, it)
			}
		}
		items = filtered
	}
	if items == nil {
		items = []Item{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) handleListPages(w http.ResponseWriter, r *http.Request, user string) {
	pages, err := h.ListPages(r.Context(), user)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if pages == nil {
		pages = []Page{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"pages": pages})
}

func (h *Handler) handleSetPage(w http.ResponseWriter, r *http.Request, user string) {
	var payload struct {
		Page Page `json:"page"`
	}
	if err := readJSON(w, r, 64<<10, &payload); err != nil {
		h.writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	if err := h.SetPage(r.Context(), user, payload.Page); err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) handlePublishPage(w http.ResponseWriter, r *http.Request, user string) {
	var payload struct {
		Page  Page   `json:"page"`
		Items []Item `json:"items"`
		Note  string `json:"note"`
	}
	if err := readJSON(w, r, 16<<20, &payload); err != nil {
		h.writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	if payload.Page.Name != r.PathValue("name") {
		h.writeError(w, http.StatusBadRequest, fmt.Errorf("page name does not match request path"))
		return
	}
	if err := h.PublishPage(r.Context(), user, payload.Page, payload.Items, "api", payload.Note); err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "page": payload.Page.Name, "item_count": len(payload.Items),
	})
}

func (h *Handler) handleDeletePage(w http.ResponseWriter, r *http.Request, user string) {
	removed, err := h.DeletePage(r.Context(), user, r.PathValue("name"), "api")
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed_items": removed})
}

// itemsPayload 同时接受 {"items":[...]} 与裸数组两种请求体。
type itemsPayload struct {
	Items []Item `json:"items"`
	Note  string `json:"note"`
}

func decodeItemsPayload(w http.ResponseWriter, r *http.Request) (*itemsPayload, error) {
	body := &itemsPayload{}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	raw, err := readAll(r)
	if err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		err = json.Unmarshal(trimmed, &body.Items)
	} else {
		err = json.Unmarshal(trimmed, body)
	}
	if err != nil {
		return nil, fmt.Errorf("invalid request body: %w", err)
	}
	return body, nil
}

func readAll(r *http.Request) ([]byte, error) {
	var buf bytes.Buffer
	_, err := buf.ReadFrom(r.Body)
	return buf.Bytes(), err
}

func (h *Handler) handlePutItems(w http.ResponseWriter, r *http.Request, user string) {
	body, err := decodeItemsPayload(w, r)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	note := body.Note
	if note == "" {
		note = fmt.Sprintf("put %d item(s)", len(body.Items))
	}
	if err := h.PutItems(r.Context(), user, body.Items, "api", note); err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) handleDeleteItems(w http.ResponseWriter, r *http.Request, user string) {
	ids := r.URL.Query()["id"]
	removed, err := h.DeleteItems(r.Context(), user, ids, "api", fmt.Sprintf("delete %s", strings.Join(ids, ",")))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": removed})
}

func (h *Handler) handleReplaceItems(w http.ResponseWriter, r *http.Request, user string) {
	body, err := decodeItemsPayload(w, r)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	note := body.Note
	if note == "" {
		note = fmt.Sprintf("replace all with %d item(s)", len(body.Items))
	}
	if err := h.ReplaceItems(r.Context(), user, body.Items, "api", note); err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) handleListRevisions(w http.ResponseWriter, r *http.Request, user string) {
	revs, err := h.ListRevisions(r.Context(), user)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if revs == nil {
		revs = []RevisionInfo{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"revisions": revs})
}

func (h *Handler) handleRevisionItems(w http.ResponseWriter, r *http.Request, user string) {
	items, err := h.RevisionItems(r.Context(), user, r.PathValue("id"))
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) handleRollback(w http.ResponseWriter, r *http.Request, user string) {
	if err := h.Rollback(r.Context(), user, r.PathValue("id"), "api"); err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) handleKVGet(w http.ResponseWriter, r *http.Request, user string) {
	key := r.PathValue("key")
	value, revision, err := h.KVGetWithRevision(r.Context(), user, key)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrReservedKVKey) {
			status = http.StatusForbidden
		} else if errors.Is(err, ErrNotFound) {
			status = http.StatusNotFound
		}
		h.writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"value": value, "revision": revision})
}

func (h *Handler) handleKVSet(w http.ResponseWriter, r *http.Request, user string) {
	key := r.PathValue("key")
	var payload struct {
		Value            json.RawMessage `json:"value"`
		ExpectedRevision json.RawMessage `json:"expected_revision,omitempty"`
	}
	if err := readJSON(w, r, MaxKVValueBytes+4096, &payload); err != nil {
		h.writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	revision := ""
	var err error
	if len(payload.ExpectedRevision) != 0 {
		var expected string
		if bytes.Equal(bytes.TrimSpace(payload.ExpectedRevision), []byte("null")) || json.Unmarshal(payload.ExpectedRevision, &expected) != nil {
			h.writeError(w, http.StatusBadRequest, fmt.Errorf("expected_revision must be a non-null string"))
			return
		}
		revision, err = h.KVCompareAndSet(r.Context(), user, key, payload.Value, expected)
	} else {
		err = h.KVSet(r.Context(), user, key, payload.Value)
		if err == nil {
			revision = kvValueRevision(payload.Value)
		}
	}
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrReservedKVKey) {
			status = http.StatusForbidden
		} else if errors.Is(err, ErrKVConflict) {
			var conflict *KVConflictError
			if errors.As(err, &conflict) {
				out := map[string]any{
					"error": err.Error(), "expected_revision": conflict.Expected,
					"current_revision": conflict.Actual, "exists": conflict.Exists,
				}
				if conflict.Exists {
					out["current"] = conflict.Current
				}
				writeJSON(w, http.StatusConflict, out)
				return
			}
			status = http.StatusConflict
		}
		h.writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revision": revision})
}

func (h *Handler) handleKVDelete(w http.ResponseWriter, r *http.Request, user string) {
	key := r.PathValue("key")
	if err := h.KVDelete(r.Context(), user, key); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrReservedKVKey) {
			status = http.StatusForbidden
		}
		h.writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) handleListErrors(w http.ResponseWriter, r *http.Request, user string) {
	errs, err := h.PageErrors(r.Context(), user)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if errs == nil {
		errs = []PageError{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"errors": errs})
}

func (h *Handler) handleReportErrors(w http.ResponseWriter, r *http.Request, user string) {
	var payload struct {
		Errors []PageError `json:"errors"`
	}
	if err := readJSON(w, r, 256<<10, &payload); err != nil {
		h.writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	if err := h.ReportErrors(r.Context(), user, payload.Errors); err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleEvents 是 SSE 端点: 推送 items/kv/pages 变更事件, 25s 心跳保活,
// 订阅成功立即发 resync 消除"先 fetch 后 subscribe"空窗，之后每分钟
// 周期性 resync 使不能报告丢包的外部 Broadcaster 也最终收敛。
// 连接 55 分钟强制到期，配合 LB 排空。
func (h *Handler) handleEvents(w http.ResponseWriter, r *http.Request, user string) {
	if h.closed.Load() {
		h.writeError(w, http.StatusServiceUnavailable, fmt.Errorf("shutting down"))
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		h.writeError(w, http.StatusInternalServerError, fmt.Errorf("streaming unsupported"))
		return
	}
	release := h.acquireConn("sse:"+user, h.opts.limits.MaxSSEPerUser)
	if release == nil {
		h.metric("rate_limited", 1)
		h.writeError(w, http.StatusTooManyRequests, fmt.Errorf("too many event streams"))
		return
	}
	defer release()
	h.metric("sse_connections", 1)
	defer h.metric("sse_connections", -1)

	// 必须先订阅再向客户端宣告就绪；否则响应头已到达
	// 但 Subscribe 尚未建立的短窗口仍可丢失变更。
	ch, cancel := h.opts.broadcaster.Subscribe(user)
	defer cancel()

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, "retry: 3000\n\n")
	initial := h.resyncEvent(user, "subscribe")
	if err := writeSSEEvent(w, initial); err != nil {
		return
	}
	flusher.Flush()

	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	resync := time.NewTicker(time.Minute)
	defer resync.Stop()
	expire := time.NewTimer(55 * time.Minute)
	defer expire.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-h.closeCh:
			return
		case <-expire.C:
			return
		case <-ticker.C:
			if !h.requestUserValid(r, user) {
				return
			}
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-resync.C:
			if err := writeSSEEvent(w, h.resyncEvent(user, "periodic")); err != nil {
				return
			}
			flusher.Flush()
		case ev, ok := <-ch:
			if !ok {
				return // Broadcaster 关闭(优雅停机)
			}
			if err := writeSSEEvent(w, ev); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (h *Handler) resyncEvent(user, reason string) Event {
	ev := Event{Type: "resync", Reason: reason}
	if versioned, ok := h.opts.broadcaster.(VersionedBroadcaster); ok {
		ev.Version = versioned.CurrentVersion(user)
	}
	return ev
}

func writeSSEEvent(w io.Writer, ev Event) error {
	raw, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if ev.Version > 0 {
		if _, err := fmt.Fprintf(w, "id: %d\n", ev.Version); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", raw)
	return err
}
