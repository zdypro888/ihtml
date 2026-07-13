package chat

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	"github.com/zdypro888/ihtml"
)

type guardedStoreCall struct {
	ctx    context.Context
	method string
}

type blockingExternalSessionStore struct {
	method  string
	started chan guardedStoreCall
	release <-chan struct{}
	once    sync.Once
}

func (s *blockingExternalSessionStore) enter(ctx context.Context, method string) {
	if s.method != method {
		return
	}
	block := false
	s.once.Do(func() { block = true })
	s.started <- guardedStoreCall{ctx: ctx, method: method}
	if block {
		<-s.release // Deliberately ignore ctx; the guard must track real return.
	}
}

func (s *blockingExternalSessionStore) LoadEvents(
	ctx context.Context,
	_ string,
	_ *adk.LoadSessionEventsRequest,
) (*adk.LoadSessionEventsResult[*schema.Message], error) {
	s.enter(ctx, "LoadEvents")
	return &adk.LoadSessionEventsResult[*schema.Message]{}, nil
}

func (s *blockingExternalSessionStore) AppendEvents(
	ctx context.Context,
	_ string,
	_ []*adk.SessionEvent[*schema.Message],
) error {
	s.enter(ctx, "AppendEvents")
	return nil
}

type panicExternalSessionStore struct{ method string }

func (s *panicExternalSessionStore) LoadEvents(
	context.Context,
	string,
	*adk.LoadSessionEventsRequest,
) (*adk.LoadSessionEventsResult[*schema.Message], error) {
	if s.method == "LoadEvents" {
		panic("session-store-super-secret")
	}
	return &adk.LoadSessionEventsResult[*schema.Message]{}, nil
}

type payloadExternalSessionStore struct {
	loadResult  *adk.LoadSessionEventsResult[*schema.Message]
	loadLimit   int
	appendCalls int
}

type panickingStorePayloadMarshaler struct{}

func (panickingStorePayloadMarshaler) MarshalJSON() ([]byte, error) {
	panic("store-payload-super-secret")
}

func (s *payloadExternalSessionStore) LoadEvents(
	_ context.Context,
	_ string,
	req *adk.LoadSessionEventsRequest,
) (*adk.LoadSessionEventsResult[*schema.Message], error) {
	if req != nil {
		s.loadLimit = req.Limit
	}
	return s.loadResult, nil
}

func (s *payloadExternalSessionStore) AppendEvents(
	context.Context,
	string,
	[]*adk.SessionEvent[*schema.Message],
) error {
	s.appendCalls++
	return nil
}

func (s *panicExternalSessionStore) AppendEvents(
	context.Context,
	string,
	[]*adk.SessionEvent[*schema.Message],
) error {
	if s.method == "AppendEvents" {
		panic("session-store-super-secret")
	}
	return nil
}

func externalStoreBackend(store adk.SessionEventStore[*schema.Message], operationLimit int) *Backend {
	return New(Config{
		SessionStore:             store,
		SessionNamespace:         "session-store-guard-tests",
		WritesPerMinute:          -1,
		MaxOutstandingOperations: operationLimit,
	})
}

func TestExternalSessionStoreBlocksOwnerUntilRealReturn(t *testing.T) {
	for _, method := range []string{"LoadEvents", "AppendEvents"} {
		t.Run(method, func(t *testing.T) {
			release := make(chan struct{})
			store := &blockingExternalSessionStore{
				method: method, started: make(chan guardedStoreCall, 16), release: release,
			}
			backend := externalStoreBackend(store, 4)
			service := ihtml.New(ihtml.NewMemoryStore())
			first := newOwnedTestSession(t, backend, service, "guarded-store-owner")
			firstAgentStarted := make(chan struct{})
			if err := first.queueTurn(context.Background(), newLifecycleTurn(&lifecycleTestAgent{
				started: firstAgentStarted, message: "first",
			})); err != nil {
				t.Fatal(err)
			}
			call := waitLifecycleValue(t, store.started, "external store "+method)

			first.Close()
			select {
			case <-call.ctx.Done():
			case <-time.After(time.Second):
				t.Fatalf("Close did not cancel %s context", method)
			}
			second := newOwnedTestSession(t, backend, service, "guarded-store-owner")
			secondAgentStarted := make(chan struct{})
			if err := second.queueTurn(context.Background(), newLifecycleTurn(&lifecycleTestAgent{
				started: secondAgentStarted, message: "second",
			})); err != nil {
				t.Fatal(err)
			}
			assertLifecycleBlocked(t, secondAgentStarted, "successor behind blocked "+method)
			if second.ownerBarrier != first.shutdownDone {
				t.Fatalf("successor barrier = %p, want blocked store owner %p", second.ownerBarrier, first.shutdownDone)
			}

			close(release)
			waitLifecycleSignal(t, first.shutdownDone, "blocked store owner shutdown")
			waitLifecycleSignal(t, secondAgentStarted, "successor after store return")
			second.Close()
			waitLifecycleSignal(t, second.shutdownDone, "store successor shutdown")
		})
	}
}

func TestExternalSessionStoreRespectsBackendCapacityAcrossCSIDs(t *testing.T) {
	for _, method := range []string{"LoadEvents", "AppendEvents"} {
		t.Run(method, func(t *testing.T) {
			release := make(chan struct{})
			store := &blockingExternalSessionStore{
				method: method, started: make(chan guardedStoreCall, 16), release: release,
			}
			backend := externalStoreBackend(store, 1)
			service := ihtml.New(ihtml.NewMemoryStore())
			first := newOwnedTestSession(t, backend, service, "guarded-store-capacity-a")
			if err := first.queueTurn(context.Background(), newLifecycleTurn(&lifecycleTestAgent{
				started: make(chan struct{}), message: "first",
			})); err != nil {
				t.Fatal(err)
			}
			_ = waitLifecycleValue(t, store.started, "capacity-held "+method)

			second := newOwnedTestSession(t, backend, service, "guarded-store-capacity-b")
			secondAgentStarted := make(chan struct{})
			if err := second.queueTurn(context.Background(), newLifecycleTurn(&lifecycleTestAgent{
				started: secondAgentStarted, message: "second",
			})); err != nil {
				t.Fatal(err)
			}
			select {
			case <-secondAgentStarted:
				t.Fatal("agent ran after external store capacity rejection")
			case <-time.After(100 * time.Millisecond):
			}
			select {
			case extra := <-store.started:
				t.Fatalf("capacity-rejected session entered external %s", extra.method)
			default:
			}

			first.Close()
			second.Close()
			close(release)
			waitLifecycleSignal(t, first.shutdownDone, "capacity holder shutdown")
			waitLifecycleSignal(t, second.shutdownDone, "capacity-rejected session shutdown")
		})
	}
}

func TestExternalSessionStorePanicsAreRecoveredAndReleaseCapacity(t *testing.T) {
	for _, method := range []string{"LoadEvents", "AppendEvents"} {
		t.Run(method, func(t *testing.T) {
			backend := externalStoreBackend(&panicExternalSessionStore{method: method}, 1)
			s := newOwnedTestSession(t, backend, ihtml.New(ihtml.NewMemoryStore()), "guarded-store-panic-"+method)
			if _, ok := s.store.(*guardedSessionStore); !ok {
				t.Fatalf("external store was not guarded: %T", s.store)
			}
			var err error
			if method == "LoadEvents" {
				_, err = s.store.LoadEvents(context.Background(), s.sessionID, nil)
			} else {
				err = s.store.AppendEvents(context.Background(), s.sessionID, nil)
			}
			if !errors.Is(err, errExternalSessionStore) {
				t.Fatalf("recovered %s error = %v", method, err)
			}
			if strings.Contains(err.Error(), "super-secret") {
				t.Fatalf("panic detail leaked through %s: %v", method, err)
			}
			s.workMu.Lock()
			active := s.activeOps
			s.workMu.Unlock()
			if active != 0 {
				t.Fatalf("active operations after %s panic = %d", method, active)
			}
			if !backend.operations.tryAcquire() {
				t.Fatalf("%s panic leaked the Backend operation slot", method)
			}
			backend.operations.release()
			s.Close()
			waitLifecycleSignal(t, s.shutdownDone, "panic store session shutdown")
		})
	}
}

func TestExternalSessionStorePayloadsAreBoundedWithoutFullMarshal(t *testing.T) {
	t.Run("append", func(t *testing.T) {
		store := &payloadExternalSessionStore{}
		backend := externalStoreBackend(store, 2)
		s := newOwnedTestSession(t, backend, ihtml.New(ihtml.NewMemoryStore()), "guarded-store-payload-append")
		events := []*adk.SessionEvent[*schema.Message]{{
			EventID: "large", Kind: adk.SessionEventMessage,
			Message: schema.UserMessage(strings.Repeat("x", maxExternalSessionStorePayloadBytes+1)),
		}}
		err := s.store.AppendEvents(context.Background(), s.sessionID, events)
		if !errors.Is(err, errExternalSessionStorePayload) || store.appendCalls != 0 {
			t.Fatalf("oversized append = err:%v calls:%d", err, store.appendCalls)
		}
		s.Close()
		waitLifecycleSignal(t, s.shutdownDone, "oversized append session shutdown")
	})

	t.Run("append custom marshaler", func(t *testing.T) {
		store := &payloadExternalSessionStore{}
		backend := externalStoreBackend(store, 2)
		s := newOwnedTestSession(t, backend, ihtml.New(ihtml.NewMemoryStore()), "guarded-store-payload-custom")
		events := []*adk.SessionEvent[*schema.Message]{{
			EventID: "custom", Kind: adk.SessionEventMessage,
			Message: &schema.Message{Role: schema.User, Extra: map[string]any{
				"custom": panickingStorePayloadMarshaler{},
			}},
		}}
		err := s.store.AppendEvents(context.Background(), s.sessionID, events)
		if !errors.Is(err, errExternalSessionStorePayload) || store.appendCalls != 0 || strings.Contains(err.Error(), "super-secret") {
			t.Fatalf("custom marshaler append = err:%v calls:%d", err, store.appendCalls)
		}
		s.Close()
		waitLifecycleSignal(t, s.shutdownDone, "custom marshaler append session shutdown")
	})

	t.Run("load", func(t *testing.T) {
		store := &payloadExternalSessionStore{loadResult: &adk.LoadSessionEventsResult[*schema.Message]{
			Events: []*adk.SessionEvent[*schema.Message]{{
				EventID: "large", Kind: adk.SessionEventMessage,
				Message: schema.AssistantMessage(strings.Repeat("x", maxExternalSessionStorePayloadBytes+1), nil),
			}},
		}}
		backend := externalStoreBackend(store, 2)
		s := newOwnedTestSession(t, backend, ihtml.New(ihtml.NewMemoryStore()), "guarded-store-payload-load")
		_, err := s.store.LoadEvents(context.Background(), s.sessionID, nil)
		if !errors.Is(err, errExternalSessionStorePayload) {
			t.Fatalf("oversized load error = %v", err)
		}
		if store.loadLimit != maxExternalSessionStoreEvents {
			t.Fatalf("external load limit = %d, want %d", store.loadLimit, maxExternalSessionStoreEvents)
		}
		s.Close()
		waitLifecycleSignal(t, s.shutdownDone, "oversized load session shutdown")
	})
}
