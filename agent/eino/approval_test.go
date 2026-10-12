package einoengine

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/schema"
	ai "github.com/zdypro888/ihtml/agent"
)

func TestNativeApprovalResumeAfterEngineRecreation(t *testing.T) {
	for _, approved := range []bool{true, false} {
		name := "reject"
		if approved {
			name = "approve"
		}
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			state := &scriptedModelState{fn: func(input []*schema.Message, _ []*schema.ToolInfo) (*schema.Message, error) {
				for _, m := range input {
					if m.Role == schema.Tool && m.ToolName == "write_value" {
						return schema.AssistantMessage("finished: "+m.Content, nil), nil
					}
				}
				return schema.AssistantMessage("", []schema.ToolCall{{ID: "call-approval", Type: "function", Function: schema.FunctionCall{Name: "write_value", Arguments: `{"value":"alpha"}`}}}), nil
			}}
			runtime := session.NewInMemoryStore[*schema.Message](nil)
			model := &scriptedModel{state: state}
			req := &ai.TurnRequest{SessionID: "42", InvocationScope: "turn-approval", UserText: "write", RequireApproval: true, Tools: []ai.Tool{{Name: "write_value", Effect: ai.ToolEffectWrite, LoadMode: ai.ToolLoadImmediate, ApprovalRequired: true, Handler: func(ctx context.Context, _ json.RawMessage) (string, error) {
				if ai.ToolInvocationKey(ctx) == "" {
					t.Error("missing invocation identity")
				}
				calls.Add(1)
				return "stored", nil
			}}}}
			first, err := newNativeTestEngine(model, runtime).RunTurn(context.Background(), req)
			if err != nil || first == nil || first.PendingApproval == nil {
				t.Fatalf("pause: %+v %v", first, err)
			}
			if first.FinishReason != "awaiting_approval" || calls.Load() != 0 || len(first.PendingApproval.Tools) != 1 {
				t.Fatalf("invalid pause %+v calls=%d", first, calls.Load())
			}
			if len(first.Steps) != 1 || !first.Steps[0].ApprovalPending || first.PendingApproval.Tools[0].ToolCallID != first.Steps[0].ToolCallID {
				t.Fatalf("pending evidence not correlated: %+v", first)
			}
			req.EngineSession = first.EngineSession
			if _, err := newNativeTestEngine(model, runtime).RunTurn(context.Background(), req); err == nil {
				t.Fatal("fresh turn replaced pending checkpoint")
			}
			bypass := *req
			bypass.RequireApproval = false
			if _, err := newNativeTestEngine(model, runtime).RunTurn(context.Background(), &bypass); err == nil {
				t.Fatal("non-approval turn replaced pending checkpoint")
			}
			req.Resume = &ai.ResumeRequest{CheckPointID: first.PendingApproval.CheckPointID, Decisions: map[string]bool{first.PendingApproval.Tools[0].InterruptID: approved}}
			// A new Engine has no process-local selected tools or approval state.
			result, err := newNativeTestEngine(model, runtime).RunTurn(context.Background(), req)
			if err != nil || result == nil || result.PendingApproval != nil || result.Text == "" {
				t.Fatalf("resume: %+v %v", result, err)
			}
			want := int32(0)
			if approved {
				want = 1
			}
			if calls.Load() != want {
				t.Fatalf("calls=%d want=%d", calls.Load(), want)
			}
			found := false
			for _, step := range result.Steps {
				if step.ToolName == "write_value" {
					found = true
					if step.ApprovalRejected == approved || string(step.Args) != `{"value":"alpha"}` {
						t.Fatalf("incorrect resumed evidence %+v", step)
					}
				}
			}
			if !found {
				t.Fatal("resumed tool evidence missing")
			}
			if _, err := newNativeTestEngine(model, runtime).RunTurn(context.Background(), req); err == nil {
				t.Fatal("consumed checkpoint resumed twice")
			}
			if calls.Load() != want {
				t.Fatal("duplicate resume executed handler")
			}
		})
	}
}

func TestNativeApprovalDoesNotApproveUnselectedDeferredTool(t *testing.T) {
	var calls atomic.Int32
	state := &scriptedModelState{fn: func(input []*schema.Message, _ []*schema.ToolInfo) (*schema.Message, error) {
		for _, m := range input {
			if m.Role == schema.Tool && m.ToolName == "write_value" {
				return schema.AssistantMessage("not loaded", nil), nil
			}
		}
		return schema.AssistantMessage("", []schema.ToolCall{{ID: "unloaded", Type: "function", Function: schema.FunctionCall{Name: "write_value", Arguments: `{}`}}}), nil
	}}
	result, err := newNativeTestEngine(&scriptedModel{state: state}, session.NewInMemoryStore[*schema.Message](nil)).RunTurn(context.Background(), &ai.TurnRequest{SessionID: "99", RequireApproval: true, UserText: "write", Tools: []ai.Tool{{Name: "write_value", Effect: ai.ToolEffectWrite, ApprovalRequired: true, Handler: func(context.Context, json.RawMessage) (string, error) { calls.Add(1); return "ok", nil }}}})
	if err != nil || result == nil || result.PendingApproval != nil || calls.Load() != 0 {
		t.Fatalf("unselected call: result=%+v err=%v calls=%d", result, err, calls.Load())
	}
}

func TestNativeApprovalRequiresDurableSession(t *testing.T) {
	engine := newNativeTestEngine(&scriptedModel{state: &scriptedModelState{}}, nil)
	if _, err := engine.RunTurn(context.Background(), &ai.TurnRequest{SessionID: "42", RequireApproval: true}); err == nil {
		t.Fatal("non-durable approval accepted")
	}
}

func TestNativeApprovalDeferredToolResumeAndChangedBinding(t *testing.T) {
	var calls atomic.Int32
	state := &scriptedModelState{fn: func(input []*schema.Message, _ []*schema.ToolInfo) (*schema.Message, error) {
		last := input[len(input)-1]
		if last.Role == schema.Tool && last.ToolName == "write_value" {
			return schema.AssistantMessage("stored", nil), nil
		}
		if last.Role == schema.Tool && last.ToolName == "tool_search" {
			return schema.AssistantMessage("", []schema.ToolCall{{ID: "write", Type: "function", Function: schema.FunctionCall{Name: "write_value", Arguments: `{}`}}}), nil
		}
		return schema.AssistantMessage("", []schema.ToolCall{{ID: "search", Type: "function", Function: schema.FunctionCall{Name: "tool_search", Arguments: `{"query":"select:write_value"}`}}}), nil
	}}
	runtime := session.NewInMemoryStore[*schema.Message](nil)
	model := &scriptedModel{state: state}
	req := &ai.TurnRequest{SessionID: "55", SessionCapability: "alice", RequireApproval: true, UserText: "write", Tools: []ai.Tool{{Name: "write_value", Effect: ai.ToolEffectWrite, ApprovalRequired: true, Handler: func(context.Context, json.RawMessage) (string, error) { calls.Add(1); return "ok", nil }}}}
	first, err := newNativeTestEngine(model, runtime).RunTurn(context.Background(), req)
	if err != nil || first == nil || first.PendingApproval == nil {
		t.Fatalf("pause=%+v err=%v", first, err)
	}
	req.EngineSession = first.EngineSession
	req.Resume = &ai.ResumeRequest{CheckPointID: first.PendingApproval.CheckPointID, Decisions: map[string]bool{first.PendingApproval.Tools[0].InterruptID: true}}
	for _, change := range []func(*ai.TurnRequest){
		func(r *ai.TurnRequest) { r.SessionCapability = "bob" },
		func(r *ai.TurnRequest) { r.SessionID = "56" },
		func(r *ai.TurnRequest) {
			r.Resume = &ai.ResumeRequest{CheckPointID: "other", Decisions: r.Resume.Decisions}
		},
		func(r *ai.TurnRequest) {
			r.Tools = append([]ai.Tool(nil), r.Tools...)
			r.Tools[0].ApprovalRequired = false
		},
	} {
		changed := *req
		change(&changed)
		if _, err := newNativeTestEngine(model, runtime).RunTurn(context.Background(), &changed); err == nil {
			t.Fatal("changed binding accepted")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid resume executed")
	}
	result, err := newNativeTestEngine(model, runtime).RunTurn(context.Background(), req)
	if err != nil || result == nil || result.PendingApproval != nil || calls.Load() != 1 {
		t.Fatalf("deferred resume=%+v err=%v calls=%d", result, err, calls.Load())
	}
}

func TestNativeApprovalUntargetedCallRemainsPaused(t *testing.T) {
	var a, b atomic.Int32
	state := &scriptedModelState{fn: func(input []*schema.Message, _ []*schema.ToolInfo) (*schema.Message, error) {
		results := 0
		for _, m := range input {
			if m.Role == schema.Tool && (m.ToolName == "write_a" || m.ToolName == "write_b") {
				results++
			}
		}
		if results >= 2 {
			return schema.AssistantMessage("done", nil), nil
		}
		return schema.AssistantMessage("", []schema.ToolCall{{ID: "a", Type: "function", Function: schema.FunctionCall{Name: "write_a", Arguments: `{}`}}, {ID: "b", Type: "function", Function: schema.FunctionCall{Name: "write_b", Arguments: `{}`}}}), nil
	}}
	runtime := session.NewInMemoryStore[*schema.Message](nil)
	model := &scriptedModel{state: state}
	req := &ai.TurnRequest{SessionID: "65", RequireApproval: true, UserText: "two writes", Tools: []ai.Tool{
		{Name: "write_a", Effect: ai.ToolEffectWrite, LoadMode: ai.ToolLoadImmediate, ApprovalRequired: true, Handler: func(context.Context, json.RawMessage) (string, error) { a.Add(1); return "a", nil }},
		{Name: "write_b", Effect: ai.ToolEffectWrite, LoadMode: ai.ToolLoadImmediate, ApprovalRequired: true, Handler: func(context.Context, json.RawMessage) (string, error) { b.Add(1); return "b", nil }},
	}}
	first, err := newNativeTestEngine(model, runtime).RunTurn(context.Background(), req)
	if err != nil || first == nil || first.PendingApproval == nil || len(first.PendingApproval.Tools) != 2 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	var id string
	for _, call := range first.PendingApproval.Tools {
		if call.ToolName == "write_a" {
			id = call.InterruptID
		}
	}
	req.EngineSession = first.EngineSession
	req.Resume = &ai.ResumeRequest{CheckPointID: first.PendingApproval.CheckPointID, Decisions: map[string]bool{id: true}}
	second, err := newNativeTestEngine(model, runtime).RunTurn(context.Background(), req)
	if err != nil || second == nil || second.PendingApproval == nil || len(second.PendingApproval.Tools) != 1 || second.PendingApproval.Tools[0].ToolName != "write_b" || a.Load() != 1 || b.Load() != 0 {
		t.Fatalf("second=%+v err=%v a=%d b=%d", second, err, a.Load(), b.Load())
	}
	req.Resume = &ai.ResumeRequest{CheckPointID: second.PendingApproval.CheckPointID, Decisions: map[string]bool{second.PendingApproval.Tools[0].InterruptID: true}}
	final, err := newNativeTestEngine(model, runtime).RunTurn(context.Background(), req)
	if err != nil || final == nil || final.PendingApproval != nil || a.Load() != 1 || b.Load() != 1 {
		t.Fatalf("final=%+v err=%v a=%d b=%d", final, err, a.Load(), b.Load())
	}
}
