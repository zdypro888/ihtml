package ihtml_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zdypro888/ihtml"
)

type nilMetrics struct{}

func (*nilMetrics) Add(string, float64)     {}
func (*nilMetrics) Observe(string, float64) {}

type nilBroadcaster struct{}

func (*nilBroadcaster) Publish(string, ihtml.Event) {}
func (*nilBroadcaster) Subscribe(string) (<-chan ihtml.Event, func()) {
	return nil, func() {}
}

type nilChatBackend struct{}

func (*nilChatBackend) NewSession(context.Context, *ihtml.ChatSessionContext) (ihtml.ChatSession, error) {
	return nil, nil
}

// legacyOnlyService models a third-party implementation compiled against the
// original Service contract. Embedding the legacy interface intentionally
// hides optional capabilities implemented by the concrete value it wraps.
type legacyOnlyService struct {
	ihtml.Service
}

func ExampleNewHandler() {
	handler, err := ihtml.NewHandler(
		ihtml.NewMemoryStore(),
		ihtml.WithUserResolver(func(*http.Request) (string, error) { return "tenant-user", nil }),
		ihtml.WithPageTitle("Embedded workspace"),
	)
	if err != nil {
		panic(err)
	}
	defer handler.Close()

	request := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	fmt.Println(response.Code)
	// Output: 200
}

func TestNewHandlerReturnsTypedConfigurationErrors(t *testing.T) {
	if _, err := ihtml.NewHandler(nil); !errors.Is(err, ihtml.ErrInvalidHandlerConfig) {
		t.Fatalf("nil store error = %v", err)
	}
	if _, err := ihtml.NewHandler(ihtml.NewMemoryStore(), nil); !errors.Is(err, ihtml.ErrInvalidHandlerConfig) {
		t.Fatalf("nil option error = %v", err)
	}
	if _, err := ihtml.NewHandler(ihtml.NewMemoryStore(), ihtml.WithMaxRevisions(-1)); !errors.Is(err, ihtml.ErrInvalidHandlerConfig) {
		t.Fatalf("invalid revision error = %v", err)
	}
}

func TestNewHandlerRejectsTypedNilInterfaceDependencies(t *testing.T) {
	var nilStore *ihtml.MemoryStore
	var nilChat *nilChatBackend
	var nilMetricSink *nilMetrics
	var nilEvents *nilBroadcaster

	tests := []struct {
		name  string
		field string
		new   func() (*ihtml.Handler, error)
	}{
		{name: "store", field: "Store", new: func() (*ihtml.Handler, error) { return ihtml.NewHandler(nilStore) }},
		{name: "chat", field: "ChatBackend", new: func() (*ihtml.Handler, error) {
			return ihtml.NewHandler(ihtml.NewMemoryStore(), ihtml.WithChatBackend(nilChat))
		}},
		{name: "metrics", field: "Metrics", new: func() (*ihtml.Handler, error) {
			return ihtml.NewHandler(ihtml.NewMemoryStore(), ihtml.WithMetrics(nilMetricSink))
		}},
		{name: "broadcaster", field: "Broadcaster", new: func() (*ihtml.Handler, error) {
			return ihtml.NewHandler(ihtml.NewMemoryStore(), ihtml.WithBroadcaster(nilEvents))
		}},
		{name: "ordered broadcaster", field: "Broadcaster", new: func() (*ihtml.Handler, error) {
			return ihtml.NewHandler(ihtml.NewMemoryStore(), ihtml.WithOrderedBroadcaster(nilEvents))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, err := tt.new()
			if h != nil || !errors.Is(err, ihtml.ErrInvalidHandlerConfig) {
				t.Fatalf("handler=%v error=%v", h, err)
			}
			var configErr *ihtml.HandlerConfigError
			if !errors.As(err, &configErr) || configErr.Field != tt.field {
				t.Fatalf("config error=%#v, want field %q", configErr, tt.field)
			}
		})
	}
}

func TestScopeServiceRejectsTypedNilService(t *testing.T) {
	var service *ihtml.Handler
	scoped, err := ihtml.ScopeService(service, "alice")
	if scoped != nil || !errors.Is(err, ihtml.ErrInvalidServiceScope) {
		t.Fatalf("scoped=%v error=%v", scoped, err)
	}
}

func TestScopeServiceDoesNotInventVersionedKVCapability(t *testing.T) {
	handler, err := ihtml.NewHandler(ihtml.NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()

	var service ihtml.Service = &legacyOnlyService{Service: handler}
	scoped, err := ihtml.ScopeService(service, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := scoped.(ihtml.ScopedVersionedKVService); ok {
		t.Fatal("legacy-only Service unexpectedly exposes the optional versioned KV capability")
	}
	if _, ok := scoped.(ihtml.ScopedPagePublicationService); ok {
		t.Fatal("legacy-only Service unexpectedly exposes the optional page publication capability")
	}
	if _, err := scoped.ListItems(context.Background()); err != nil {
		t.Fatalf("legacy scoped service read failed: %v", err)
	}
}

func TestReservedKVContractIsTypedForSDKCallers(t *testing.T) {
	handler, err := ihtml.NewHandler(ihtml.NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()

	key := ihtml.ReservedKVPrefix + ".host-test"
	if _, err := handler.KVGet(context.Background(), "alice", key); !errors.Is(err, ihtml.ErrReservedKVKey) {
		t.Fatalf("reserved KV error = %v", err)
	}
}

func TestServiceForRequestBindsUserAndEnforcesPolicy(t *testing.T) {
	handler, err := ihtml.NewHandler(
		ihtml.NewMemoryStore(),
		ihtml.WithUserResolver(func(*http.Request) (string, error) { return "alice", nil }),
		ihtml.WithAccessPolicy(func(*http.Request, string) ihtml.Access { return ihtml.Access{ReadOnly: true} }),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()

	request := httptest.NewRequest(http.MethodGet, "/host-extension", nil)
	scoped, err := handler.ServiceForRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scoped.ListItems(context.Background()); err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if err := scoped.PutItems(context.Background(), []ihtml.Item{{ID: "card", Type: ihtml.ItemHTML, Content: "<div/>"}}, "host", "test"); !errors.Is(err, ihtml.ErrAccessDenied) {
		t.Fatalf("read-only write error = %v", err)
	}
	versioned, ok := scoped.(ihtml.ScopedVersionedKVService)
	if !ok {
		t.Fatal("request-scoped Handler must expose the additive versioned KV capability")
	}
	if _, _, err := versioned.KVGetWithRevision(context.Background(), "layout"); !errors.Is(err, ihtml.ErrNotFound) {
		t.Fatalf("versioned KV read error = %v", err)
	}
	if _, err := versioned.KVCompareAndSet(context.Background(), "layout", json.RawMessage(`{}`), ihtml.MissingKVRevision); !errors.Is(err, ihtml.ErrAccessDenied) {
		t.Fatalf("read-only conditional KV write error = %v", err)
	}
	publisher, ok := scoped.(ihtml.ScopedPagePublicationService)
	if !ok {
		t.Fatal("request-scoped Handler must expose the additive page publication capability")
	}
	if err := publisher.PublishPage(context.Background(), ihtml.Page{Name: "report", Title: "Report"}, []ihtml.Item{
		{ID: "body", Type: ihtml.ItemHTML, Page: "report", Content: "ready"},
	}, "host", "test"); !errors.Is(err, ihtml.ErrAccessDenied) {
		t.Fatalf("read-only page publication error = %v", err)
	}
}

func TestAccessPolicyInfrastructureErrorsFailClosed(t *testing.T) {
	handler, err := ihtml.NewHandler(
		ihtml.NewMemoryStore(),
		ihtml.WithUserResolver(func(*http.Request) (string, error) { return "alice", nil }),
		ihtml.WithAccessPolicyE(func(*http.Request, string) (ihtml.Access, error) {
			return ihtml.Access{}, errors.New("authorization database unavailable")
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()

	request := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}

	scoped, err := handler.ServiceForRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scoped.ListItems(context.Background()); !errors.Is(err, ihtml.ErrAccessDenied) {
		t.Fatalf("scoped service fail-closed error = %v", err)
	}
}
