package chat

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zdypro888/ihtml"
)

var testConfirmationScope = newConfirmationScope("session-a", "capability-a")

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

func TestMemoryConfirmationNonceStoreHasHardCapacityAndReusesConsumedSlot(t *testing.T) {
	store := newMemoryConfirmNonceStore(2)
	ctx := context.Background()
	expires := time.Now().Add(time.Hour)
	if err := store.Put(ctx, "first", expires); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, "second", expires); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, "third", expires); err == nil {
		t.Fatal("memory nonce store grew past its hard capacity")
	}
	if ok, err := store.Consume(ctx, "first", time.Now()); err != nil || !ok {
		t.Fatalf("consume first nonce: ok=%v err=%v", ok, err)
	}
	if err := store.Put(ctx, "third", expires); err != nil {
		t.Fatalf("consumed capacity was not reusable: %v", err)
	}
	if len(store.nonces) != 2 {
		t.Fatalf("nonce count = %d, want 2", len(store.nonces))
	}
}

func TestMemoryConfirmationNonceStoreCleansExpiredEntriesAtCapacity(t *testing.T) {
	store := newMemoryConfirmNonceStore(2)
	now := time.Now()
	store.mu.Lock()
	store.nonces["expired"] = now.Add(-time.Second)
	store.nonces["live"] = now.Add(time.Hour)
	store.nextCleanup = now.Add(-time.Second)
	store.mu.Unlock()

	if err := store.Put(context.Background(), "replacement", now.Add(time.Hour)); err != nil {
		t.Fatalf("expired capacity was not reclaimed: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, exists := store.nonces["expired"]; exists {
		t.Fatal("expired nonce survived forced capacity cleanup")
	}
	if len(store.nonces) != 2 {
		t.Fatalf("nonce count = %d, want 2", len(store.nonces))
	}
}

func TestMemoryConfirmationNonceCleanupIsThrottled(t *testing.T) {
	store := newMemoryConfirmNonceStore(8)
	now := time.Now()
	if err := store.Put(context.Background(), "live-a", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.nonces["expired"] = now.Add(-time.Second)
	nextCleanup := store.nextCleanup
	store.mu.Unlock()

	if err := store.Put(context.Background(), "live-b", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	_, survivedThrottledPut := store.nonces["expired"]
	unchangedSchedule := store.nextCleanup.Equal(nextCleanup)
	store.nextCleanup = now.Add(-time.Second)
	store.mu.Unlock()
	if !survivedThrottledPut || !unchangedSchedule {
		t.Fatal("ordinary Put performed an unthrottled full-map cleanup")
	}

	if err := store.Put(context.Background(), "live-c", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	_, survivedScheduledPut := store.nonces["expired"]
	store.mu.Unlock()
	if survivedScheduledPut {
		t.Fatal("scheduled cleanup did not remove expired nonce")
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
