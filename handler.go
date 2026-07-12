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
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

//go:embed web
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

	closed atomic.Bool
}

type rateWindow struct {
	start int64 // 分钟窗口起点(unix 分钟)
	n     int
}

// New 创建 Handler。store 必填, 行为经 Option 调整。
func New(store Store, opts ...Option) *Handler {
	if store == nil {
		panic("ihtml: store is required")
	}
	o := options{
		resolver:     DefaultUserResolver,
		maxRevisions: DefaultMaxRevisions,
		logger:       slog.Default(),
		pageTitle:    "ihtml",
		limits:       Limits{}.normalized(),
	}
	for _, fn := range opts {
		fn(&o)
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
	}
	raw, err := webFS.ReadFile("web/index.html")
	if err != nil {
		panic(fmt.Sprintf("ihtml: embedded index.html missing: %v", err))
	}
	h.page = bytes.Replace(raw, []byte("{{TITLE}}"), []byte(htmlEscape(o.pageTitle)), 1)
	h.pageETag = etagOf(h.page)
	h.routes()
	return h
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

// accessFor 计算该请求的访问策略。
func (h *Handler) accessFor(r *http.Request, user string) Access {
	if h.opts.access == nil {
		return Access{}
	}
	return h.opts.access(r, user)
}

// Close 优雅停机: 拒绝新连接、断开全部 SSE/WS。应在 http.Server.Shutdown
// 前调用(WS 是被劫持的连接, Server.Shutdown 不会等待它们)。
func (h *Handler) Close() error {
	if h.closed.Swap(true) {
		return nil
	}
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

func (h *Handler) routes() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", h.handlePage)
	// /ihtml.js = 标准库 + 内核 拼接下发(宿主只需引入一个脚本)
	mux.HandleFunc("GET /ihtml.js", h.handleBundle("text/javascript; charset=utf-8", "web/ihtml-lib.js", "web/ihtml.js"))
	mux.HandleFunc("GET /ihtml.css", h.handleAsset("web/ihtml.css", "text/css; charset=utf-8"))

	mux.HandleFunc("GET /api/config", h.withUser(h.handleConfig))
	mux.HandleFunc("GET /api/items", h.withUser(h.handleListItems))
	mux.HandleFunc("PUT /api/items", h.withWrite(h.handlePutItems))
	mux.HandleFunc("POST /api/items", h.withWrite(h.handlePutItems)) // 等价 PUT, 方便简单客户端
	mux.HandleFunc("DELETE /api/items", h.withWrite(h.handleDeleteItems))
	mux.HandleFunc("POST /api/items/replace", h.withWrite(h.handleReplaceItems))
	mux.HandleFunc("GET /api/pages", h.withUser(h.handleListPages))
	mux.HandleFunc("PUT /api/pages", h.withWrite(h.handleSetPage))
	mux.HandleFunc("DELETE /api/pages/{name}", h.withWrite(h.handleDeletePage))
	mux.HandleFunc("GET /api/revisions", h.withUser(h.handleListRevisions))
	mux.HandleFunc("GET /api/revisions/{id}", h.withUser(h.handleRevisionItems))
	mux.HandleFunc("POST /api/revisions/{id}/rollback", h.withWrite(h.handleRollback))
	mux.HandleFunc("GET /api/kv/{key}", h.withUser(h.handleKVGet))
	mux.HandleFunc("PUT /api/kv/{key}", h.withWrite(h.handleKVSet))
	mux.HandleFunc("DELETE /api/kv/{key}", h.withWrite(h.handleKVDelete))
	mux.HandleFunc("GET /api/export", h.withUser(h.handleExport))
	mux.HandleFunc("GET /api/events", h.withUser(h.handleEvents))
	mux.HandleFunc("GET /api/errors", h.withUser(h.handleListErrors))
	mux.HandleFunc("POST /api/errors", h.withUser(h.handleReportErrors))
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
		fn(w, r, user)
	})
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
	// CORS: 宿主页面跨域 mount 内核时放行(鉴权走参数, 不放 cookie)
	if origin := r.Header.Get("Origin"); origin != "" && h.originAllowed(origin) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Ihtml-User")
			w.Header().Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
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

func (h *Handler) handlePage(w http.ResponseWriter, r *http.Request) {
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
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		fn(w, r, user)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
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
	return dec.Decode(v)
}

func (h *Handler) handleConfig(w http.ResponseWriter, r *http.Request, user string) {
	access := h.accessFor(r, user)
	out := map[string]any{
		"user":      user,
		"chat":      h.opts.chat != nil && !access.ChatDisabled,
		"read_only": access.ReadOnly,
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
	w.Header().Set("Content-Disposition", `attachment; filename="ihtml-`+htmlEscape(user)+`.json"`)
	writeJSON(w, http.StatusOK, export)
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
	value, err := h.KVGet(r.Context(), user, r.PathValue("key"))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"value": value})
}

func (h *Handler) handleKVSet(w http.ResponseWriter, r *http.Request, user string) {
	key := r.PathValue("key")
	if strings.HasPrefix(key, reservedKVPrefix) {
		h.writeError(w, http.StatusForbidden, fmt.Errorf("kv key %q is reserved", key))
		return
	}
	var payload struct {
		Value json.RawMessage `json:"value"`
	}
	if err := readJSON(w, r, MaxKVValueBytes+4096, &payload); err != nil {
		h.writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	if err := h.KVSet(r.Context(), user, key, payload.Value); err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) handleKVDelete(w http.ResponseWriter, r *http.Request, user string) {
	key := r.PathValue("key")
	if strings.HasPrefix(key, reservedKVPrefix) {
		h.writeError(w, http.StatusForbidden, fmt.Errorf("kv key %q is reserved", key))
		return
	}
	if err := h.KVDelete(r.Context(), user, key); err != nil {
		h.writeError(w, http.StatusBadRequest, err)
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
// 55 分钟强制到期(配合 LB 排空; 内核会自动重连并整体 refresh)。
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

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, "retry: 3000\n\n: hello\n\n")
	flusher.Flush()

	ch, cancel := h.opts.broadcaster.Subscribe(user)
	defer cancel()
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	expire := time.NewTimer(55 * time.Minute)
	defer expire.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-expire.C:
			return
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case ev, ok := <-ch:
			if !ok {
				return // Broadcaster 关闭(优雅停机)
			}
			raw, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", raw); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
