package chat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zdypro888/ihtml"
)

func TestProbeAPIUsesExplicitBaseAndControlledIdentity(t *testing.T) {
	var gotCookie, gotAuthorization, gotUser, gotQuery, gotUID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCookie = r.Header.Get("Cookie")
		gotAuthorization = r.Header.Get("Authorization")
		gotUser = r.Header.Get("X-Probe-User")
		gotQuery = r.URL.Query().Get("q")
		gotUID = r.URL.Query().Get("uid")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	inbound := http.Header{
		"Cookie":        {"session=abc"},
		"Authorization": {"Bearer must-not-leak"},
	}
	rt := &runtime{
		user:         "alice",
		apiBase:      server.URL,
		apis:         []ihtml.APISpec{{Method: http.MethodGet, Path: "/players"}},
		inbound:      inbound,
		inboundQuery: url.Values{"uid": {"alice"}},
		cfg: Config{
			ProbeForwardHeaders: []string{"Cookie"},
			ProbeForwardQuery:   []string{"uid"},
			ProbeRequest: func(_ context.Context, user string, source, outbound http.Header) error {
				source.Set("Cookie", "mutated-copy=1")
				outbound.Set("X-Probe-User", user)
				return nil
			},
		},
	}
	result, _, err := probeAPI(context.Background(), rt, map[string]any{
		"path":  "/players",
		"query": map[string]any{"q": "张三", "uid": "mallory"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["status"] != http.StatusOK {
		t.Fatalf("result: %#v", result)
	}
	if gotCookie != "session=abc" || gotAuthorization != "" || gotUser != "alice" || gotQuery != "张三" || gotUID != "alice" {
		t.Fatalf("identity/query mismatch cookie=%q authorization=%q user=%q query=%q uid=%q", gotCookie, gotAuthorization, gotUser, gotQuery, gotUID)
	}
	if inbound.Get("Cookie") != "session=abc" {
		t.Fatalf("decorator mutated captured inbound headers: %q", inbound.Get("Cookie"))
	}
}

func TestProbeAPIDoesNotTrustRequestHost(t *testing.T) {
	backend := New(Config{})
	req := httptest.NewRequest(http.MethodGet, "http://attacker.invalid/chat?csid=abc", nil)
	req.Host = "169.254.169.254"
	svc := ihtml.New(ihtml.NewMemoryStore())
	created, err := backend.NewSession(context.Background(), &ihtml.ChatSessionContext{
		User: "alice", Service: svc, Emit: func(ihtml.ChatStreamEvent) error { return nil }, Request: req,
	})
	if err != nil {
		t.Fatal(err)
	}
	sess := created.(*session)
	defer sess.Close()
	if sess.apiBase != "" {
		t.Fatalf("Request.Host became probe base: %q", sess.apiBase)
	}
}

func TestProbeAPIRejectsCrossOriginRedirect(t *testing.T) {
	var reached atomic.Bool
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached.Store(true)
	}))
	defer destination.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+"/next", http.StatusFound)
	}))
	defer origin.Close()

	rt := &runtime{
		apiBase: origin.URL,
		apis: []ihtml.APISpec{
			{Method: http.MethodGet, Path: "/start"},
			{Method: http.MethodGet, Path: "/next"},
		},
	}
	_, _, err := probeAPI(context.Background(), rt, map[string]any{"path": "/start"})
	if err == nil || !strings.Contains(err.Error(), "跨主机重定向") {
		t.Fatalf("expected cross-origin rejection, got %v", err)
	}
	if reached.Load() {
		t.Fatal("cross-origin redirect was followed")
	}
}

func TestProbeAPIAllowsOnlyCataloguedSameOriginRedirect(t *testing.T) {
	var server *httptest.Server
	gotUID := ""
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, server.URL+"/next?uid=mallory", http.StatusFound)
			return
		}
		gotUID = r.URL.Query().Get("uid")
		_, _ = w.Write([]byte(`{"redirected":true}`))
	}))
	defer server.Close()
	rt := &runtime{
		apiBase:      server.URL,
		inboundQuery: url.Values{"uid": {"alice"}},
		cfg:          Config{ProbeForwardQuery: []string{"uid"}},
		apis: []ihtml.APISpec{
			{Method: http.MethodGet, Path: "/start"},
			{Method: http.MethodGet, Path: "/next"},
		},
	}
	result, _, err := probeAPI(context.Background(), rt, map[string]any{"path": "/start"})
	if err != nil || result.(map[string]any)["status"] != http.StatusOK {
		t.Fatalf("safe redirect: %#v %v", result, err)
	}
	if gotUID != "alice" {
		t.Fatalf("redirect overrode forwarded identity: %q", gotUID)
	}

	rt.apis = rt.apis[:1]
	_, _, err = probeAPI(context.Background(), rt, map[string]any{"path": "/start"})
	if err == nil || !strings.Contains(err.Error(), "目录内") {
		t.Fatalf("uncatalogued redirect should fail: %v", err)
	}
}

func TestProbePathCanonicalizationAndMethod(t *testing.T) {
	specs := []ihtml.APISpec{
		{Method: http.MethodGet, Path: "/players/{id}/bets"},
		{Method: http.MethodPost, Path: "/admin"},
	}
	if !apiPathAllowed(specs, "/players/88/bets") {
		t.Fatal("valid templated GET path rejected")
	}
	for _, candidate := range []string{
		"//evil/x", "/players/../admin", "/players/%2e%2e/admin",
		"/players/%252e%252e/admin", "/players/a%2fb/bets", "/players\\admin",
		"/players//88/bets", "/players/88/bets?next=/admin", "/admin",
	} {
		if apiPathAllowed(specs, candidate) {
			t.Errorf("unsafe/non-GET path allowed: %q", candidate)
		}
	}
}

func TestProbeRequestCannotRewriteTarget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	rt := &runtime{
		user: "alice", apiBase: server.URL,
		apis: []ihtml.APISpec{{Method: http.MethodGet, Path: "/safe"}},
		cfg: Config{ProbeRequest: func(_ context.Context, _ string, _, outbound http.Header) error {
			outbound.Set("Host", "attacker.invalid")
			return nil
		}},
	}
	_, _, err := probeAPI(context.Background(), rt, map[string]any{"path": "/safe"})
	if err == nil || !strings.Contains(err.Error(), "不允许设置探测请求头") {
		t.Fatalf("host header rewrite accepted: %v", err)
	}
}
