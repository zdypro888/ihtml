package einoengine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/schema"
	"github.com/zdypro888/ihtml/agent"
)

func TestConfigRejectsImplicitCredentialsAndUnsafeEndpoints(t *testing.T) {
	base := Config{Provider: ProviderOpenAI, APIKey: "explicit", Model: "model"}
	for _, mutate := range []func(*Config){
		func(c *Config) { c.APIKey = "" },
		func(c *Config) { c.Model = "" },
		func(c *Config) { c.Provider = "unknown" },
		func(c *Config) { c.OpenAIAPI = "unknown" },
		func(c *Config) { c.Provider = ProviderClaude; c.OpenAIAPI = "responses" },
		func(c *Config) { c.BaseURL = "https://user:secret@example.com" },
		func(c *Config) { c.BaseURL = "file:///tmp/model" },
		func(c *Config) { c.BaseURL = "https://example.com?token=secret" },
		func(c *Config) { c.BaseURL = "https://example.com#secret" },
	} {
		cfg := base
		mutate(&cfg)
		if _, err := New(context.Background(), cfg); err == nil {
			t.Fatal("accepted unsafe configuration")
		}
	}
}

func TestDefaultTransportDoesNotForwardRedirectCredentials(t *testing.T) {
	var reached atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Store(true)
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	req, _ := http.NewRequest(http.MethodGet, origin.URL, nil)
	req.Header.Set("Authorization", "Bearer explicit")
	response, err := providerHTTPClient(Config{}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusTemporaryRedirect || reached.Load() {
		t.Fatal("followed provider redirect")
	}
}

func TestHostSessionNamespaceAndValidator(t *testing.T) {
	e := &Engine{cfg: Config{SessionNamespace: "orders:v1"}, runtime: session.NewInMemoryStore[*schema.Message](nil)}
	req := &agent.TurnRequest{SessionID: "tenant/alice/opaque", SessionCapability: "reader"}
	id := e.engineSessionID(req)
	if !strings.HasPrefix(id, "orders:v1:tenant/alice/opaque:") {
		t.Fatalf("bad namespace: %q", id)
	}
	req.EngineSession = id
	if e.engineSessionID(req) != id {
		t.Fatal("same capability did not reuse session")
	}
	req.SessionCapability = "writer"
	if e.engineSessionID(req) == id {
		t.Fatal("capability change reused old model history")
	}
	req.DisableSession = true
	if e.engineSessionID(req) != "" {
		t.Fatal("ignored session opt-out")
	}
	req.DisableSession = false
	e.cfg.ValidateSessionID = func(string) bool { return false }
	if e.engineSessionID(req) != "" {
		t.Fatal("ignored host validator")
	}
}
