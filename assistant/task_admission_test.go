package assistant

import (
	"context"
	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/agent"
	"testing"
)

// 资源拒绝是通用执行事实；即使工具声明异步，也不能展示为已受理。
func TestTaskAdmissionRejectionDoesNotAppearAccepted(t *testing.T) {
	rec := newRecorder()
	cfg := configFor(&fakeEngine{run: func(_ context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
		req.OnEvent(agent.Step{Kind: agent.StepToolCall, ToolName: "arbitrary_async", Completion: agent.ToolCompletionAsynchronous, Result: `{"status":"not_admitted","execution_state":"not_started","code":"budget_exhausted"}`})
		return &agent.TurnResult{Text: "work remains unsubmitted"}, nil
	}})
	session := openSession(t, cfg, "alice", rec)
	send(session, "start")
	events := rec.wait(t, ihtml.ChatStreamAssistantDone, 1)
	for _, event := range events {
		if event.Type == ihtml.ChatStreamToolResult {
			if event.ToolStatus != ihtml.ChatToolCancelled {
				t.Fatal("unadmitted request appeared accepted", event)
			}
			return
		}
	}
	t.Fatal("missing admission result")
}

// 结构化错误作为工具信封返回也必须保持unknown/error显示，不能因Go error为nil显示成功。
func TestStructuredToolFailureProjectionKeepsExecutionFacts(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		state := "handler_returned"
		expected := ihtml.ChatToolError
		if unknown {
			state = "unknown"
			expected = ihtml.ChatToolUnknown
		}
		rec := newRecorder()
		cfg := configFor(&fakeEngine{run: func(_ context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
			req.OnEvent(agent.Step{Kind: agent.StepToolCall, ToolName: "arbitrary", Result: `{"status":"operation_failed","execution_state":"` + state + `","admitted":true}`})
			return &agent.TurnResult{Text: "receipt required"}, nil
		}})
		s := openSession(t, cfg, "alice", rec)
		send(s, "start")
		events := rec.wait(t, ihtml.ChatStreamAssistantDone, 1)
		found := false
		for _, event := range events {
			if event.Type == ihtml.ChatStreamToolResult {
				found = true
				if event.ToolStatus != expected {
					t.Fatal(state, event)
				}
			}
		}
		if !found {
			t.Fatal("missing execution facts")
		}
	}
}
