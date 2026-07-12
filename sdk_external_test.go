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
