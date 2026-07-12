package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestNormalizeClaudeBaseURLAvoidsDoubleVersionPath(t *testing.T) {
	for input, want := range map[string]string{
		" https://api.anthropic.com/v1/ ":          "https://api.anthropic.com",
		"https://gateway.example/api/anthropic/v1": "https://gateway.example/api/anthropic",
		"https://gateway.example/api/anthropic":    "https://gateway.example/api/anthropic",
	} {
		if got := normalizeClaudeBaseURL(input); got != want {
			t.Errorf("normalizeClaudeBaseURL(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestClaudeModelNeverFallsBackToAmbientCredentials(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "ambient-secret-must-not-be-used")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "ambient-secret-must-not-be-used")
	_, err := newClaudeChatModel(context.Background(), Config{
		Provider: ProviderClaude,
		BaseURL:  "http://127.0.0.1:9999",
		Model:    "test-model",
	})
	if err == nil {
		t.Fatal("Claude adapter accepted implicit ambient credentials")
	}
}

func TestClaudeModelDefaultsAuthThinkingAndTools(t *testing.T) {
	var requestBodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("request path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("authorization = %q", got)
		}
		var requestBody map[string]any
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requestBodies = append(requestBodies, requestBody)
		if requestBody["stream"] == true {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"stream probe"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_test","type":"message","role":"assistant","model":"test-model","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	model, err := newClaudeChatModel(context.Background(), Config{
		Provider: ProviderClaude, BaseURL: server.URL + "/v1", AuthToken: "test-token",
		Model: "test-model", Thinking: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	model, err = model.WithTools([]*schema.ToolInfo{{Name: "ping", Desc: "Return pong"}})
	if err != nil {
		t.Fatal(err)
	}
	message, err := model.Generate(context.Background(), []*schema.Message{schema.UserMessage("hello")})
	if err != nil {
		t.Fatal(err)
	}
	if message.Content != "OK" {
		t.Fatalf("message content = %q", message.Content)
	}
	if _, err := model.Stream(context.Background(), []*schema.Message{schema.UserMessage("hello stream")}); err == nil {
		t.Fatal("expected the stream probe's deliberate 400 response")
	}
	if len(requestBodies) != 2 {
		t.Fatalf("request count = %d", len(requestBodies))
	}
	for index, requestBody := range requestBodies {
		maxTokens, ok := requestBody["max_tokens"].(float64)
		if !ok || int(maxTokens) != defaultClaudeMaxTokens {
			t.Fatalf("request %d max_tokens = %#v", index, requestBody["max_tokens"])
		}
		thinking, ok := requestBody["thinking"].(map[string]any)
		budget, budgetOK := thinking["budget_tokens"].(float64)
		if !ok || thinking["type"] != "enabled" || !budgetOK || int(budget) != defaultClaudeThinkingBudget {
			t.Fatalf("request %d thinking = %#v", index, requestBody["thinking"])
		}
		if tools, ok := requestBody["tools"].([]any); !ok || len(tools) != 1 {
			t.Fatalf("request %d tools = %#v", index, requestBody["tools"])
		}
	}
	if requestBodies[0]["stream"] == true || requestBodies[1]["stream"] != true {
		t.Fatalf("stream flags = %#v, %#v", requestBodies[0]["stream"], requestBodies[1]["stream"])
	}
}

func TestClaudeThinkingBudgetMustFitMaxTokens(t *testing.T) {
	_, err := newClaudeChatModel(context.Background(), Config{
		Provider: ProviderClaude, BaseURL: "https://claude.example", Model: "model",
		Thinking: true, MaxTokens: 1024, ThinkingBudgetTokens: 1024,
	})
	if err == nil {
		t.Fatal("expected invalid thinking budget to fail before a request")
	}
}
