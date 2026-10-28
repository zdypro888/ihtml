package confirmation

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zdypro888/ihtml"
)

func testSigner(t *testing.T, store NonceStore) *Signer {
	t.Helper()
	s, err := NewChecked(Config{Secret: []byte("secret"), NonceStore: store})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func testCard(t *testing.T, s *Signer) ihtml.ProposedToolCall {
	t.Helper()
	c, err := s.Prepare(context.Background(), "alice", NewScope("session", "capability"), ihtml.ProposedToolCall{
		ID: "call", Name: "write", Method: "TOOL", Path: "ui://write", RequiresConfirmation: true,
		AgentMode: "deep", ApprovalResume: "manual_tool_only", Body: map[string]any{"nested": []any{"original"}},
		Query: map[string]any{"q": []any{"original"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestLegacyCanonicalSignature(t *testing.T) {
	s := testSigner(t, nil)
	c := ihtml.ProposedToolCall{ID: "id", Name: "n", Title: "t", Method: "TOOL", Path: "ui://n",
		Query: map[string]any{"z": 2, "a": 1}, Body: map[string]any{"b": "x"}, Risk: "high",
		RequiresConfirmation: true, Reason: "r", AgentMode: "deep", ApprovalResume: "manual_tool_only",
		ConfirmationNonce: "nonce", ConfirmationExpires: 123}
	const payload = `{"scope":{"session_id":"session","capability_scope":"capability","tool_protocol_version":"ihtml.semantic-tools.confirmation.v2"},"id":"id","user":"alice","name":"n","title":"t","method":"TOOL","path":"ui://n","query":{"a":1,"z":2},"body":{"b":"x"},"risk":"high","confirmation":true,"reason":"r","agent_mode":"deep","resume_mode":"manual_tool_only","nonce":"nonce","expires":123}`
	key := sha256.Sum256([]byte("secret"))
	mac := hmac.New(sha256.New, key[:])
	mac.Write([]byte(payload))
	if got := s.Sign("alice", NewScope("session", "capability"), c); got != hex.EncodeToString(mac.Sum(nil)) {
		t.Fatal("legacy signature changed", got)
	}
}

func TestBindingTamperExpiryAndReplay(t *testing.T) {
	s := testSigner(t, nil)
	c := testCard(t, s)
	ctx := context.Background()
	scope := NewScope("session", "capability")
	for _, bad := range []Scope{NewScope("other", "capability"), NewScope("session", "other"), {"session", "capability", "v999"}} {
		if ok, err := s.Revoke(ctx, "alice", bad, c); ok || err != nil {
			t.Fatal(ok, err)
		}
	}
	if ok, err := s.Consume(ctx, "bob", scope, c); ok || err != nil {
		t.Fatal(ok, err)
	}
	mutators := []func(*ihtml.ProposedToolCall){
		func(c *ihtml.ProposedToolCall) { c.ID += "x" }, func(c *ihtml.ProposedToolCall) { c.Name += "x" },
		func(c *ihtml.ProposedToolCall) { c.Title += "x" }, func(c *ihtml.ProposedToolCall) { c.Method += "x" },
		func(c *ihtml.ProposedToolCall) { c.Path += "x" }, func(c *ihtml.ProposedToolCall) { c.Query = map[string]any{"x": 1} },
		func(c *ihtml.ProposedToolCall) { c.Body = "x" }, func(c *ihtml.ProposedToolCall) { c.Risk += "x" },
		func(c *ihtml.ProposedToolCall) { c.RequiresConfirmation = false }, func(c *ihtml.ProposedToolCall) { c.Reason += "x" },
		func(c *ihtml.ProposedToolCall) { c.AgentMode += "x" }, func(c *ihtml.ProposedToolCall) { c.ApprovalResume += "x" },
		func(c *ihtml.ProposedToolCall) { c.ConfirmationNonce += "x" }, func(c *ihtml.ProposedToolCall) { c.ConfirmationExpires++ },
		func(c *ihtml.ProposedToolCall) { c.ConfirmationToken += "x" },
	}
	for i, mutate := range mutators {
		bad := c
		mutate(&bad)
		if ok, err := s.Consume(ctx, "alice", scope, bad); ok || err != nil {
			t.Fatalf("tamper %d: %v %v", i, ok, err)
		}
	}
	expired := c
	expired.ConfirmationExpires = time.Now().Unix()
	expired.ConfirmationToken = s.Sign("alice", scope, expired)
	if ok, err := s.Consume(ctx, "alice", scope, expired); ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, err := s.Consume(ctx, "alice", scope, c); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, err := s.Revoke(ctx, "alice", scope, c); ok || err != nil {
		t.Fatal(ok, err)
	}
}

func TestConcurrentConfirmRevokeSharedRestart(t *testing.T) {
	store := NewMemoryNonceStore()
	issuer := testSigner(t, store)
	other := testSigner(t, store)
	c := testCard(t, issuer)
	var winners atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var ok bool
			var err error
			if i%2 == 0 {
				ok, err = other.Consume(context.Background(), "alice", NewScope("session", "capability"), c)
			} else {
				ok, err = issuer.Revoke(context.Background(), "alice", NewScope("session", "capability"), c)
			}
			if err != nil {
				t.Error(err)
			}
			if ok {
				winners.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatal(winners.Load())
	}
}

func TestSnapshotOwnership(t *testing.T) {
	secret := []byte("secret")
	s, _ := NewChecked(Config{Secret: secret})
	secret[0] = 'X'
	body := map[string]any{"list": []any{map[string]any{"n": "before"}}}
	query := map[string]any{"list": []any{"before"}}
	c, err := s.Prepare(context.Background(), "alice", NewScope("session", "capability"), ihtml.ProposedToolCall{Body: body, Query: query})
	if err != nil {
		t.Fatal(err)
	}
	body["list"].([]any)[0].(map[string]any)["n"] = "after"
	query["list"].([]any)[0] = "after"
	if c.Body.(map[string]any)["list"].([]any)[0].(map[string]any)["n"] != "before" {
		t.Fatal("body aliased")
	}
	c.Query["list"].([]any)[0] = "card changed"
	if query["list"].([]any)[0] != "after" {
		t.Fatal("query aliased")
	}
	c.Query["list"].([]any)[0] = "before"
	if ok, err := testSigner(t, s.nonces).Consume(context.Background(), "alice", NewScope("session", "capability"), c); !ok || err != nil {
		t.Fatal(ok, err)
	}
}

type callbackStore struct {
	put     func(context.Context) error
	consume func(context.Context) (bool, error)
}

type testContextKey struct{}

func (s *callbackStore) Put(ctx context.Context, _ string, _ time.Time) error { return s.put(ctx) }
func (s *callbackStore) Consume(ctx context.Context, _ string, _ time.Time) (bool, error) {
	return s.consume(ctx)
}

func TestDependenciesContextAndStoreErrors(t *testing.T) {
	var typedNil *callbackStore
	if _, err := NewChecked(Config{NonceStore: typedNil}); err == nil {
		t.Fatal("typed nil accepted")
	}
	if _, err := NewChecked(Config{MaxMemoryNonces: -1}); err == nil {
		t.Fatal("negative limit")
	}
	if _, err := NewChecked(Config{NonceStore: NewMemoryNonceStore(), MaxMemoryNonces: 1}); err == nil {
		t.Fatal("ignored limit")
	}
	s := testSigner(t, nil)
	c := testCard(t, s)
	scope := NewScope("session", "capability")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Prepare(ctx, "alice", scope, c); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := s.Revoke(ctx, "alice", scope, c); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := (*Signer)(nil).Consume(context.Background(), "alice", scope, c); err == nil {
		t.Fatal("nil signer")
	}
	//lint:ignore SA1012 deliberately verify rejection at the public boundary
	if _, err := s.Prepare(nil, "alice", scope, c); err == nil {
		t.Fatal("nil context")
	}
	if _, err := s.Prepare(context.Background(), "", scope, c); err == nil {
		t.Fatal("empty user")
	}
	if _, err := s.Prepare(context.Background(), "alice", Scope{}, c); err == nil {
		t.Fatal("invalid scope")
	}
	if out, err := s.Prepare(context.Background(), "alice", scope, ihtml.ProposedToolCall{Body: make(chan int)}); err == nil || out.ConfirmationToken != "" {
		t.Fatal("invalid body signed")
	}
	errDB := errors.New("database unavailable")
	ctx = context.WithValue(context.Background(), testContextKey{}, "binding")
	store := &callbackStore{put: func(got context.Context) error {
		if got != ctx {
			t.Fatal("context replaced")
		}
		return errDB
	}, consume: func(got context.Context) (bool, error) {
		if got != ctx {
			t.Fatal("context replaced")
		}
		return true, errDB
	}}
	s = testSigner(t, store)
	if out, err := s.Prepare(ctx, "alice", scope, c); !errors.Is(err, errDB) || out.ConfirmationToken != "" {
		t.Fatal(out, err)
	}
	if ok, err := s.Revoke(ctx, "alice", scope, c); ok || !errors.Is(err, errDB) {
		t.Fatal(ok, err)
	}
	store.consume = func(context.Context) (bool, error) { panic("secret") }
	if ok, err := s.Consume(ctx, "alice", scope, c); ok || err == nil {
		t.Fatal(ok, err)
	}
	store.put = func(context.Context) error { panic("secret") }
	if out, err := s.Prepare(ctx, "alice", scope, c); err == nil || out.ConfirmationToken != "" {
		t.Fatal(out, err)
	}
}

func TestMemoryCapacityCleanupDuplicateAndCancellation(t *testing.T) {
	s := NewMemoryNonceStore(2)
	ctx := context.Background()
	now := time.Now()
	if err := s.Put(ctx, "one", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, "one", now.Add(time.Hour)); err == nil {
		t.Fatal("duplicate accepted")
	}
	s.nonces["expired"] = now.Add(-time.Second)
	if err := s.Put(ctx, "two", now.Add(time.Hour)); err == nil {
		t.Fatal("cleanup not throttled")
	}
	s.nextCleanup = now.Add(-time.Second)
	if err := s.Put(ctx, "two", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, "three", now.Add(time.Hour)); err == nil {
		t.Fatal("unbounded capacity")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if ok, err := s.Consume(cancelled, "one", now); ok || !errors.Is(err, context.Canceled) {
		t.Fatal(ok, err)
	}
	if ok, err := s.Consume(ctx, "one", now); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if err := s.Put(ctx, "three", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Consume(ctx, "three", now.Add(2*time.Hour)); ok || err != nil {
		t.Fatal(ok, err)
	}
	if err := s.Put(ctx, "", now.Add(time.Hour)); err == nil {
		t.Fatal("empty nonce")
	}
	if err := s.Put(ctx, "old", now.Add(-time.Second)); err == nil {
		t.Fatal("expired put")
	}
}

func TestMemoryNonceCleanupIsThrottled(t *testing.T) {
	s := NewMemoryNonceStore(8)
	now := time.Now()
	if err := s.Put(context.Background(), "live-a", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.nonces["expired"] = now.Add(-time.Second)
	nextCleanup := s.nextCleanup
	s.mu.Unlock()
	if err := s.Put(context.Background(), "live-b", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	_, survived := s.nonces["expired"]
	unchanged := s.nextCleanup.Equal(nextCleanup)
	s.nextCleanup = now.Add(-time.Second)
	s.mu.Unlock()
	if !survived || !unchanged {
		t.Fatal("ordinary Put performed an unthrottled cleanup")
	}
	if err := s.Put(context.Background(), "live-c", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	_, survived = s.nonces["expired"]
	s.mu.Unlock()
	if survived {
		t.Fatal("scheduled cleanup did not remove expired nonce")
	}
}

func TestPresentationSignedAndSnapshotOwned(t *testing.T) {
	s := testSigner(t, nil)
	ctx := context.Background()
	scope := NewScope("session", "capability")
	view := &ihtml.ToolCallPresentation{ConfirmText: "Approve", CancelText: "Leave", ReasonLabel: "Effect", Facts: []ihtml.ToolCallFact{{Label: "Targets", Value: "alice"}}}
	card, err := s.Prepare(ctx, "alice", scope, ihtml.ProposedToolCall{Presentation: view})
	if err != nil {
		t.Fatal(err)
	}
	view.ConfirmText = "mutated"
	view.Facts[0].Value = "mutated"
	if card.Presentation.Facts[0].Value != "alice" {
		t.Fatal("presentation facts alias input")
	}
	if card.Presentation.ConfirmText != "Approve" {
		t.Fatal("presentation aliases input")
	}
	for _, mutate := range []func(*ihtml.ToolCallPresentation){
		func(p *ihtml.ToolCallPresentation) { p.ConfirmText = "other" },
		func(p *ihtml.ToolCallPresentation) { p.Facts = []ihtml.ToolCallFact{{Label: "Targets", Value: "bob"}} },
		func(p *ihtml.ToolCallPresentation) { p.CancelText = "other" },
		func(p *ihtml.ToolCallPresentation) { p.ReasonLabel = "other" },
	} {
		changed := card
		copied := *card.Presentation
		changed.Presentation = &copied
		mutate(changed.Presentation)
		if ok, err := s.Consume(ctx, "alice", scope, changed); ok || err != nil {
			t.Fatal("changed presentation accepted", err)
		}
	}
	changed := card
	changed.Presentation = nil
	if ok, err := s.Consume(ctx, "alice", scope, changed); ok || err != nil {
		t.Fatal("removed presentation accepted", err)
	}
	if ok, err := s.Consume(ctx, "alice", scope, card); !ok || err != nil {
		t.Fatal("valid presentation rejected", err)
	}
}

func TestBusinessDeadlineCapsSignatureAndNonce(t *testing.T) {
	s := testSigner(t, nil)
	scope := NewScope("session", "capability")
	for _, delta := range []time.Duration{time.Minute, time.Hour, -time.Minute} {
		deadline := time.Now().Add(delta)
		card, err := s.PrepareUntil(context.Background(), "alice", scope, ihtml.ProposedToolCall{ID: "deadline", Name: "write", Method: "POST", Path: "/record", RequiresConfirmation: true}, deadline)
		if delta < 0 {
			if err == nil || card.ConfirmationToken != "" {
				t.Fatal("expired deadline signed")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if card.ConfirmationExpires > deadline.Unix() || card.ConfirmationExpires > time.Now().Add(TTL).Unix() {
			t.Fatal("business/default deadline extended", card)
		}
		if delta == time.Minute && card.ConfirmationExpires != deadline.Unix() {
			t.Fatal("business deadline discarded")
		}
		tampered := card
		tampered.ConfirmationExpires++
		if ok, _ := s.Consume(context.Background(), "alice", scope, tampered); ok {
			t.Fatal("deadline extension accepted")
		}
		if ok, err := s.Consume(context.Background(), "alice", scope, card); !ok || err != nil {
			t.Fatal(ok, err)
		}
	}
}
