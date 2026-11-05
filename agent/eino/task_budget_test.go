package einoengine

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/schema"
	ai "github.com/zdypro888/ihtml/agent"
)

// 模型读取额度后自主提交后台调用；容量拒绝必须是未受理事实，而非虚构已执行回执。
func TestDeepAgentObservesSharedBudgetAndAdmissionBackpressure(t *testing.T) {
	runtime, err := ai.NewTaskRuntime(t.Context(), ai.TaskConfig{BudgetPool: "pool", Budgets: []ai.TaskBudgetLimit{{Key: "global", MaxActive: 1, MaxConcurrent: 1, MaxWaiting: 1}}})
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
			case "task_budget":
				var snapshot struct {
					Resources []ai.TaskBudgetView `json:"resources"`
				}
				if json.Unmarshal([]byte(message.Content), &snapshot) != nil || len(snapshot.Resources) != 1 || snapshot.Resources[0].ActiveRemaining != 1 {
					return nil, fmt.Errorf("missing shared budget: %s", message.Content)
				}
				return taskModelCall("first", "task_start", `{"tool":"read_sync","arguments":{"item":1}}`), nil
			case "task_start":
				var response map[string]any
				if json.Unmarshal([]byte(message.Content), &response) != nil {
					return nil, fmt.Errorf("invalid scheduling facts: %s", message.Content)
				}
				if response["status"] == "not_admitted" {
					if response["code"] != "budget_exhausted" || response["execution_state"] != "not_started" {
						return nil, fmt.Errorf("wrong admission facts: %s", message.Content)
					}
					return schema.AssistantMessage("capacity observed; further work remains unsubmitted", nil), nil
				}
				return taskModelCall("second", "task_start", `{"tool":"read_sync","arguments":{"item":2}}`), nil
			}
		}
		return taskModelCall("budget", "task_budget", `{}`), nil
	}}
	req := &ai.TurnRequest{SessionID: "budget", InvocationScope: "turn", UserText: "inspect capacity", Tasks: runtime, Tools: []ai.Tool{{Name: "read_sync", LoadMode: ai.ToolLoadImmediate, Effect: ai.ToolEffectRead, Handler: func(ctx context.Context, _ json.RawMessage) (string, error) {
		calls.Add(1)
		<-ctx.Done()
		return "", ctx.Err()
	}}}}
	result, err := newNativeTestEngine(&scriptedModel{state: state}, nil).RunTurn(t.Context(), req)
	if err != nil || result == nil || result.Text != "capacity observed; further work remains unsubmitted" || calls.Load() > 1 {
		t.Fatal(result, err, calls.Load())
	}
}
