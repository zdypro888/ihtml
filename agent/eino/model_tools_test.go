package einoengine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	ai "github.com/zdypro888/ihtml/agent"
)

// 在发送任何模型请求前识别该模型已公开的工具协议限制；普通文本模式不受影响。
func TestTerraToolCallsRequireResponses(t *testing.T) {
	for _, model := range []string{"gpt-6.1-terra", "gpt-6.1-terra-2026-10-26", " gpt-6.1-terra "} {
		e := &Engine{cfg: Config{Provider: ProviderOpenAI, Model: model, OpenAIAPI: "chat_completions"}}
		_, err := e.RunTurn(context.Background(), &ai.TurnRequest{UserText: "fixture", Tools: []ai.Tool{{Name: "probe", Handler: func(context.Context, json.RawMessage) (string, error) {
			t.Error("incompatible model invoked a tool")
			return "", nil
		}}}})
		if err == nil || !strings.Contains(err.Error(), "requires openai_api=responses") {
			t.Fatalf("wrong protocol error for %s", model)
		}
	}
}
