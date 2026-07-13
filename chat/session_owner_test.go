package chat

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/zdypro888/ihtml"
)

func newOwnedTestSession(t *testing.T, backend *Backend, service ihtml.Service, csid string) *session {
	t.Helper()
	created, err := backend.NewSession(context.Background(), &ihtml.ChatSessionContext{
		User:    "owner-user",
		Service: service,
		Emit:    func(ihtml.ChatStreamEvent) error { return nil },
		Request: httptest.NewRequest("GET", "http://ihtml.test/chat?csid="+csid, nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	s, ok := created.(*session)
	if !ok {
		t.Fatalf("session type = %T", created)
	}
	return s
}

func waitHistoryActive(t *testing.T, history *boundedSessionStore, sessionID string, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		history.mu.Lock()
		active := 0
		if entry := history.sessions[sessionID]; entry != nil {
			active = entry.active
		}
		history.mu.Unlock()
		if active == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("history active = %d, want %d", active, want)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestBackendOwnerTakeoverWaitsForIgnoredModelAndKeepsNewestLease(t *testing.T) {
	backend := New(Config{WritesPerMinute: -1})
	service := ihtml.New(ihtml.NewMemoryStore())
	first := newOwnedTestSession(t, backend, service, "shared-owner")

	modelStarted := make(chan struct{})
	modelRelease := make(chan struct{})
	modelDone := make(chan struct{})
	model := trackChatModel(first, &blockingLifecycleModel{started: modelStarted, release: modelRelease})
	go func() {
		defer close(modelDone)
		_, _ = model.Generate(context.Background(), nil)
	}()
	waitLifecycleSignal(t, modelStarted, "old owner model")

	startedAt := time.Now()
	second := newOwnedTestSession(t, backend, service, "shared-owner")
	third := newOwnedTestSession(t, backend, service, "shared-owner")
	if elapsed := time.Since(startedAt); elapsed > 250*time.Millisecond {
		t.Fatalf("owner takeover blocked NewSession for %s", elapsed)
	}
	if !first.isClosed() || !second.isClosed() {
		t.Fatal("superseded owners were not revoked")
	}

	newStarted := make(chan struct{})
	if err := third.queueTurn(context.Background(), newLifecycleTurn(&lifecycleTestAgent{
		started: newStarted, message: "new owner",
	})); err != nil {
		t.Fatal(err)
	}
	assertLifecycleBlocked(t, newStarted, "third owner before first model return")

	// The active predecessor and newest successor own independent pins. The
	// never-activated middle successor releases its pin immediately instead of
	// retaining an inherited-barrier shutdown goroutine.
	first.Close()
	second.Close()
	backend.ownerMu.Lock()
	owner := backend.owners[third.sessionID]
	backend.ownerMu.Unlock()
	if owner != third {
		t.Fatalf("current owner = %p, want newest %p", owner, third)
	}
	waitHistoryActive(t, backend.history, third.sessionID, 2)

	close(modelRelease)
	waitLifecycleSignal(t, modelDone, "old owner model return")
	waitLifecycleSignal(t, newStarted, "new owner after inherited shutdown barrier")
	waitHistoryActive(t, backend.history, third.sessionID, 1)

	backend.ownerMu.Lock()
	owner = backend.owners[third.sessionID]
	backend.ownerMu.Unlock()
	if owner != third {
		t.Fatalf("old Close released newest lease: owner=%p want=%p", owner, third)
	}
	third.Close()
	waitLifecycleSignal(t, third.shutdownDone, "newest owner shutdown")
	waitHistoryActive(t, backend.history, third.sessionID, 0)
}

func TestRapidPendingOwnerTakeoversFlattenToOriginalBarrier(t *testing.T) {
	backend := New(Config{WritesPerMinute: -1})
	service := ihtml.New(ihtml.NewMemoryStore())
	first := newOwnedTestSession(t, backend, service, "flattened-owner")

	modelStarted := make(chan struct{})
	modelRelease := make(chan struct{})
	modelDone := make(chan struct{})
	go func() {
		defer close(modelDone)
		_, _ = trackChatModel(first, &blockingLifecycleModel{
			started: modelStarted, release: modelRelease,
		}).Generate(context.Background(), nil)
	}()
	waitLifecycleSignal(t, modelStarted, "original owner model")

	const takeoverCount = 128
	successors := make([]*session, 0, takeoverCount)
	for index := 0; index < takeoverCount; index++ {
		successor := newOwnedTestSession(t, backend, service, "flattened-owner")
		successors = append(successors, successor)
		if successor.ownerBarrier != first.shutdownDone {
			t.Fatalf("successor %d inherited %p, want original barrier %p", index, successor.ownerBarrier, first.shutdownDone)
		}
	}
	latest := successors[len(successors)-1]
	for index, superseded := range successors[:len(successors)-1] {
		select {
		case <-superseded.shutdownDone:
		case <-time.After(2 * time.Second):
			t.Fatalf("never-activated successor %d retained a shutdown waiter", index)
		}
	}
	backend.ownerMu.Lock()
	owner := backend.owners[first.sessionID]
	barrier := backend.ownerBarriers[first.sessionID]
	backend.ownerMu.Unlock()
	if owner != latest || barrier != first.shutdownDone {
		t.Fatalf("flattened lease = owner:%p barrier:%p, want %p/%p", owner, barrier, latest, first.shutdownDone)
	}
	waitHistoryActive(t, backend.history, first.sessionID, 2)

	latestStarted := make(chan struct{})
	if err := latest.queueTurn(context.Background(), newLifecycleTurn(&lifecycleTestAgent{
		started: latestStarted, message: "latest",
	})); err != nil {
		t.Fatal(err)
	}
	assertLifecycleBlocked(t, latestStarted, "latest owner before original return")

	close(modelRelease)
	waitLifecycleSignal(t, modelDone, "original owner return")
	waitLifecycleSignal(t, latestStarted, "latest owner after original return")
	latest.Close()
	waitLifecycleSignal(t, latest.shutdownDone, "latest owner shutdown")
}

type ownerBlockingKVService struct {
	ihtml.Service
	started      chan string
	firstRelease <-chan struct{}
	mu           sync.Mutex
	calls        int
}

func (s *ownerBlockingKVService) KVSet(_ context.Context, user, key string, value json.RawMessage) error {
	s.mu.Lock()
	index := s.calls
	s.calls++
	s.mu.Unlock()
	s.started <- key
	if index == 0 {
		<-s.firstRelease // Deliberately ignore ctx for the superseded owner.
	}
	return s.Service.KVSet(context.Background(), user, key, value)
}

func prepareOwnerKVConfirmation(t *testing.T, s *session, id, key string) ihtml.ProposedToolCall {
	t.Helper()
	call, err := s.backend.signer.prepareContext(
		context.Background(), s.user, newConfirmationScope(s.sessionID, s.capabilityScope),
		ihtml.ProposedToolCall{
			ID: id, Name: "ui_kv_set", Title: "写入 KV", Risk: "medium", RequiresConfirmation: true,
			Body: map[string]any{"key": key, "value": key + "-value"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return call
}

func TestBackendOwnerBlocksNewConfirmationUntilIgnoredServiceReturns(t *testing.T) {
	release := make(chan struct{})
	base := ihtml.New(ihtml.NewMemoryStore())
	service := &ownerBlockingKVService{Service: base, started: make(chan string, 2), firstRelease: release}
	backend := New(Config{WritesPerMinute: -1})
	first := newOwnedTestSession(t, backend, service, "shared-confirm")
	firstCall := prepareOwnerKVConfirmation(t, first, "owner-confirm-1", "first")
	first.startConfirm(context.Background(), &ihtml.ChatClientEvent{Type: "confirm_tool", ToolCall: &firstCall})
	if key := waitLifecycleValue(t, service.started, "old owner service"); key != "first" {
		t.Fatalf("first key = %q", key)
	}

	second := newOwnedTestSession(t, backend, service, "shared-confirm")
	secondCall := prepareOwnerKVConfirmation(t, second, "owner-confirm-2", "second")
	startedAt := time.Now()
	second.startConfirm(context.Background(), &ihtml.ChatClientEvent{Type: "confirm_tool", ToolCall: &secondCall})
	if elapsed := time.Since(startedAt); elapsed > 250*time.Millisecond {
		t.Fatalf("queued confirmation blocked event handler for %s", elapsed)
	}
	select {
	case key := <-service.started:
		t.Fatalf("new owner confirmation %q overlapped ignored old Service", key)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	if key := waitLifecycleValue(t, service.started, "new owner service"); key != "second" {
		t.Fatalf("second key = %q", key)
	}
	second.workMu.Lock()
	done := second.manualDone
	second.workMu.Unlock()
	if done != nil {
		waitLifecycleSignal(t, done, "new owner confirmation completion")
	}
	for _, key := range []string{"first", "second"} {
		value, err := base.KVGet(context.Background(), "owner-user", key)
		if err != nil || string(value) != `"`+key+`-value"` {
			t.Fatalf("%s value = %s, %v", key, value, err)
		}
	}
	second.Close()
	waitLifecycleSignal(t, second.shutdownDone, "second owner shutdown")
}

func TestBackendOwnerDefersEntireTurnBuildWithoutBlockingEventReader(t *testing.T) {
	interceptorStarted := make(chan struct{})
	var interceptorOnce sync.Once
	backend := New(Config{
		Provider: ProviderOpenAI, BaseURL: "http://model.invalid/v1", WritesPerMinute: -1,
		TurnInterceptor: func(context.Context, string) error {
			interceptorOnce.Do(func() { close(interceptorStarted) })
			return nil
		},
	})
	service := ihtml.New(ihtml.NewMemoryStore())
	first := newOwnedTestSession(t, backend, service, "deferred-build")
	modelStarted := make(chan struct{})
	modelRelease := make(chan struct{})
	model := trackChatModel(first, &blockingLifecycleModel{started: modelStarted, release: modelRelease})
	modelDone := make(chan struct{})
	go func() {
		defer close(modelDone)
		_, _ = model.Generate(context.Background(), nil)
	}()
	waitLifecycleSignal(t, modelStarted, "old owner before deferred build")

	second := newOwnedTestSession(t, backend, service, "deferred-build")
	startedAt := time.Now()
	second.HandleEvent(context.Background(), &ihtml.ChatClientEvent{
		Type: "user_message", Messages: []ihtml.ChatMessage{{Role: "user", Content: "hello"}},
	})
	if elapsed := time.Since(startedAt); elapsed > 250*time.Millisecond {
		t.Fatalf("deferred user frame blocked event reader for %s", elapsed)
	}
	assertLifecycleBlocked(t, interceptorStarted, "new owner TurnInterceptor")

	close(modelRelease)
	waitLifecycleSignal(t, modelDone, "old owner model completion")
	waitLifecycleSignal(t, interceptorStarted, "deferred TurnInterceptor after takeover")
	second.Close()
	waitLifecycleSignal(t, second.shutdownDone, "deferred owner shutdown")
}
