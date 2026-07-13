package chat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/adk"
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

func TestClaudeProviderTransportMakesOneAttempt(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"api_error","message":"temporarily unavailable"}}`))
	}))
	defer server.Close()

	model, err := newClaudeChatModel(context.Background(), Config{
		Provider: ProviderClaude, BaseURL: server.URL, AuthToken: "test-token",
		Model: "test-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = model.Generate(context.Background(), []*schema.Message{schema.UserMessage("hello")})
	if err == nil || !isRetryableModelError(context.Background(), err) {
		t.Fatalf("transient Claude error = %v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("provider HTTP requests = %d; Anthropic SDK retried behind ADK", got)
	}
}

func TestClaudeADKRetriesAreBudgetedOneForOne(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := requests.Add(1)
		if got := r.Header.Get("X-Stainless-Retry-Count"); got != "0" {
			t.Errorf("provider attempt %d SDK retry count = %q", attempt, got)
		}
		w.Header().Set("Content-Type", "application/json")
		if attempt == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"api_error","message":"temporarily unavailable"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"msg_test","type":"message","role":"assistant","model":"test-model","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	base, err := newClaudeChatModel(context.Background(), Config{
		Provider: ProviderClaude, BaseURL: server.URL, AuthToken: "test-token",
		Model: "test-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	budget := newTurnBudget(Config{MaxModelCallsPerTurn: 4, MaxTurnDuration: -1}, AgentModeFast, nil)
	agent, err := adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Name:             "claude_retry_test",
		Description:      "Verify Claude retry ownership.",
		Instruction:      "Answer directly.",
		Model:            withTurnBudget(base, budget),
		GenModelInput:    plainGenModelInput,
		MaxIterations:    1,
		ModelRetryConfig: newModelRetryConfig(1),
	})
	if err != nil {
		t.Fatal(err)
	}

	iter := agent.Run(context.Background(), &adk.AgentInput{
		Messages:        []*schema.Message{schema.UserMessage("hello")},
		EnableStreaming: false,
	})
	var final string
	for event, ok := iter.Next(); ok; event, ok = iter.Next() {
		if event == nil {
			continue
		}
		if event.Err != nil {
			var retry *adk.WillRetryError
			if !errors.As(event.Err, &retry) {
				t.Fatalf("agent event error: %v", event.Err)
			}
			continue
		}
		if event.Output != nil && event.Output.MessageOutput != nil {
			message := event.Output.MessageOutput.Message
			if message != nil && message.Role == schema.Assistant {
				final = message.Content
			}
		}
	}
	if final != "OK" {
		t.Fatalf("final answer = %q", final)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("provider HTTP requests = %d, want initial + one ADK retry", got)
	}
	if got := budget.snapshot().ModelCalls; got != requests.Load() {
		t.Fatalf("budgeted model calls = %d, provider HTTP attempts = %d", got, requests.Load())
	}
}
