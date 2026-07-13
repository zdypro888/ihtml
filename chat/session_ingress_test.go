package chat

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zdypro888/ihtml"
)

type inventoryBlockCall struct {
	ctx    context.Context
	method string
}

type blockingInventoryService struct {
	ihtml.Service
	method  string
	started chan inventoryBlockCall
	release <-chan struct{}
	once    sync.Once
}

func (s *blockingInventoryService) wait(ctx context.Context, method string) {
	if s.method != method {
		return
	}
	s.once.Do(func() {
		s.started <- inventoryBlockCall{ctx: ctx, method: method}
		<-s.release // Deliberately ignore ctx; the owner barrier tracks real return.
	})
}

func (s *blockingInventoryService) ListPages(ctx context.Context, user string) ([]ihtml.Page, error) {
	s.wait(ctx, "ListPages")
	return s.Service.ListPages(context.Background(), user)
}

func (s *blockingInventoryService) ListItems(ctx context.Context, user string) ([]ihtml.Item, error) {
	s.wait(ctx, "ListItems")
	return s.Service.ListItems(context.Background(), user)
}

type countingBlockingInventoryService struct {
	ihtml.Service
	method  string
	started chan inventoryBlockCall
	release <-chan struct{}
}

func (s *countingBlockingInventoryService) wait(ctx context.Context, method string) {
	if s.method != method {
		return
	}
	s.started <- inventoryBlockCall{ctx: ctx, method: method}
	<-s.release // Deliberately ignore ctx; the global operation cap must contain it.
}

func (s *countingBlockingInventoryService) ListPages(ctx context.Context, user string) ([]ihtml.Page, error) {
	s.wait(ctx, "ListPages")
	return s.Service.ListPages(context.Background(), user)
}

func (s *countingBlockingInventoryService) ListItems(ctx context.Context, user string) ([]ihtml.Item, error) {
	s.wait(ctx, "ListItems")
	return s.Service.ListItems(context.Background(), user)
}

func ingressTestBackend(operationLimit int) *Backend {
	return New(Config{
		Provider:                 ProviderOpenAI,
		BaseURL:                  "http://model.invalid/v1",
		Model:                    "test-model",
		WritesPerMinute:          -1,
		MaxOutstandingOperations: operationLimit,
	})
}

func newIngressTestSession(t *testing.T, backend *Backend, service ihtml.Service, csid string, emit func(ihtml.ChatStreamEvent) error) *session {
	t.Helper()
	created, err := backend.NewSession(context.Background(), &ihtml.ChatSessionContext{
		User: "ingress-user", Service: service, Emit: emit,
		Request: httptest.NewRequest("GET", "http://ihtml.test/chat?csid="+csid, nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	return created.(*session)
}

func sendIngressTestMessage(s *session) time.Duration {
	started := time.Now()
	s.HandleEvent(context.Background(), &ihtml.ChatClientEvent{
		Type:     ihtml.ChatClientUserMessage,
		Messages: []ihtml.ChatMessage{{Role: "user", Content: "hello"}},
	})
	return time.Since(started)
}

func TestActiveOwnerInventoryBuildIsAsyncCancelledAndInheritedBySuccessor(t *testing.T) {
	for _, method := range []string{"ListPages", "ListItems"} {
		t.Run(method, func(t *testing.T) {
			release := make(chan struct{})
			service := &blockingInventoryService{
				Service: ihtml.New(ihtml.NewMemoryStore()), method: method,
				started: make(chan inventoryBlockCall, 1), release: release,
			}
			backend := ingressTestBackend(4)
			first := newIngressTestSession(t, backend, service, "shared-inventory", func(ihtml.ChatStreamEvent) error { return nil })
			if elapsed := sendIngressTestMessage(first); elapsed > 100*time.Millisecond {
				t.Fatalf("user_message blocked websocket reader for %s", elapsed)
			}
			call := waitLifecycleValue(t, service.started, "active owner inventory")
			if call.method != method {
				t.Fatalf("blocked method = %q, want %q", call.method, method)
			}

			secondServiceStarted := make(chan inventoryBlockCall, 1)
			secondRelease := make(chan struct{})
			secondService := &countingBlockingInventoryService{
				Service: service.Service, method: method, started: secondServiceStarted, release: secondRelease,
			}
			second := newIngressTestSession(t, backend, secondService, "shared-inventory", func(ihtml.ChatStreamEvent) error { return nil })
			if elapsed := sendIngressTestMessage(second); elapsed > 100*time.Millisecond {
				t.Fatalf("successor user_message blocked websocket reader for %s", elapsed)
			}
			select {
			case <-call.ctx.Done():
			case <-time.After(time.Second):
				t.Fatal("owner takeover did not cancel the cooperative inventory context")
			}
			select {
			case <-secondServiceStarted:
				t.Fatal("successor inventory overlapped the previous owner's ignored Service call")
			case <-time.After(100 * time.Millisecond):
			}

			close(release)
			secondCall := waitLifecycleValue(t, secondServiceStarted, "successor inventory after real return")
			second.Close()
			select {
			case <-secondCall.ctx.Done():
			case <-time.After(time.Second):
				t.Fatal("Close did not cancel the successor inventory context")
			}
			close(secondRelease)
			waitLifecycleSignal(t, first.shutdownDone, "previous owner shutdown")
			waitLifecycleSignal(t, second.shutdownDone, "successor owner shutdown")
		})
	}
}

func TestInventoryBuildsAcrossCSIDsRespectBackendOperationCapacity(t *testing.T) {
	for _, method := range []string{"ListPages", "ListItems"} {
		t.Run(method, func(t *testing.T) {
			release := make(chan struct{})
			service := &countingBlockingInventoryService{
				Service: ihtml.New(ihtml.NewMemoryStore()), method: method,
				started: make(chan inventoryBlockCall, 4), release: release,
			}
			backend := ingressTestBackend(2)
			sessions := make([]*session, 0, 3)
			for index, csid := range []string{"capacity-a", "capacity-b"} {
				s := newIngressTestSession(t, backend, service, csid, func(ihtml.ChatStreamEvent) error { return nil })
				sessions = append(sessions, s)
				if elapsed := sendIngressTestMessage(s); elapsed > 100*time.Millisecond {
					t.Fatalf("user_message %d blocked websocket reader for %s", index, elapsed)
				}
				call := waitLifecycleValue(t, service.started, "capacity-held inventory")
				if call.method != method {
					t.Fatalf("blocked method = %q, want %q", call.method, method)
				}
			}

			events := make(chan ihtml.ChatStreamEvent, 8)
			third := newIngressTestSession(t, backend, service, "capacity-c", func(event ihtml.ChatStreamEvent) error {
				events <- event
				return nil
			})
			sessions = append(sessions, third)
			if elapsed := sendIngressTestMessage(third); elapsed > 100*time.Millisecond {
				t.Fatalf("capacity rejection blocked websocket reader for %s", elapsed)
			}
			failure := waitSessionEvent(t, events, ihtml.ChatStreamTurnError)
			if !strings.Contains(failure.Error, "繁忙") {
				t.Fatalf("capacity failure = %#v", failure)
			}
			select {
			case <-service.started:
				t.Fatal("operation-saturated build entered another inventory Service call")
			case <-time.After(100 * time.Millisecond):
			}

			for _, s := range sessions {
				s.Close()
			}
			close(release)
			for _, s := range sessions {
				waitLifecycleSignal(t, s.shutdownDone, "capacity session shutdown")
			}
		})
	}
}
