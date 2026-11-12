package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/agent"
)

func TestToolLifecycleProjectionPairsParallelResultsAndPersists(t *testing.T) {
	r := newRecorder()
	s := openSession(t, configFor(&fakeEngine{}), "owner", r).(*session)
	defer s.Close()
	p := newProjection(s, t.Context(), func() {})
	for _, id := range []string{"a", "b"} {
		p.step(agent.Step{Kind: agent.StepToolStart, ToolCallID: id, ToolName: "slow_read", ToolTitle: "读取资料", Args: json.RawMessage(`{"private":"secret"}`)}, nil)
	}
	p.step(agent.Step{Kind: agent.StepToolCall, ToolCallID: "b", ToolName: "slow_read", ToolTitle: "读取资料", Completion: agent.ToolCompletionAsynchronous}, nil)
	p.step(agent.Step{Kind: agent.StepToolCall, ToolCallID: "a", ToolName: "slow_read", ToolTitle: "读取资料"}, nil)
	events := r.snapshot()
	if len(events) != 4 {
		t.Fatal(events)
	}
	if events[0].ToolID == events[1].ToolID || events[0].ToolID != events[3].ToolID || events[1].ToolID != events[2].ToolID || events[2].ToolStatus != ihtml.ChatToolAccepted {
		t.Fatal(events)
	}
	for _, event := range events {
		raw, _ := json.Marshal(event)
		if strings.Contains(string(raw), "private") || strings.Contains(string(raw), "secret") {
			t.Fatal(string(raw))
		}
		if event.ToolTitle != "读取资料" {
			t.Fatal(event)
		}
	}
	start, _ := ToolHistoryEvent(events[0])
	end, _ := ToolHistoryEvent(events[3])
	saved := MergeToolHistory(start, end)
	if saved.ID != events[0].ToolID || saved.Status != ihtml.ChatToolSuccess || saved.Call.Title != "读取资料" {
		t.Fatal(saved)
	}
	if len(p.activeTools) != 0 {
		t.Fatal("returned tool retained as active")
	}
}

func TestInterruptedStartedToolRetiresAsUnknown(t *testing.T) {
	r := newRecorder()
	s := openSession(t, configFor(&fakeEngine{}), "owner", r).(*session)
	defer s.Close()
	p := newProjection(s, t.Context(), func() {})
	p.step(agent.Step{Kind: agent.StepToolStart, ToolCallID: "original", ToolName: "slow_write", ToolTitle: "保存资料"}, nil)
	p.end(nil, errors.New("PRIVATE error"))
	events := r.snapshot()
	if len(events) != 3 || events[1].Type != ihtml.ChatStreamToolResult || events[1].ToolStatus != ihtml.ChatToolUnknown || events[1].ToolID != events[0].ToolID || events[2].Type != ihtml.ChatStreamTurnError {
		t.Fatal(events)
	}
	if strings.Contains(events[1].ToolSummary, "PRIVATE") {
		t.Fatal(events[1])
	}
}

func TestToolStartDoesNotTurnManualProposalIntoExecution(t *testing.T) {
	r := newRecorder()
	s := openSession(t, configFor(&fakeEngine{}), "owner", r).(*session)
	defer s.Close()
	p := newProjection(s, context.Background(), func() {})
	p.approvalTools["write"] = true
	p.step(agent.Step{Kind: agent.StepToolStart, ToolCallID: "proposed", ToolName: "write"}, nil)
	if len(r.snapshot()) != 0 {
		t.Fatal("unconfirmed proposal shown as running")
	}
	p.resumedCard = &ihtml.ProposedToolCall{ID: "original-card", Name: "write"}
	p.resumedToolCallID = "approved"
	p.step(agent.Step{Kind: agent.StepToolStart, ToolCallID: "approved", ToolName: "write", ToolTitle: "保存资料"}, nil)
	p.step(agent.Step{Kind: agent.StepToolCall, ToolCallID: "approved", ToolName: "write", ToolTitle: "保存资料"}, nil)
	events := r.snapshot()
	if len(events) != 2 || events[0].ToolID != "original-card" || events[1].ToolID != "original-card" {
		t.Fatal(events)
	}
}
