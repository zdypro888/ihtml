package chat

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zdypro888/ihtml"
)

func TestConfirmationSharedNonceStoreSurvivesSignerRestart(t *testing.T) {
	shared := newMemoryConfirmNonceStore()
	secret := []byte("a sufficiently long production secret")
	issuer := newConfirmSigner(secret, shared)
	consumerAfterRestart := newConfirmSigner(secret, shared)
	call, err := issuer.prepareContext(context.Background(), "alice", ihtml.ProposedToolCall{
		Name: "ui_item_put", Method: "TOOL", Path: "ui://ui_item_put",
		Body: map[string]any{"items": []any{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ok, err := consumerAfterRestart.consumeContext(context.Background(), "alice", call)
	if err != nil || !ok {
		t.Fatalf("shared confirmation was not accepted after restart: ok=%v err=%v", ok, err)
	}
	ok, err = issuer.consumeContext(context.Background(), "alice", call)
	if err != nil || ok {
		t.Fatalf("confirmation replay accepted: ok=%v err=%v", ok, err)
	}
}

func TestConfirmationDifferentSecretAndExpiredCardRejected(t *testing.T) {
	shared := newMemoryConfirmNonceStore()
	issuer := newConfirmSigner([]byte("secret-one"), shared)
	call, err := issuer.prepareContext(context.Background(), "alice", ihtml.ProposedToolCall{
		Name: "ui_theme_set", Method: "TOOL", Path: "ui://ui_theme_set", Body: map[string]any{"mode": "dark"},
	})
	if err != nil {
		t.Fatal(err)
	}
	other := newConfirmSigner([]byte("secret-two"), shared)
	if ok, err := other.consumeContext(context.Background(), "alice", call); err != nil || ok {
		t.Fatalf("different key accepted: ok=%v err=%v", ok, err)
	}
	call.ConfirmationExpires = time.Now().Unix()
	call.ConfirmationToken = issuer.sign("alice", call)
	if ok, err := issuer.consumeContext(context.Background(), "alice", call); err != nil || ok {
		t.Fatalf("expired card accepted: ok=%v err=%v", ok, err)
	}
}

func TestConfirmationSignsAgentAndResumeMode(t *testing.T) {
	signer := newConfirmSigner([]byte("secret"))
	call, err := signer.prepareContext(context.Background(), "alice", ihtml.ProposedToolCall{
		Name: "ui_item_put", Method: "TOOL", Path: "ui://ui_item_put",
		AgentMode: AgentModeDeep, ApprovalResume: "manual_tool_only",
		RequiresConfirmation: true, Body: map[string]any{"items": []any{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tampered := call
	tampered.AgentMode = AgentModeFast
	if ok, err := signer.consumeContext(context.Background(), "alice", tampered); err != nil || ok {
		t.Fatalf("tampered agent mode accepted: ok=%v err=%v", ok, err)
	}
	tampered = call
	tampered.ApprovalResume = "checkpoint"
	if ok, err := signer.consumeContext(context.Background(), "alice", tampered); err != nil || ok {
		t.Fatalf("tampered resume capability accepted: ok=%v err=%v", ok, err)
	}
	if ok, err := signer.consumeContext(context.Background(), "alice", call); err != nil || !ok {
		t.Fatalf("original deep confirmation rejected: ok=%v err=%v", ok, err)
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
