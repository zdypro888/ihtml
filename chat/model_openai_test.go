package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestOpenAIModelSendsExplicitZeroTemperature(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("request path = %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_test","object":"chat.completion","created":1,"model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	zero := float32(0)
	model, err := newOpenAIChatModel(context.Background(), Config{
		Provider: ProviderOpenAI, BaseURL: server.URL + "/v1", APIKey: "test-key", Model: "test-model",
		TemperatureOverride: &zero,
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := model.Generate(context.Background(), []*schema.Message{schema.UserMessage("hello")})
	if err != nil {
		t.Fatal(err)
	}
	if message.Content != "OK" {
		t.Fatalf("content = %q", message.Content)
	}
	temperature, ok := requestBody["temperature"].(float64)
	if !ok || temperature != 0 {
		t.Fatalf("temperature = %#v", requestBody["temperature"])
	}
}
