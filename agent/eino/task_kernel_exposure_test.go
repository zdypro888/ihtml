package einoengine

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/cloudwego/eino/schema"
	ai "github.com/zdypro888/ihtml/agent"
)

// 控制原语不依赖虚构业务工具；模型可以只用上下文、事件与追踪完成公开事实管理。
func TestDeepAgentKernelWorksWithoutBusinessCatalog(t *testing.T) {
	runtime, err := ai.NewTaskRuntime(t.Context(), ai.TaskConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	state := &scriptedModelState{fn: func(messages []*schema.Message, tools []*schema.ToolInfo) (*schema.Message, error) {
		names := map[string]bool{}
		for _, tool := range tools {
			names[tool.Name] = true
		}
		for _, name := range []string{"task_context_save", "task_context_read", "task_event_sources", "task_trace"} {
			if !names[name] {
				return nil, fmt.Errorf("missing kernel without business catalog: %s", name)
			}
		}
		for i := len(messages) - 1; i >= 0; i-- {
			message := messages[i]
			if message.Role != schema.Tool {
				continue
			}
			switch message.ToolName {
			case "task_context_save":
				var record ai.TaskContextRecord
				if json.Unmarshal([]byte(message.Content), &record) != nil || record.ID == "" {
					return nil, fmt.Errorf("context save failed: %s", message.Content)
				}
				raw, _ := json.Marshal(map[string]any{"id": record.ID})
				return taskModelCall("read", "task_context_read", string(raw)), nil
			case "task_context_read":
				var record ai.TaskContextRecord
				if json.Unmarshal([]byte(message.Content), &record) != nil || record.Document.Goal != "public goal" {
					return nil, fmt.Errorf("context read failed: %s", message.Content)
				}
				return taskModelCall("events", "task_event_sources", `{}`), nil
			case "task_event_sources":
				var result struct {
					Sources []ai.TaskEventSource `json:"sources"`
				}
				if json.Unmarshal([]byte(message.Content), &result) != nil {
					return nil, fmt.Errorf("event discovery failed: %s", message.Content)
				}
				return taskModelCall("trace", "task_trace", `{"limit":4}`), nil
			case "task_trace":
				var page ai.TaskObservationPage
				if json.Unmarshal([]byte(message.Content), &page) != nil {
					return nil, fmt.Errorf("trace read failed: %s", message.Content)
				}
				return schema.AssistantMessage("public facts maintained without business tools", nil), nil
			}
		}
		return taskModelCall("save", "task_context_save", `{"expected_revision":0,"document":{"goal":"public goal","state":"active"}}`), nil
	}}
	req := &ai.TurnRequest{SessionID: "kernel-only", InvocationScope: "turn", UserText: "maintain public facts", Tasks: runtime}
	result, err := newNativeTestEngine(&scriptedModel{state: state}, nil).RunTurn(t.Context(), req)
	if err != nil || result == nil || result.Text != "public facts maintained without business tools" {
		t.Fatal(result, err)
	}
}

// 显式关闭覆盖原目录、控制原语及可选框架工具，不因持有TaskRuntime而绕过用户开关。
func TestDeepAgentDisableToolsHidesAllCapabilities(t *testing.T) {
	runtime, err := ai.NewTaskRuntime(t.Context(), ai.TaskConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	model := &scriptedModel{state: &scriptedModelState{fn: func(_ []*schema.Message, tools []*schema.ToolInfo) (*schema.Message, error) {
		if len(tools) != 0 {
			return nil, fmt.Errorf("disabled turn exposed %d tools", len(tools))
		}
		return schema.AssistantMessage("plain reply", nil), nil
	}}}
	engine := newNativeTestEngine(model, nil)
	engine.cfg.EnableWriteTodos = true
	engine.cfg.EnableGeneralSubAgent = true
	req := &ai.TurnRequest{SessionID: "disabled", UserText: "plain", Tasks: runtime, DisableTools: true, Tools: []ai.Tool{{Name: "business", Effect: ai.ToolEffectRead, Handler: func(context.Context, json.RawMessage) (string, error) {
		t.Error("disabled business handler ran")
		return "", nil
	}}}}
	result, err := engine.RunTurn(t.Context(), req)
	if err != nil || result == nil || result.Text != "plain reply" {
		t.Fatal(result, err)
	}
	if len(req.Tools) != 1 || req.Tasks == nil {
		t.Fatal("engine mutated caller's request")
	}
}
