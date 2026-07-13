package ihtml_test

import (
	"context"
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
