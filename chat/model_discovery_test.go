package chat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestModelsSkipsDiscoveryWhenModelSelectionIsDisabled(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()

	backend := New(Config{
		Provider: ProviderOpenAI, BaseURL: server.URL + "/v1", APIKey: "provider-secret",
		Model: "server-locked", AllowModelSelect: false,
	})
	models, err := backend.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 0 {
		t.Fatalf("server-locked model triggered %d discovery requests", requests.Load())
	}
	if models.CurrentModel != "server-locked" || models.ModelSelect || len(models.Models) != 0 {
		t.Fatalf("models = %#v", models)
	}
}

func TestModelDiscoveryRejectsCrossOriginRedirectBeforeCredentialLeak(t *testing.T) {
	var leaked atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "" {
			leaked.Add(1)
		}
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, target.URL+"/models", http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	backend := New(Config{
		Provider: ProviderOpenAI, BaseURL: source.URL + "/v1", APIKey: "provider-secret",
		Model: "selectable", AllowModelSelect: true,
	})
	if _, err := backend.Models(context.Background()); err != nil {
		t.Fatal(err)
	}
	if leaked.Load() != 0 {
		t.Fatal("model discovery followed a cross-origin redirect with provider credentials")
	}
}
