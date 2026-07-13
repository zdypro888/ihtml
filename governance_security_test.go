package ihtml

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestDefaultResolverRejectsClientControlledIdentity(t *testing.T) {
	h := New(NewMemoryStore())
	srv := httptest.NewServer(h)
	defer srv.Close()

	tests := []struct {
		name   string
		url    string
		header http.Header
	}{
		{name: "query", url: srv.URL + "/api/items?uid=admin"},
		{name: "header", url: srv.URL + "/api/items", header: http.Header{"X-Ihtml-User": []string{"admin"}}},
		{name: "cookie", url: srv.URL + "/api/items", header: http.Header{"Cookie": []string{"ihtml_uid=admin"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, tt.url, nil)
			req.Header = tt.header
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			if res.StatusCode != http.StatusUnauthorized {
				t.Fatalf("client-controlled identity must be rejected, got %d", res.StatusCode)
			}
		})
	}
	if got := h.Stats()["auth_rejected"]; got != int64(len(tests)) {
		t.Fatalf("auth rejection metric = %d, want %d", got, len(tests))
	}
}

func TestUnsafeOriginAndJSONContentTypeGovernance(t *testing.T) {
	srv, _ := newTestServer(t)
	body := `[{"id":"csrf","type":"css","content":"body{}"}]`

	do := func(method, target, origin, contentType string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, target, strings.NewReader(body))
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { res.Body.Close() })
		return res
	}

	if res := do(http.MethodPut, srv.URL+"/api/items?uid=u", "https://evil.example", "text/plain"); res.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin unsafe request = %d, want 403", res.StatusCode)
	}
	if res := do(http.MethodPut, srv.URL+"/api/items?uid=u", "", "text/plain"); res.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("non-JSON write = %d, want 415", res.StatusCode)
	}
	if res := do(http.MethodPut, srv.URL+"/api/items?uid=u", srv.URL, "application/json"); res.StatusCode != http.StatusOK {
		t.Fatalf("same-origin JSON write = %d, want 200", res.StatusCode)
	}
	tlsHandler := New(NewMemoryStore(), WithUserResolver(DevelopmentUserResolver))
	tlsSrv := httptest.NewTLSServer(tlsHandler)
	defer tlsSrv.Close()
	tlsReq, _ := http.NewRequest(http.MethodPut, tlsSrv.URL+"/api/items?uid=u", strings.NewReader(body))
	tlsReq.Header.Set("Content-Type", "application/json")
	tlsReq.Header.Set("Origin", "http"+strings.TrimPrefix(tlsSrv.URL, "https"))
	tlsRes, err := tlsSrv.Client().Do(tlsReq)
	if err != nil {
		t.Fatal(err)
	}
	tlsRes.Body.Close()
	if tlsRes.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-scheme request = %d, want 403", tlsRes.StatusCode)
	}

	deleteReq, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/items?uid=u&id=csrf", nil)
	deleteReq.Header.Set("Origin", "https://evil.example")
	deleteRes, err := http.DefaultClient.Do(deleteReq)
	if err != nil {
		t.Fatal(err)
	}
	deleteRes.Body.Close()
	if deleteRes.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin bodyless write = %d, want 403", deleteRes.StatusCode)
	}

	fetchReq, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/items?uid=u&id=csrf", nil)
	fetchReq.Header.Set("Sec-Fetch-Site", "cross-site")
	fetchRes, err := http.DefaultClient.Do(fetchReq)
	if err != nil {
		t.Fatal(err)
	}
	fetchRes.Body.Close()
	if fetchRes.StatusCode != http.StatusForbidden {
		t.Fatalf("Sec-Fetch-Site cross-site write = %d, want 403", fetchRes.StatusCode)
	}

	cors, _ := newTestServer(t, WithCORS("https://host.example"))
	if res := do(http.MethodPut, cors.URL+"/api/items?uid=u", "https://host.example", "application/json"); res.StatusCode != http.StatusOK || res.Header.Get("Access-Control-Allow-Origin") != "https://host.example" {
		t.Fatalf("allowlisted CORS write = %d, ACAO=%q", res.StatusCode, res.Header.Get("Access-Control-Allow-Origin"))
	}
	preflight, _ := http.NewRequest(http.MethodOptions, cors.URL+"/api/items", nil)
	preflight.Header.Set("Origin", "https://evil.example")
	preflightRes, err := http.DefaultClient.Do(preflight)
	if err != nil {
		t.Fatal(err)
	}
	preflightRes.Body.Close()
	if preflightRes.StatusCode != http.StatusForbidden {
		t.Fatalf("unlisted preflight = %d, want 403", preflightRes.StatusCode)
	}
}

func TestStrictJSONAndSafeExportFilename(t *testing.T) {
	srv, _ := newTestServer(t)
	status, _ := doJSON(t, http.MethodPut, srv.URL+"/api/pages?uid=u", `{"page":{"name":"p","title":"P"}} {}`)
	if status != http.StatusBadRequest {
		t.Fatalf("trailing JSON must be rejected, got %d", status)
	}

	malicious := "victim\"\r\nX-Evil: injected"
	h := New(NewMemoryStore(), WithUserResolver(func(*http.Request) (string, error) { return malicious, nil }))
	exportSrv := httptest.NewServer(h)
	defer exportSrv.Close()
	res, err := http.Get(exportSrv.URL + "/api/export")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("export status: %d", res.StatusCode)
	}
	cd := res.Header.Get("Content-Disposition")
	if strings.ContainsAny(cd, "\r\n") || res.Header.Get("X-Evil") != "" {
		t.Fatalf("unsafe Content-Disposition: %q, X-Evil=%q", cd, res.Header.Get("X-Evil"))
	}
}

func TestMessageOriginsConfigurePostMessageAndFrameAncestors(t *testing.T) {
	srv, _ := newTestServer(t, WithMessageOrigins(
		"https://host.example/",
		"https://host.example", // duplicate is normalized away
		"javascript://invalid.example",
	))
	status, out := doJSON(t, http.MethodGet, srv.URL+"/api/config?uid=u", "")
	if status != http.StatusOK {
		t.Fatalf("config status: %d", status)
	}
	origins, ok := out["message_origins"].([]any)
	if !ok || len(origins) != 1 || origins[0] != "https://host.example" {
		t.Fatalf("message origins = %#v", out["message_origins"])
	}
	res, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if csp := res.Header.Get("Content-Security-Policy"); csp != "frame-ancestors 'self' https://host.example" {
		t.Fatalf("frame ancestors CSP = %q", csp)
	}
	if xfo := res.Header.Get("X-Frame-Options"); xfo != "" {
		t.Fatalf("cross-origin frame policy must not send conflicting XFO: %q", xfo)
	}

	defaultSrv, _ := newTestServer(t)
	defaultRes, err := http.Get(defaultSrv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defaultRes.Body.Close()
	if csp := defaultRes.Header.Get("Content-Security-Policy"); csp != "frame-ancestors 'self'" {
		t.Fatalf("default frame CSP = %q", csp)
	}
	if xfo := defaultRes.Header.Get("X-Frame-Options"); xfo != "SAMEORIGIN" {
		t.Fatalf("default XFO = %q", xfo)
	}
}

func TestHubOverflowSignalsResyncAndVersions(t *testing.T) {
	hub := newHub()
	ch, cancel := hub.Subscribe("u")
	defer cancel()
	for i := 0; i < 17; i++ {
		hub.Publish("u", Event{Type: "items", Op: "put"})
	}
	ev := <-ch
	if ev.Type != "resync" || ev.Reason != "overflow" || ev.Version != 17 {
		t.Fatalf("overflow event = %+v", ev)
	}
	hub.Publish("u", Event{Type: "pages"})
	ev = <-ch
	if ev.Type != "pages" || ev.Version != 18 {
		t.Fatalf("post-resync event = %+v", ev)
	}
}

type passiveBroadcaster struct {
	ch       chan Event
	canceled chan struct{}
	once     sync.Once
}

func newPassiveBroadcaster() *passiveBroadcaster {
	return &passiveBroadcaster{ch: make(chan Event, 1), canceled: make(chan struct{})}
}

func (b *passiveBroadcaster) Publish(_ string, ev Event) {
	select {
	case b.ch <- ev:
	default:
	}
}

func (b *passiveBroadcaster) Subscribe(string) (<-chan Event, func()) {
	return b.ch, func() {
		b.once.Do(func() {
			close(b.canceled)
			close(b.ch)
		})
	}
}

func TestSSEStartsWithResyncAndCustomBroadcasterCloses(t *testing.T) {
	b := newPassiveBroadcaster()
	srv, h := newTestServer(t, WithBroadcaster(b))
	res, err := http.Get(srv.URL + "/api/events?uid=u")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	scanner := bufio.NewScanner(res.Body)
	found := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			var ev Event
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
				t.Fatal(err)
			}
			if ev.Type != "resync" || ev.Reason != "subscribe" {
				t.Fatalf("first SSE event = %+v", ev)
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatal("missing initial resync")
	}

	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, res.Body)
		done <- err
	}()
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-b.canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("custom broadcaster subscription was not canceled")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("SSE did not terminate after Handler.Close")
	}
}

type captureBackend struct {
	sessions chan *ChatSessionContext
	events   chan ChatClientEvent
	models   ChatModels
}

func newCaptureBackend(models ChatModels) *captureBackend {
	return &captureBackend{
		sessions: make(chan *ChatSessionContext, 4),
		events:   make(chan ChatClientEvent, 4),
		models:   models,
	}
}

func (b *captureBackend) NewSession(_ context.Context, sctx *ChatSessionContext) (ChatSession, error) {
	b.sessions <- sctx
	return captureSession{events: b.events}, nil
}

func (b *captureBackend) Models(context.Context) (ChatModels, error) { return b.models, nil }

type captureSession struct{ events chan ChatClientEvent }

func (s captureSession) HandleEvent(_ context.Context, ev *ChatClientEvent) { s.events <- *ev }
func (captureSession) Close()                                               {}

func dialChat(t *testing.T, serverURL, user string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(serverURL, "http") + "/api/chat/ws?uid=" + user
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestChatServiceIsScopedRateLimitedAndAudited(t *testing.T) {
	backend := newCaptureBackend(ChatModels{ModelReady: true})
	var readOnly atomic.Bool
	audits := make(chan AuditEvent, 4)
	h := New(NewMemoryStore(),
		WithUserResolver(DevelopmentUserResolver),
		WithChatBackend(backend),
		WithLimits(Limits{WritesPerMinute: 2}),
		WithAccessPolicy(func(*http.Request, string) Access { return Access{ReadOnly: readOnly.Load()} }),
		WithAudit(func(_ context.Context, ev AuditEvent) { audits <- ev }),
	)
	srv := httptest.NewServer(h)
	defer srv.Close()
	_ = dialChat(t, srv.URL, "alice")
	sctx := <-backend.sessions
	if sctx.ScopedService == nil {
		t.Fatal("ChatBackend did not receive the request-bound ScopedService")
	}
	if _, err := sctx.ScopedService.ListItems(context.Background()); err != nil {
		t.Fatalf("scoped read failed: %v", err)
	}

	if _, err := sctx.Service.ListItems(context.Background(), "bob"); !errors.Is(err, ErrServiceScope) {
		t.Fatalf("cross-user service access = %v", err)
	}
	if err := sctx.Service.KVSet(context.Background(), "alice", "theme", json.RawMessage(`{"mode":"dark"}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case audit := <-audits:
		if audit.Action != "kv.set" || audit.By != "chat" {
			t.Fatalf("chat audit = %+v", audit)
		}
	case <-time.After(time.Second):
		t.Fatal("missing chat audit")
	}
	if err := sctx.Service.PutItems(context.Background(), "alice", []Item{{ID: "one", Type: ItemCSS, Content: "x"}}, "spoofed-api", "test"); err != nil {
		t.Fatal(err)
	}
	if err := sctx.Service.KVSet(context.Background(), "alice", "second", json.RawMessage(`1`)); !errors.Is(err, ErrWriteRateLimited) {
		t.Fatalf("third AI write = %v, want rate limit", err)
	}
	status, _ := doJSON(t, http.MethodPut, srv.URL+"/api/items?uid=alice", `[{"id":"http","type":"css","content":"x"}]`)
	if status != http.StatusTooManyRequests {
		t.Fatalf("HTTP and AI should share write quota, HTTP status = %d", status)
	}
	readOnly.Store(true)
	if err := sctx.Service.KVSet(context.Background(), "alice", "blocked", json.RawMessage(`1`)); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("revoked write = %v, want access denied", err)
	}
}

func TestChatModelMetadataAndOverrideAreHiddenByDefault(t *testing.T) {
	backend := newCaptureBackend(ChatModels{
		Provider:     "claude",
		ModelReady:   true,
		CurrentModel: "secret-model",
		Models:       []string{"secret-model", "other-internal-model"},
		ModelSelect:  false,
	})
	srv, _ := newTestServer(t, WithChatBackend(backend))
	status, out := doJSON(t, http.MethodGet, srv.URL+"/api/chat/models?uid=u", "")
	if status != http.StatusOK || out["provider"] != nil || out["current_model"] != "" || len(out["models"].([]any)) != 0 || out["protocol_version"] != float64(ChatProtocolVersion) {
		t.Fatalf("hidden models response: %d %v", status, out)
	}
	conn := dialChat(t, srv.URL, "u")
	<-backend.sessions
	if err := conn.WriteJSON(ChatClientEvent{Type: "user_message", Model: "attacker-model"}); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-backend.events:
		if ev.Model != "" {
			t.Fatalf("client model override was not stripped: %q", ev.Model)
		}
	case <-time.After(time.Second):
		t.Fatal("backend did not receive event")
	}

	selectable := newCaptureBackend(ChatModels{
		Provider: "claude", ModelReady: true, CurrentModel: "public-default", Models: []string{"public-default", "public-alt"}, ModelSelect: true,
	})
	selectSrv, _ := newTestServer(t, WithChatBackend(selectable))
	status, out = doJSON(t, http.MethodGet, selectSrv.URL+"/api/chat/models?uid=u", "")
	if status != http.StatusOK || out["provider"] != "claude" || out["current_model"] != "public-default" || len(out["models"].([]any)) != 2 || out["protocol_version"] != float64(ChatProtocolVersion) {
		t.Fatalf("explicit model selection response: %d %v", status, out)
	}
	selectConn := dialChat(t, selectSrv.URL, "u")
	<-selectable.sessions
	if err := selectConn.WriteJSON(ChatClientEvent{Type: "user_message", Model: "public-alt"}); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-selectable.events:
		if ev.Model != "public-alt" {
			t.Fatalf("explicit model override = %q", ev.Model)
		}
	case <-time.After(time.Second):
		t.Fatal("selectable backend did not receive event")
	}
}

func TestChatPreviewOnlyCannotAdvertiseRealModelReadiness(t *testing.T) {
	backend := newCaptureBackend(ChatModels{ModelReady: true, PreviewOnly: true})
	srv, _ := newTestServer(t, WithChatBackend(backend))
	status, out := doJSON(t, http.MethodGet, srv.URL+"/api/chat/models?uid=u", "")
	if status != http.StatusOK || out["preview_only"] != true || out["model_ready"] != false {
		t.Fatalf("preview capability was not normalized: %d %v", status, out)
	}
}

func TestChatDisabledRevocationStopsNewEvents(t *testing.T) {
	backend := newCaptureBackend(ChatModels{ModelReady: true})
	var disabled atomic.Bool
	srv, _ := newTestServer(t,
		WithChatBackend(backend),
		WithAccessPolicy(func(*http.Request, string) Access { return Access{ChatDisabled: disabled.Load()} }),
	)
	conn := dialChat(t, srv.URL, "u")
	<-backend.sessions
	if err := conn.WriteJSON(ChatClientEvent{Type: "cancel"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-backend.events:
	case <-time.After(time.Second):
		t.Fatal("initial event was not handled")
	}
	disabled.Store(true)
	if err := conn.WriteJSON(ChatClientEvent{Type: "user_message"}); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	var stream ChatStreamEvent
	if err := conn.ReadJSON(&stream); err != nil {
		t.Fatal(err)
	}
	if stream.Type != "turn_error" {
		t.Fatalf("revocation response = %+v", stream)
	}
	select {
	case ev := <-backend.events:
		t.Fatalf("revoked event reached backend: %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
}
