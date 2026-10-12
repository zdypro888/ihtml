package chat

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/confirmation"
)

var testConfirmationScope = newConfirmationScope("session-a", "capability-a")

func TestLegacyConfirmationWireSignatureIsFrozen(t *testing.T) {
	signer := newConfirmSigner([]byte("legacy-wire-secret"))
	call := ihtml.ProposedToolCall{
		ID: "card-a", Name: "write", Title: "Update", Method: "TOOL", Path: "ui://write",
		Query: map[string]any{"b": 2, "a": 1}, Body: map[string]any{"value": 3, "id": "a"},
		Risk: "high", RequiresConfirmation: true, Reason: "changes data",
		AgentMode: "deep", ApprovalResume: "manual_tool_only",
		ConfirmationNonce: "nonce-a", ConfirmationExpires: 1900000000,
	}
	const want = "1cbfc85f6d504d55b73a97a043df50bbbe8695df503caabf582dff222b06ccb8"
	if got := signer.sign("alice", testConfirmationScope, call); got != want {
		t.Fatalf("legacy signature changed: got %s, want %s", got, want)
	}
}

func TestChatAndPublicConfirmationInteroperate(t *testing.T) {
	store := newMemoryConfirmNonceStore()
	secret := []byte("same-host-confirmation-secret")
	legacy := newConfirmSigner(secret, store)
	shared, err := confirmation.NewChecked(confirmation.Config{Secret: secret, NonceStore: store})
	if err != nil {
		t.Fatal(err)
	}
	for _, reverse := range []bool{false, true} {
		call := ihtml.ProposedToolCall{
			ID: "write-card", Name: "ui_item_put", Title: "Update item",
			Method: "TOOL", Path: "ui://ui_item_put", Risk: "medium",
			Body: map[string]any{"id": "item-a"}, RequiresConfirmation: true,
			AgentMode: AgentModeDeep, ApprovalResume: "manual_tool_only",
		}
		var signed ihtml.ProposedToolCall
		if reverse {
			signed, err = shared.Prepare(context.Background(), "alice", confirmation.Scope(testConfirmationScope), call)
		} else {
			signed, err = legacy.prepareContext(context.Background(), "alice", testConfirmationScope, call)
		}
		if err != nil {
			t.Fatal(err)
		}
		var ok bool
		if reverse {
			ok, err = legacy.consumeContext(context.Background(), "alice", testConfirmationScope, signed)
		} else {
			ok, err = shared.Consume(context.Background(), "alice", confirmation.Scope(testConfirmationScope), signed)
		}
		if err != nil || !ok {
			t.Fatalf("cross-package consumption reverse=%v: %v, %v", reverse, ok, err)
		}
	}
}

func TestConfirmationSharedNonceStoreSurvivesSignerRestart(t *testing.T) {
	shared := newMemoryConfirmNonceStore()
	secret := []byte("a sufficiently long production secret")
	issuer := newConfirmSigner(secret, shared)
	consumerAfterRestart := newConfirmSigner(secret, shared)
	call, err := issuer.prepareContext(context.Background(), "alice", testConfirmationScope, ihtml.ProposedToolCall{
		Name: "ui_item_put", Method: "TOOL", Path: "ui://ui_item_put",
		Body: map[string]any{"items": []any{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ok, err := consumerAfterRestart.consumeContext(context.Background(), "alice", testConfirmationScope, call)
	if err != nil || !ok {
		t.Fatalf("shared confirmation was not accepted after restart: ok=%v err=%v", ok, err)
	}
	ok, err = issuer.consumeContext(context.Background(), "alice", testConfirmationScope, call)
	if err != nil || ok {
		t.Fatalf("confirmation replay accepted: ok=%v err=%v", ok, err)
	}
}

func TestConfirmationDifferentSecretAndExpiredCardRejected(t *testing.T) {
	shared := newMemoryConfirmNonceStore()
	issuer := newConfirmSigner([]byte("secret-one"), shared)
	call, err := issuer.prepareContext(context.Background(), "alice", testConfirmationScope, ihtml.ProposedToolCall{
		Name: "ui_theme_set", Method: "TOOL", Path: "ui://ui_theme_set", Body: map[string]any{"mode": "dark"},
	})
	if err != nil {
		t.Fatal(err)
	}
	other := newConfirmSigner([]byte("secret-two"), shared)
	if ok, err := other.consumeContext(context.Background(), "alice", testConfirmationScope, call); err != nil || ok {
		t.Fatalf("different key accepted: ok=%v err=%v", ok, err)
	}
	call.ConfirmationExpires = time.Now().Unix()
	call.ConfirmationToken = issuer.sign("alice", testConfirmationScope, call)
	if ok, err := issuer.consumeContext(context.Background(), "alice", testConfirmationScope, call); err != nil || ok {
		t.Fatalf("expired card accepted: ok=%v err=%v", ok, err)
	}
}

func TestConfirmationSignsAgentAndResumeMode(t *testing.T) {
	signer := newConfirmSigner([]byte("secret"))
	call, err := signer.prepareContext(context.Background(), "alice", testConfirmationScope, ihtml.ProposedToolCall{
		Name: "ui_item_put", Method: "TOOL", Path: "ui://ui_item_put",
		AgentMode: AgentModeDeep, ApprovalResume: "manual_tool_only",
		RequiresConfirmation: true, Body: map[string]any{"items": []any{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tampered := call
	tampered.AgentMode = AgentModeFast
	if ok, err := signer.consumeContext(context.Background(), "alice", testConfirmationScope, tampered); err != nil || ok {
		t.Fatalf("tampered agent mode accepted: ok=%v err=%v", ok, err)
	}
	tampered = call
	tampered.ApprovalResume = "checkpoint"
	if ok, err := signer.consumeContext(context.Background(), "alice", testConfirmationScope, tampered); err != nil || ok {
		t.Fatalf("tampered resume capability accepted: ok=%v err=%v", ok, err)
	}
	if ok, err := signer.consumeContext(context.Background(), "alice", testConfirmationScope, call); err != nil || !ok {
		t.Fatalf("original deep confirmation rejected: ok=%v err=%v", ok, err)
	}
}

func TestConfirmationIsBoundToSessionCapabilityAndToolProtocol(t *testing.T) {
	shared := newMemoryConfirmNonceStore()
	signer := newConfirmSigner([]byte("one shared secret for every application"), shared)
	issuedScope := newConfirmationScope("session-app-a", "app-a-capabilities")
	call, err := signer.prepareContext(context.Background(), "alice", issuedScope, ihtml.ProposedToolCall{
		Name: "ui_item_put", Method: "TOOL", Path: "ui://ui_item_put",
		RequiresConfirmation: true, Body: map[string]any{"items": []any{}},
	})
	if err != nil {
		t.Fatal(err)
	}

	wrongScopes := []struct {
		name  string
		scope confirmationScope
	}{
		{name: "other logical session", scope: newConfirmationScope("session-app-a-2", "app-a-capabilities")},
		{name: "other application capability", scope: newConfirmationScope("session-app-b", "app-b-capabilities")},
		{name: "other tool protocol", scope: confirmationScope{
			SessionID: "session-app-a", CapabilityScope: "app-a-capabilities", ToolProtocolVersion: "ihtml.semantic-tools.confirmation.v999",
		}},
	}
	for _, tc := range wrongScopes {
		t.Run(tc.name, func(t *testing.T) {
			ok, err := signer.consumeContext(context.Background(), "alice", tc.scope, call)
			if err != nil || ok {
				t.Fatalf("cross-scope card accepted: ok=%v err=%v", ok, err)
			}
		})
	}

	// Invalid cross-scope attempts must not burn the issuer's nonce.
	ok, err := signer.consumeContext(context.Background(), "alice", issuedScope, call)
	if err != nil || !ok {
		t.Fatalf("issuer scope could not consume its card: ok=%v err=%v", ok, err)
	}
}

func TestConfirmationCancelConsumesNonceAndRejectsReplay(t *testing.T) {
	signer := newConfirmSigner([]byte("a shared confirmation cancellation secret"))
	call, err := signer.prepareContext(context.Background(), "alice", testConfirmationScope, ihtml.ProposedToolCall{
		Name: "ui_page_delete", Method: "TOOL", Path: "ui://ui_page_delete",
		RequiresConfirmation: true, Body: map[string]any{"name": "old"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ok, err := signer.revokeContext(context.Background(), "alice", testConfirmationScope, call)
	if err != nil || !ok {
		t.Fatalf("cancel did not revoke card: ok=%v err=%v", ok, err)
	}
	ok, err = signer.consumeContext(context.Background(), "alice", testConfirmationScope, call)
	if err != nil || ok {
		t.Fatalf("confirm replay after cancel was accepted: ok=%v err=%v", ok, err)
	}
	ok, err = signer.revokeContext(context.Background(), "alice", testConfirmationScope, call)
	if err != nil || ok {
		t.Fatalf("cancel replay was accepted: ok=%v err=%v", ok, err)
	}
}

func TestInvalidCancelDoesNotBurnConfirmation(t *testing.T) {
	signer := newConfirmSigner([]byte("a shared confirmation cancellation secret"))
	call, err := signer.prepareContext(context.Background(), "alice", testConfirmationScope, ihtml.ProposedToolCall{
		Name: "ui_page_delete", Method: "TOOL", Path: "ui://ui_page_delete",
		RequiresConfirmation: true, Body: map[string]any{"name": "old"},
	})
	if err != nil {
		t.Fatal(err)
	}
	tampered := call
	tampered.Body = map[string]any{"name": "different"}
	if ok, err := signer.revokeContext(context.Background(), "alice", testConfirmationScope, tampered); err != nil || ok {
		t.Fatalf("tampered cancel accepted: ok=%v err=%v", ok, err)
	}
	if ok, err := signer.consumeContext(context.Background(), "alice", testConfirmationScope, call); err != nil || !ok {
		t.Fatalf("invalid cancel burned valid card: ok=%v err=%v", ok, err)
	}
}

type failingConfirmNonceStore struct{}

func (failingConfirmNonceStore) Put(context.Context, string, time.Time) error {
	return errors.New("nonce database unavailable")
}

func (failingConfirmNonceStore) Consume(context.Context, string, time.Time) (bool, error) {
	return false, errors.New("nonce database unavailable")
}

func TestConfirmationStoreFailureFailsClosed(t *testing.T) {
	rt, _, _ := newTestRuntime(t)
	rt.cfg.AllowUnconfirmedItemWrites = false
	rt.signer = newConfirmSigner([]byte("secret"), failingConfirmNonceStore{})
	out, err := findTool(t, rt, "ui_item_put").InvokableRun(context.Background(), `{"items":[{"id":"x","type":"html","content":"x"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rt.timeline.pendingCalls()) != 0 {
		t.Fatal("unpersisted nonce produced a pending confirmation")
	}
	if out == "" || !strings.Contains(out, `"status":"error"`) || !strings.Contains(out, "确认卡签发失败") {
		t.Fatalf("nonce store failure did not fail closed: %s", out)
	}
}
