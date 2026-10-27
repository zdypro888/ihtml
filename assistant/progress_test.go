package assistant

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/agent"
)

// PrepareTurn 返回值会被桥接器复制；状态必须经工具上下文返回同一投影，不能捕获原请求指针。
func TestToolContextProgressUsesManagedTurn(t *testing.T) {
	var audited atomic.Int32
	engine := &fakeEngine{run: func(ctx context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
		_, err := req.Tools[0].Handler(ctx, json.RawMessage(`{}`))
		return &agent.TurnResult{Text: "ready"}, err
	}}
	cfg := configFor(engine)
	cfg.PrepareTurn = func(context.Context, *ihtml.ChatSessionContext, *ihtml.ChatClientEvent) (*agent.TurnRequest, error) {
		return &agent.TurnRequest{SessionID: "fixture", OnEvent: func(step agent.Step) {
			if step.Kind == agent.StepStatus {
				audited.Add(1)
			}
		}, Tools: []agent.Tool{{Name: "wait", Effect: agent.ToolEffectRead, Handler: func(ctx context.Context, _ json.RawMessage) (string, error) {
			if !agent.ReportStatus(ctx, "正在等待原任务") {
				t.Error("missing status reporter")
			}
			return `{}`, nil
		}}}}, nil
	}
	recorder := newRecorder()
	session := openSession(t, cfg, "user", recorder)
	send(session, "wait")
	events := recorder.wait(t, ihtml.ChatStreamAssistantDone, 1)
	count := 0
	for _, ev := range events {
		if ev.Type == ihtml.ChatStreamAssistantStatus && ev.Status == "正在等待原任务" {
			count++
			if ev.RunID == "" || ev.Seq == 0 {
				t.Fatal("unsequenced status")
			}
		}
	}
	if count != 1 || audited.Load() != 1 {
		t.Fatal("progress lost or duplicated", count, audited.Load())
	}
}

func TestConfirmedToolContextCanReportProgress(t *testing.T) {
	cfg := approvalConfig(t, func(ctx context.Context, _ json.RawMessage) (string, error) {
		if !agent.ReportStatus(ctx, "正在执行已确认操作") {
			t.Error("confirmed tool missing reporter")
		}
		return `{}`, nil
	})
	recorder := newRecorder()
	session := openSession(t, cfg, "user", recorder)
	card := proposedCard(t, session, recorder)
	session.HandleEvent(t.Context(), &ihtml.ChatClientEvent{Type: ihtml.ChatClientConfirmTool, ToolCall: &card})
	events := recorder.wait(t, ihtml.ChatStreamAssistantDone, 2)
	count := 0
	for _, ev := range events {
		if ev.Type == ihtml.ChatStreamAssistantStatus && ev.Status == "正在执行已确认操作" {
			count++
			if ev.RunID == "" {
				t.Fatal("unmanaged progress")
			}
		}
	}
	if count != 1 {
		t.Fatal("confirmed progress lost", count)
	}
}
