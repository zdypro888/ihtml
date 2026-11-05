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

// 所有公共控制工具使用同一错误信封，错误不会偷偷结束模型轮次或透传后端原文。
func TestKernelHandlersExposeStructuredErrors(t *testing.T) {
	runtime, err := ai.NewTaskRuntime(t.Context(), ai.TaskConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	tools, err := taskTools(&ai.TurnRequest{SessionID: "failure", Tasks: runtime})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{"task_context_read": `{"id":"missing"}`, "task_event_read": `{"source_id":"missing","after":0,"limit":1}`, "task_result": `{"id":"missing","offset":0,"limit":1}`}
	checked := 0
	for _, tool := range tools {
		raw, ok := cases[tool.Name]
		if !ok {
			continue
		}
		value, err := tool.Handler(t.Context(), json.RawMessage(raw))
		if err != nil {
			t.Fatal(tool.Name, "escaped structured envelope", err)
		}
		var failure struct {
			Code           string `json:"code"`
			AdmissionState string `json:"admission_state"`
		}
		if json.Unmarshal([]byte(value), &failure) != nil || failure.Code == "" || failure.AdmissionState == "" {
			t.Fatal(tool.Name, value)
		}
		checked++
	}
	if checked != len(cases) {
		t.Fatal("missing kernel tools", checked)
	}
}

// 在真实Eino边界，已发生外层副作用后的内层拒绝仍是未知结果，模型只能核对回执。
func TestDeepAgentPreservesOuterWriteWhenNestedCapacityFails(t *testing.T) {
	runtime, err := ai.NewTaskRuntime(t.Context(), ai.TaskConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	var writes atomic.Int32
	state := &scriptedModelState{fn: func(messages []*schema.Message, _ []*schema.ToolInfo) (*schema.Message, error) {
		for i := len(messages) - 1; i >= 0; i-- {
			message := messages[i]
			if message.Role != schema.Tool || message.ToolName != "nested_write" {
				continue
			}
			var failure struct {
				Status         string `json:"status"`
				Code           string `json:"code"`
				ExecutionState string `json:"execution_state"`
				AdmissionState string `json:"admission_state"`
				ReceiptID      string `json:"receipt_id"`
				Message        string `json:"message"`
			}
			if json.Unmarshal([]byte(message.Content), &failure) != nil || failure.Status != "operation_failed" || failure.Code != "budget_exhausted" || failure.ExecutionState != "unknown" || failure.AdmissionState != "admitted" || failure.ReceiptID == "" || strings.Contains(failure.Message, "尚未受理") {
				return nil, fmt.Errorf("outer effect lost: %s", message.Content)
			}
			return schema.AssistantMessage("inspect original receipt", nil), nil
		}
		return taskModelCall("outer", "nested_write", `{}`), nil
	}}
	req := &ai.TurnRequest{SessionID: "outer-write", InvocationScope: "turn", Tasks: runtime, UserText: "test error facts", Tools: []ai.Tool{{Name: "nested_write", Effect: ai.ToolEffectWrite, LoadMode: ai.ToolLoadImmediate, Handler: func(context.Context, json.RawMessage) (string, error) {
		writes.Add(1)
		return "", ai.TaskFailureFrom(&ai.TaskBudgetError{Kind: "task"}, "not_started", false, "inner", nil)
	}}}}
	result, err := newNativeTestEngine(&scriptedModel{state: state}, nil).RunTurn(t.Context(), req)
	if err != nil || result == nil || result.Text != "inspect original receipt" || writes.Load() != 1 {
		t.Fatal(result, err, writes.Load())
	}
}
