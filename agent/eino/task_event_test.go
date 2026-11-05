package einoengine

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	ai "github.com/zdypro888/ihtml/agent"
)

// 模型只发现宿主给出的源并选择事件挂起；源发布入口不暴露为模型工具。
func TestDeepAgentChoosesAuthorizedExternalEventSubscription(t *testing.T) {
	scope := ai.TaskScope(&ai.TurnRequest{SessionID: "events", SessionCapability: "cap"})
	runtime, err := ai.NewTaskRuntime(t.Context(), ai.TaskConfig{Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	source, err := runtime.EventPublisher().OpenSource(t.Context(), scope, "trusted-host-key", "external source")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	state := &scriptedModelState{fn: func(messages []*schema.Message, tools []*schema.ToolInfo) (*schema.Message, error) {
		calls++
		for _, tool := range tools {
			if tool.Name == "event_publish" || tool.Name == "event_source_create" {
				return nil, fmt.Errorf("model received host publishing authority")
			}
		}
		for i := len(messages) - 1; i >= 0; i-- {
			message := messages[i]
			if message.Role != schema.Tool {
				continue
			}
			if message.ToolName == "task_event_sources" {
				var response struct {
					Sources []ai.TaskEventSource `json:"sources"`
				}
				if json.Unmarshal([]byte(message.Content), &response) != nil || len(response.Sources) != 1 || response.Sources[0].ID != source.ID {
					return nil, fmt.Errorf("wrong authorized source discovery: %s", message.Content)
				}
				args, _ := json.Marshal(map[string]any{"ids": []string{}, "events": []ai.TaskEventCondition{{SourceID: source.ID, After: response.Sources[0].Head, Types: []string{"ready"}}}, "timeout_ms": 60000, "checkpoint": "inspect the event and decide how to continue"})
				return taskModelCall("wait", "task_suspend", string(args)), nil
			}
		}
		return taskModelCall("sources", "task_event_sources", `{}`), nil
	}}
	req := &ai.TurnRequest{SessionID: "events", SessionCapability: "cap", InvocationScope: "turn", UserText: "wait for the authorized source", Tasks: runtime, AllowTaskSuspend: true, Tools: []ai.Tool{{Name: "unrelated_read", Effect: ai.ToolEffectRead, LoadMode: ai.ToolLoadImmediate, Handler: func(context.Context, json.RawMessage) (string, error) { return "unused", nil }}}}
	result, err := newNativeTestEngine(&scriptedModel{state: state}, nil).RunTurn(t.Context(), req)
	if err != nil || result == nil || result.Waiting == nil || result.FinishReason != "task_waiting" || calls != 2 {
		t.Fatal("event waiting polled model or did not suspend", result, err, calls)
	}
	if _, err = runtime.EventPublisher().Publish(t.Context(), scope, source.ID, ai.TaskExternalEvent{Key: "external-ready", Type: "ready", Payload: json.RawMessage(`{"state":"actual fact"}`)}); err != nil {
		t.Fatal(err)
	}
	wake, err := runtime.ClaimWake(t.Context(), scope, "consumer")
	if err != nil || wake == nil || len(wake.EventMatches) != 1 || wake.EventMatches[0].Seq != 1 || !wake.Spec.Deadline.After(time.Now()) {
		t.Fatal(wake, err)
	}
}
