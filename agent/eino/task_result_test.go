package einoengine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/schema"
	ai "github.com/zdypro888/ihtml/agent"
)

// 模型可以直接调用原接口，再自行按引用选取结果片段，无需接口专用大结果流程。
func TestDeepAgentReadsLargeResultThroughGenericByteRange(t *testing.T) {
	runtime, err := ai.NewTaskRuntime(t.Context(), ai.TaskConfig{MaxResultBytes: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	var calls atomic.Int32
	state := &scriptedModelState{fn: func(messages []*schema.Message, _ []*schema.ToolInfo) (*schema.Message, error) {
		for i := len(messages) - 1; i >= 0; i-- {
			message := messages[i]
			if message.Role != schema.Tool {
				continue
			}
			switch message.ToolName {
			case "read_large":
				var result struct {
					Status string `json:"status"`
					TaskID string `json:"task_id"`
				}
				if json.Unmarshal([]byte(message.Content), &result) != nil || result.Status != "result_reference" || result.TaskID == "" {
					return nil, fmt.Errorf("missing result reference: %s", message.Content)
				}
				args, _ := json.Marshal(map[string]any{"id": result.TaskID, "offset": 0, "limit": 5})
				return taskModelCall("page", "task_result", string(args)), nil
			case "task_result":
				var page ai.TaskResultPage
				if json.Unmarshal([]byte(message.Content), &page) != nil || page.Text != "first" || page.EOF || page.NextOffset != 5 {
					return nil, fmt.Errorf("bad result page: %s", message.Content)
				}
				return schema.AssistantMessage("selected fragment inspected", nil), nil
			}
		}
		return taskModelCall("original", "read_large", `{}`), nil
	}}
	req := &ai.TurnRequest{SessionID: "result-reference", InvocationScope: "turn", UserText: "inspect first fragment", Tasks: runtime, Tools: []ai.Tool{{Name: "read_large", Effect: ai.ToolEffectRead, LoadMode: ai.ToolLoadImmediate, Handler: func(context.Context, json.RawMessage) (string, error) {
		calls.Add(1)
		return "first" + strings.Repeat("large", 100), nil
	}}}}
	result, err := newNativeTestEngine(&scriptedModel{state: state}, nil).RunTurn(t.Context(), req)
	if err != nil || result == nil || result.Text != "selected fragment inspected" || calls.Load() != 1 {
		t.Fatal(result, err, calls.Load())
	}
}
