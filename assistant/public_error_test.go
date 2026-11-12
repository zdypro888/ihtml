package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/agent"
)

func TestPublicFailureProjectionKeepsTitleReasonAndHistory(t *testing.T) {
	for _, execution := range []string{"not_started", "handler_returned", "unknown"} {
		t.Run(execution, func(t *testing.T) {
			r := newRecorder()
			cfg := configFor(&fakeEngine{})
			cfg.PresentToolResult = func(context.Context, *ihtml.ChatSessionContext, agent.Tool, json.RawMessage, string) ToolResultView {
				// 公共展示钩子可以补充标题，但不能覆盖规范化的失败状态与原因。
				return ToolResultView{Title: "查询记录", Status: ihtml.ChatToolSuccess, Summary: "后台接口已返回"}
			}
			s := openSession(t, cfg, "owner", r).(*session)
			p := newProjection(s, t.Context(), func() {})
			p.tools["read"] = agent.Tool{Name: "read", Completion: agent.ToolCompletionAsynchronous}
			wire, _ := agent.TaskFailureJSON(agent.NewPublicError("record_not_found", "找不到该记录，请核对原记录。", errors.New("PRIVATE")), execution, execution != "not_started", "receipt", nil)
			p.step(agent.Step{Kind: agent.StepToolCall, ToolName: "read", Result: wire, Completion: agent.ToolCompletionAsynchronous}, nil)
			events := r.snapshot()
			if len(events) != 1 {
				t.Fatal(events)
			}
			ev := events[0]
			want := ihtml.ChatToolError
			if execution == "unknown" {
				want = ihtml.ChatToolUnknown
			} else if execution == "not_started" {
				want = ihtml.ChatToolCancelled
			}
			if ev.ToolStatus != want || ev.ToolTitle != "查询记录" || !strings.Contains(ev.ToolSummary, "找不到该记录") || strings.Contains(ev.ToolSummary+string(ev.ToolResult), "PRIVATE") {
				t.Fatal(ev)
			}
			var failure agent.TaskFailure
			if json.Unmarshal(ev.ToolResult, &failure) != nil || failure.Public == nil || failure.Public.Code != "record_not_found" || failure.ExecutionState != execution {
				t.Fatal(string(ev.ToolResult))
			}
			history, ok := ToolHistoryEvent(ev)
			if !ok || history.Call.Title != ev.ToolTitle || history.Summary != ev.ToolSummary || history.Status != ev.ToolStatus || string(history.Result) != string(ev.ToolResult) {
				t.Fatal(history)
			}
		})
	}
}

func TestOrdinaryPrivateErrorRemainsRedactedWithPublicTitle(t *testing.T) {
	r := newRecorder()
	cfg := configFor(&fakeEngine{})
	cfg.PresentToolResult = func(context.Context, *ihtml.ChatSessionContext, agent.Tool, json.RawMessage, string) ToolResultView {
		return ToolResultView{Title: "查询记录", Summary: "后台接口已返回", Status: ihtml.ChatToolSuccess}
	}
	s := openSession(t, cfg, "owner", r).(*session)
	p := newProjection(s, t.Context(), func() {})
	p.step(agent.Step{Kind: agent.StepToolCall, ToolName: "read", Err: "PRIVATE provider response"}, nil)
	ev := r.snapshot()[0]
	if ev.ToolTitle != "查询记录" || ev.ToolStatus != ihtml.ChatToolError || strings.Contains(ev.ToolSummary+string(ev.ToolResult), "PRIVATE") || ev.ToolSummary == "后台接口已返回" {
		t.Fatal(ev)
	}
}

func TestConfirmedPublicFailureKeepsUnknownAndSafeFacts(t *testing.T) {
	r := newRecorder()
	cfg := approvalConfig(t, func(context.Context, json.RawMessage) (string, error) {
		return "PRIVATE response", agent.NewPublicError("record_not_found", "找不到该记录，请核对原记录。", errors.New("PRIVATE cause"))
	})
	s := openSession(t, cfg, "owner", r)
	card := proposedCard(t, s, r)
	confirmCard(s, card)
	events := r.wait(t, ihtml.ChatStreamToolResult, 1)
	for _, ev := range events {
		if ev.Type != ihtml.ChatStreamToolResult {
			continue
		}
		if ev.ToolStatus != ihtml.ChatToolUnknown || ev.ToolTitle != card.Title || !strings.Contains(ev.ToolSummary, "找不到该记录") || !strings.Contains(ev.ToolSummary, "不自动重放") || strings.Contains(ev.ToolSummary+string(ev.ToolResult), "PRIVATE") {
			t.Fatal(ev)
		}
	}
}

func TestScopePublicFailurePersistsSafeReceiptWithoutReplay(t *testing.T) {
	f := newScopeFixture(t)
	var calls atomic.Int32
	f.tool.Handler = func(context.Context, json.RawMessage) (string, error) {
		calls.Add(1)
		return "PRIVATE response", agent.NewPublicError("record_not_found", "找不到该记录，请核对原记录。", errors.New("PRIVATE cause"))
	}
	s, _ := f.session(t)
	id, ref := scopePrepared(t, f, s)
	request := scopeFindTool(t, s, f.tool, scopeReq(), authorizationRequest)
	if _, err := request.Handler(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	execute := scopeFindTool(t, s, f.tool, scopeReq(), authorizationExecute)
	var previous string
	for range 2 {
		result, err := execute.Handler(t.Context(), scopeReferenceJSON(id, "one"))
		var envelope scopeResult
		if err != nil || json.Unmarshal([]byte(result), &envelope) != nil || envelope.View.Status != ihtml.ChatToolUnknown || !strings.Contains(envelope.View.Summary, "找不到该记录") || strings.Contains(result, "PRIVATE") || calls.Load() != 1 {
			t.Fatal(result, err, calls.Load())
		}
		if previous != "" && previous != result {
			t.Fatal("recovered failure receipt changed")
		}
		previous = result
	}
}
