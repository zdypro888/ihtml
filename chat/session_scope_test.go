package chat

import (
	"context"
	"encoding/hex"
	"net/http/httptest"
	"testing"

	"github.com/zdypro888/ihtml"
)

func TestSessionCapabilityFingerprintIsCanonicalAndScoped(t *testing.T) {
	cfg := Config{
		Provider: "openai", SystemPrompt: "host policy", ProbeBaseURL: "https://app.example",
		SessionNamespace: "billing-app", SessionCapabilityVersion: "v1",
		ProbeForwardHeaders: []string{"Authorization", "cookie"},
	}
	apis := []ihtml.APISpec{
		{Name: "b", Method: "get", Path: "/b", Description: "B"},
		{Name: "a", Method: "GET", Path: "/a", Description: "A"},
	}
	first := sessionCapabilityFingerprint(cfg, ihtml.Access{}, apis)
	if len(first) != 64 {
		t.Fatalf("capability fingerprint length = %d, want 64 hex characters", len(first))
	}
	if _, err := hex.DecodeString(first); err != nil {
		t.Fatalf("capability fingerprint is not canonical hex: %v", err)
	}
	reordered := append([]ihtml.APISpec(nil), apis...)
	reordered[0], reordered[1] = reordered[1], reordered[0]
	cfg.ProbeForwardHeaders = []string{"COOKIE", "authorization", "cookie"}
	if got := sessionCapabilityFingerprint(cfg, ihtml.Access{}, reordered); got != first {
		t.Fatal("equivalent capability sets produced different fingerprints")
	}
	if got := sessionCapabilityFingerprint(cfg, ihtml.Access{ReadOnly: true}, reordered); got == first {
		t.Fatal("read-only boundary did not rotate the capability fingerprint")
	}
	cfg.ProbeForwardQuery = []string{"uid"}
	queryLower := sessionCapabilityFingerprint(cfg, ihtml.Access{}, reordered)
	cfg.ProbeForwardQuery = []string{"UID"}
	if got := sessionCapabilityFingerprint(cfg, ihtml.Access{}, reordered); got == queryLower {
		t.Fatal("case-sensitive query forwarding boundary did not rotate the fingerprint")
	}
	cfg.ProbeForwardQuery = nil
	cfg.SystemPrompt = "changed policy"
	if got := sessionCapabilityFingerprint(cfg, ihtml.Access{}, reordered); got == first {
		t.Fatal("trusted policy change did not rotate the capability fingerprint")
	}
	cfg.SystemPrompt = "host policy"
	cfg.SessionNamespace = "support-app"
	if got := sessionCapabilityFingerprint(cfg, ihtml.Access{}, reordered); got == first {
		t.Fatal("application namespace did not rotate the capability fingerprint")
	}
	cfg.SessionNamespace = "billing-app"
	cfg.SessionCapabilityVersion = "v2"
	if got := sessionCapabilityFingerprint(cfg, ihtml.Access{}, reordered); got == first {
		t.Fatal("host capability version did not rotate the capability fingerprint")
	}
}

func TestSecureSessionIDScopesCapabilitiesWithoutLeakingThem(t *testing.T) {
	a := secureSessionIDScoped("alice", "stable", "scope-a")
	b := secureSessionIDScoped("alice", "stable", "scope-b")
	if a == b {
		t.Fatal("capability scopes collided")
	}
	if a != secureSessionIDScoped("alice", "stable", "scope-a") {
		t.Fatal("scoped session ID is not stable")
	}
}

func TestUncheckedBackendsFailSafeWithUniqueExternalStoreNamespaces(t *testing.T) {
	store := newBoundedSessionStore(historyLimits{maxSessions: -1, maxEvents: -1, maxBytes: -1, idleTTL: -1})
	first := New(Config{SessionStore: store})
	second := New(Config{SessionStore: store})
	if first.cfg.SessionNamespace == "" || second.cfg.SessionNamespace == "" || first.cfg.SessionNamespace == second.cfg.SessionNamespace {
		t.Fatalf("ephemeral external-store namespaces = %q / %q", first.cfg.SessionNamespace, second.cfg.SessionNamespace)
	}
}

func TestUncheckedBackendsFailSafeWithUniqueExternalNonceStoreNamespaces(t *testing.T) {
	store := newMemoryConfirmNonceStore()
	first := New(Config{ConfirmNonceStore: store})
	second := New(Config{ConfirmNonceStore: store})
	if first.cfg.SessionNamespace == "" || second.cfg.SessionNamespace == "" || first.cfg.SessionNamespace == second.cfg.SessionNamespace {
		t.Fatalf("ephemeral external-nonce namespaces = %q / %q", first.cfg.SessionNamespace, second.cfg.SessionNamespace)
	}
}

func TestSharedConfirmationInfrastructureCannotCrossApplicationNamespace(t *testing.T) {
	shared := newMemoryConfirmNonceStore()
	secret := []byte("one shared secret used by multiple host applications")
	first := New(Config{ConfirmSecret: secret, ConfirmNonceStore: shared, SessionNamespace: "application-a"})
	second := New(Config{ConfirmSecret: secret, ConfirmNonceStore: shared, SessionNamespace: "application-b"})

	user, csid := "alice", "stable-browser-session"
	service := ihtml.New(ihtml.NewMemoryStore())
	defer service.Close()
	newSession := func(backend *Backend) *session {
		created, err := backend.NewSession(context.Background(), &ihtml.ChatSessionContext{
			User: user, Service: service, Emit: func(ihtml.ChatStreamEvent) error { return nil },
			Request: httptest.NewRequest("GET", "http://ihtml.test/api/chat?csid="+csid, nil),
		})
		if err != nil {
			t.Fatal(err)
		}
		actual, ok := created.(*session)
		if !ok {
			t.Fatalf("session type = %T", created)
		}
		return actual
	}
	firstRuntime := newSession(first)
	secondRuntime := newSession(second)
	defer firstRuntime.Close()
	defer secondRuntime.Close()

	firstScope, secondScope := firstRuntime.capabilityScope, secondRuntime.capabilityScope
	firstSession, secondSession := firstRuntime.sessionID, secondRuntime.sessionID
	if firstSession == secondSession || firstScope == secondScope {
		t.Fatal("application namespace did not isolate the derived confirmation scope")
	}
	call, err := first.signer.prepareContext(context.Background(), user, newConfirmationScope(firstSession, firstScope), ihtml.ProposedToolCall{
		ID: "cross-app", Name: "ui_kv_set", Method: "TOOL", Path: "ui://ui_kv_set",
		RequiresConfirmation: true, Body: map[string]any{"key": "x", "value": "y"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := second.signer.consumeContext(context.Background(), user, newConfirmationScope(secondSession, secondScope), call); err != nil || ok {
		t.Fatalf("cross-application confirmation accepted: ok=%v err=%v", ok, err)
	}
	if ok, err := first.signer.consumeContext(context.Background(), user, newConfirmationScope(firstSession, firstScope), call); err != nil || !ok {
		t.Fatalf("issuer could not consume intact card: ok=%v err=%v", ok, err)
	}
}
