package einoengine

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/cloudwego/eino/schema"
	ai "github.com/zdypro888/ihtml/agent"
)

// 模型按当前目标自主保存/读取长期事实，无需接口专用计划模板或宿主自动编排。
func TestDeepAgentMaintainsLongTaskContextByGenericTools(t *testing.T) {
	runtime, err := ai.NewTaskRuntime(t.Context(), ai.TaskConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	state := &scriptedModelState{fn: func(messages []*schema.Message, _ []*schema.ToolInfo) (*schema.Message, error) {
		for i := len(messages) - 1; i >= 0; i-- {
			message := messages[i]
			if message.Role != schema.Tool {
				continue
			}
			switch message.ToolName {
			case "task_context_save":
				var record ai.TaskContextRecord
				if json.Unmarshal([]byte(message.Content), &record) != nil || record.ID == "" || record.Revision != 1 {
					return nil, fmt.Errorf("missing saved context: %s", message.Content)
				}
				args, _ := json.Marshal(map[string]any{"id": record.ID})
				return taskModelCall("read", "task_context_read", string(args)), nil
			case "task_context_read":
				var record ai.TaskContextRecord
				if json.Unmarshal([]byte(message.Content), &record) != nil || record.Document.Goal != "original user goal" || len(record.Document.Constraints) != 1 || record.Document.Constraints[0] != "same fixed scope" {
					return nil, fmt.Errorf("lost goal or constraint: %s", message.Content)
				}
				return schema.AssistantMessage("saved facts read; future steps remain my decision", nil), nil
			}
		}
		return taskModelCall("save", "task_context_save", `{"expected_revision":0,"document":{"goal":"original user goal","constraints":["same fixed scope"],"pending":["inspect original receipt"],"evidence":[{"kind":"task","id":"original-handle"}],"state":"active"}}`), nil
	}}
	req := &ai.TurnRequest{SessionID: "long-context", InvocationScope: "turn", UserText: "keep the goal for later", Tasks: runtime, Tools: []ai.Tool{{Name: "unrelated_read", Effect: ai.ToolEffectRead, LoadMode: ai.ToolLoadImmediate, Handler: func(context.Context, json.RawMessage) (string, error) {
		t.Error("unrelated business interface invoked")
		return "", nil
	}}}}
	result, err := newNativeTestEngine(&scriptedModel{state: state}, nil).RunTurn(t.Context(), req)
	if err != nil || result == nil || result.Text != "saved facts read; future steps remain my decision" {
		t.Fatal(result, err)
	}
}
