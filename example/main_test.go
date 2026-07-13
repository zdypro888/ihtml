package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/chat"
)

func TestExampleChatConfigKeepsDeepExplicitAndBudgetsBounded(t *testing.T) {
	for _, key := range []string{
		"IHTML_AI_BASE_URL",
		"IHTML_AI_PROVIDER",
		"IHTML_AI_API_KEY",
		"IHTML_AI_AUTH_TOKEN",
		"IHTML_AI_MODEL",
		"IHTML_AI_THINKING",
		"IHTML_AI_MAX_TOKENS",
		"IHTML_AI_THINKING_BUDGET_TOKENS",
		"IHTML_AI_REQUEST_TIMEOUT_MS",
		"IHTML_AI_ENABLE_DEEP",
		"IHTML_AI_MAX_MODEL_CALLS",
		"IHTML_AI_MAX_SUBTASKS",
		"IHTML_AI_MAX_TURN_SECONDS",
		"ANTHROPIC_BASE_URL",
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_AUTH_TOKEN",
		"ANTHROPIC_MODEL",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL",
		"ANTHROPIC_DEFAULT_SONNET_MODEL",
		"ANTHROPIC_DEFAULT_OPUS_MODEL",
		"API_TIMEOUT_MS",
	} {
		t.Setenv(key, "")
	}

	cfg := exampleChatConfig(" https://ai.example/v1 ", "http://127.0.0.1:8899", []string{"uid"})
	if cfg.EnableDeepAgent {
		t.Fatal("Deep Agent must remain disabled without an explicit environment flag")
	}
	if cfg.Provider != chat.ProviderOpenAI || cfg.BaseURL != "https://ai.example/v1" {
		t.Fatalf("provider/base = %q, %q", cfg.Provider, cfg.BaseURL)
	}
	if cfg.MaxModelCallsPerTurn != 24 || cfg.MaxSubtasksPerTurn != 3 || cfg.MaxTurnDuration != 2*time.Minute {
		t.Fatalf("default budgets = calls:%d subtasks:%d duration:%s", cfg.MaxModelCallsPerTurn, cfg.MaxSubtasksPerTurn, cfg.MaxTurnDuration)
	}
	if len(cfg.ProbeForwardQuery) != 1 || cfg.ProbeForwardQuery[0] != "uid" {
		t.Fatalf("probe query = %v", cfg.ProbeForwardQuery)
	}

	t.Setenv("IHTML_AI_THINKING", "yes")
	t.Setenv("IHTML_AI_ENABLE_DEEP", "true")
	t.Setenv("IHTML_AI_MAX_MODEL_CALLS", "12")
	t.Setenv("IHTML_AI_MAX_SUBTASKS", "2")
	t.Setenv("IHTML_AI_MAX_TURN_SECONDS", "45")
	cfg = exampleChatConfig("https://ai.example/v1", "", nil)
	if !cfg.Thinking || !cfg.EnableDeepAgent {
		t.Fatalf("explicit feature flags were not enabled: thinking=%v deep=%v", cfg.Thinking, cfg.EnableDeepAgent)
	}
	if cfg.MaxModelCallsPerTurn != 12 || cfg.MaxSubtasksPerTurn != 2 || cfg.MaxTurnDuration != 45*time.Second {
		t.Fatalf("configured budgets = calls:%d subtasks:%d duration:%s", cfg.MaxModelCallsPerTurn, cfg.MaxSubtasksPerTurn, cfg.MaxTurnDuration)
	}
}

func TestDemoInspectionNeverClaimsMissingItemLoadedBecauseOfOldError(t *testing.T) {
	note := demoInspectionNote(&ihtml.ChatClientInspection{Items: []ihtml.ChatClientInspectionItem{{
		ID: "ai-note", Present: false, Error: "historical error",
	}}})
	if !strings.Contains(note, "还没有找到") || strings.Contains(note, "已加载") {
		t.Fatalf("missing Item was described as loaded: %q", note)
	}
	visible := demoInspectionNote(&ihtml.ChatClientInspection{Items: []ihtml.ChatClientInspectionItem{{
		ID: "ai-note", Present: true, Visible: true,
	}}})
	if !strings.Contains(visible, "当前视口可见") {
		t.Fatalf("visible Item was not accepted: %q", visible)
	}
}

func TestExampleChatConfigAcceptsAnthropicEnvironment(t *testing.T) {
	for _, key := range []string{
		"IHTML_AI_BASE_URL", "IHTML_AI_PROVIDER", "IHTML_AI_API_KEY", "IHTML_AI_AUTH_TOKEN", "IHTML_AI_MODEL",
		"IHTML_AI_MAX_TOKENS", "IHTML_AI_THINKING_BUDGET_TOKENS", "IHTML_AI_REQUEST_TIMEOUT_MS",
		"ANTHROPIC_API_KEY", "ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("ANTHROPIC_BASE_URL", "https://claude.example/api/anthropic")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "test-token")
	t.Setenv("ANTHROPIC_DEFAULT_HAIKU_MODEL", "fast-model")
	t.Setenv("API_TIMEOUT_MS", "3000000")

	base := envFirst("IHTML_AI_BASE_URL", "ANTHROPIC_BASE_URL")
	cfg := exampleChatConfig(base, "", nil)
	if cfg.Provider != chat.ProviderClaude || cfg.BaseURL != "https://claude.example/api/anthropic" {
		t.Fatalf("provider/base = %q, %q", cfg.Provider, cfg.BaseURL)
	}
	if cfg.APIKey != "" || cfg.AuthToken != "test-token" || cfg.Model != "fast-model" {
		t.Fatalf("credentials/model mapping failed: api_set=%v auth_matches=%v model=%q", cfg.APIKey != "", cfg.AuthToken == "test-token", cfg.Model)
	}
	if cfg.MaxTokens != 4096 || cfg.ThinkingBudgetTokens != 1024 {
		t.Fatalf("claude token defaults = max:%d thinking:%d", cfg.MaxTokens, cfg.ThinkingBudgetTokens)
	}
	if cfg.RequestTimeout != 3000000*time.Millisecond {
		t.Fatalf("request timeout = %s", cfg.RequestTimeout)
	}
}

func TestExampleChatConfigDoesNotLeakAnthropicCredentialsToOpenAI(t *testing.T) {
	t.Setenv("IHTML_AI_PROVIDER", chat.ProviderOpenAI)
	t.Setenv("IHTML_AI_API_KEY", "")
	t.Setenv("IHTML_AI_AUTH_TOKEN", "")
	t.Setenv("IHTML_AI_MODEL", "openai-model")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-key")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "anthropic-token")

	cfg := exampleChatConfig("https://openai.example/v1", "", nil)
	if cfg.APIKey != "" || cfg.AuthToken != "" {
		t.Fatalf("Anthropic credentials crossed provider boundary: api_set=%v auth_set=%v", cfg.APIKey != "", cfg.AuthToken != "")
	}
}

func TestExampleChatConfigDoesNotMixCredentialSources(t *testing.T) {
	t.Setenv("IHTML_AI_PROVIDER", chat.ProviderClaude)
	t.Setenv("IHTML_AI_API_KEY", "ihtml-key")
	t.Setenv("IHTML_AI_AUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-key")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "anthropic-token")

	cfg := exampleChatConfig("https://claude.example", "", nil)
	if cfg.APIKey != "ihtml-key" || cfg.AuthToken != "" {
		t.Fatalf("credential sources were mixed: api_matches=%v auth_set=%v", cfg.APIKey == "ihtml-key", cfg.AuthToken != "")
	}
}

func TestExampleModelCapabilitiesMatchEnabledBackend(t *testing.T) {
	t.Setenv("IHTML_AI_PROVIDER", chat.ProviderClaude) // Models() need not call a remote catalog.
	t.Setenv("IHTML_AI_ENABLE_DEEP", "")

	cfg := exampleChatConfig("https://ai.example/v1", "", nil)
	models, err := chat.New(cfg).Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models.AgentModes) != 1 || models.AgentModes[0] != chat.AgentModeFast || models.DeepApprovalResume != "" {
		t.Fatalf("default capabilities = modes:%v approval:%q", models.AgentModes, models.DeepApprovalResume)
	}

	t.Setenv("IHTML_AI_ENABLE_DEEP", "1")
	cfg = exampleChatConfig("https://ai.example/v1", "", nil)
	models, err = chat.New(cfg).Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models.AgentModes) != 2 || models.AgentModes[0] != chat.AgentModeFast || models.AgentModes[1] != chat.AgentModeDeep {
		t.Fatalf("enabled capabilities = %v", models.AgentModes)
	}
	if models.DeepApprovalResume != "manual_tool_only" {
		t.Fatalf("Deep approval capability = %q", models.DeepApprovalResume)
	}

	demoModels, err := (&demoBackend{}).Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(demoModels.AgentModes) != 1 || demoModels.AgentModes[0] != chat.AgentModeFast {
		t.Fatalf("demo backend must only advertise Fast: %v", demoModels.AgentModes)
	}
}

func TestLocalSameOriginRequest(t *testing.T) {
	tests := []struct {
		name      string
		host      string
		origin    string
		fetchSite string
		want      bool
	}{
		{name: "server-side loopback", host: "127.0.0.1:8899", want: true},
		{name: "same origin", host: "127.0.0.1:8899", origin: "http://127.0.0.1:8899", fetchSite: "same-origin", want: true},
		{name: "cross origin", host: "127.0.0.1:8899", origin: "https://evil.example", fetchSite: "cross-site", want: false},
		{name: "dns rebinding host", host: "evil.example:8899", origin: "http://evil.example:8899", fetchSite: "same-origin", want: false},
		{name: "null origin", host: "localhost:8899", origin: "null", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://"+tc.host+"/capi/nodes", nil)
			r.Host = tc.host
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if tc.fetchSite != "" {
				r.Header.Set("Sec-Fetch-Site", tc.fetchSite)
			}
			if got := isLocalSameOriginRequest(r); got != tc.want {
				t.Fatalf("isLocalSameOriginRequest() = %v, want %v", got, tc.want)
			}
		})
	}
}

func newDemoTestSession(t *testing.T) (*ihtml.Handler, *demoSession, *[]ihtml.ChatStreamEvent) {
	t.Helper()
	h := ihtml.New(ihtml.NewMemoryStore())
	var mu sync.Mutex
	events := []ihtml.ChatStreamEvent{}
	s := &demoSession{sctx: &ihtml.ChatSessionContext{
		User:    "demo-test",
		Service: h,
		Emit: func(event ihtml.ChatStreamEvent) error {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
			return nil
		},
	}}
	t.Cleanup(func() {
		s.Close()
		_ = h.Close()
	})
	return h, s, &events
}

func TestDemoSessionCancelPreventsWrite(t *testing.T) {
	h, s, _ := newDemoTestSession(t)
	s.HandleEvent(context.Background(), &ihtml.ChatClientEvent{
		Type: "user_message", Messages: []ihtml.ChatMessage{{Role: "user", Content: "must not be written"}},
	})
	s.HandleEvent(context.Background(), &ihtml.ChatClientEvent{Type: "cancel"})
	s.Close()
	items, err := h.ListItems(context.Background(), "demo-test")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("cancelled demo run wrote %d items", len(items))
	}
}

func TestDemoSessionWritesScopedEscapedHTML(t *testing.T) {
	h, s, _ := newDemoTestSession(t)
	s.HandleEvent(context.Background(), &ihtml.ChatClientEvent{
		Type: "user_message", Messages: []ihtml.ChatMessage{{Role: "user", Content: `<img src=x onerror="alert(1)">`}},
	})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		items, err := h.ListItems(context.Background(), "demo-test")
		if err != nil {
			t.Fatal(err)
		}
		if len(items) == 1 {
			content := items[0].Content
			if strings.Contains(content, "<script") || strings.Contains(content, "<img") {
				t.Fatalf("demo item contains executable user markup: %s", content)
			}
			if !strings.Contains(content, "&lt;img") || !strings.Contains(content, `class="ui-page"`) {
				t.Fatalf("demo item is not escaped/scoped: %s", content)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("demo assistant did not write an item before timeout")
}
