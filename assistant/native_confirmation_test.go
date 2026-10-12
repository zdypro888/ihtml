package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/agent"
)

type nativeFakeEngine struct{ *fakeEngine }

func (*nativeFakeEngine) SupportsNativeApproval() bool { return true }

func TestNativeConfirmationPersistsBeforePauseAndResumesOriginal(t *testing.T) {
	var persisted, completed, claimed atomic.Int32
	card := ihtml.ProposedToolCall{ID: "approval-1", Name: "write", RequiresConfirmation: true, ApprovalResume: "checkpoint", ConfirmationToken: "signed", ConfirmationNonce: "nonce"}
	cfg := configFor(&fakeEngine{run: func(_ context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
		if !req.RequireApproval {
			t.Error("approval not enabled")
		}
		if req.Resume == nil {
			return &agent.TurnResult{EngineSession: "original", FinishReason: "awaiting_approval", PendingApproval: &agent.PendingApproval{CheckPointID: "checkpoint", Tools: []agent.ApprovalTool{{InterruptID: "point", ToolName: "write", Args: []byte(`{}`)}}}}, nil
		}
		if req.EngineSession != "original" || req.InvocationScope != "same-turn" || req.Resume.CheckPointID != "checkpoint" {
			t.Error("original binding lost")
		}
		return &agent.TurnResult{Text: "continued", EngineSession: "original"}, nil
	}})
	cfg.Engine = &nativeFakeEngine{cfg.Engine.(*fakeEngine)}
	cfg.ToolExecutionGuard = func(context.Context, *ihtml.ChatSessionContext, agent.Tool, json.RawMessage) error { return nil }
	cfg.CompleteTurn = func(context.Context, *ihtml.ChatSessionContext, *agent.TurnRequest, *agent.TurnResult, error) error {
		completed.Add(1)
		return nil
	}
	cfg.NativeConfirmation = &NativeConfirmationConfig{
		Pause: func(context.Context, *ihtml.ChatSessionContext, *agent.TurnRequest, *agent.TurnResult) ([]ihtml.ProposedToolCall, error) {
			persisted.Add(1)
			return []ihtml.ProposedToolCall{card}, nil
		},
		PrepareResume: func(_ context.Context, _ *ihtml.ChatSessionContext, c ihtml.ProposedToolCall, approved bool) (*agent.TurnRequest, error) {
			if c.ConfirmationToken != "signed" || !claimed.CompareAndSwap(0, 1) {
				return nil, errors.New("invalid or repeated consent")
			}
			return &agent.TurnRequest{RequireApproval: true, EngineSession: "original", InvocationScope: "same-turn", Resume: &agent.ResumeRequest{CheckPointID: "checkpoint", ToolCallID: "call-1", Decisions: map[string]bool{"point": approved}}}, nil
		},
		Pending: func(context.Context, *ihtml.ChatSessionContext) ([]ihtml.ProposedToolCall, error) { return nil, nil },
	}
	b, err := NewChecked(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r := newRecorder()
	s, err := b.NewSession(context.Background(), &ihtml.ChatSessionContext{User: "alice", Emit: func(ev ihtml.ChatStreamEvent) error {
		if ev.Type == ihtml.ChatStreamConfirmationRequired && persisted.Load() != 1 {
			t.Error("card exposed before persistence")
		}
		return r.emit(ev)
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	send(s, "write")
	events := r.wait(t, ihtml.ChatStreamTurnPaused, 1)
	if completed.Load() != 0 {
		t.Fatal("paused execution completed")
	}
	for _, ev := range events {
		if ev.Type == ihtml.ChatStreamAssistantDone {
			t.Fatal("false completion")
		}
	}
	s.HandleEvent(context.Background(), &ihtml.ChatClientEvent{Type: ihtml.ChatClientConfirmTool, ToolCall: &card})
	r.wait(t, ihtml.ChatStreamAssistantDone, 1)
	if completed.Load() != 1 {
		t.Fatalf("completion count=%d", completed.Load())
	}
	s.HandleEvent(context.Background(), &ihtml.ChatClientEvent{Type: ihtml.ChatClientConfirmTool, ToolCall: &card})
	r.wait(t, ihtml.ChatStreamTurnError, 1)
}

func TestNativeConfirmationRestoresAndContainsPendingPanic(t *testing.T) {
	for _, panicHook := range []bool{false, true} {
		cfg := configFor(&fakeEngine{run: func(context.Context, *agent.TurnRequest) (*agent.TurnResult, error) {
			t.Error("restore ran model")
			return nil, nil
		}})
		cfg.Engine = &nativeFakeEngine{cfg.Engine.(*fakeEngine)}
		cfg.ToolExecutionGuard = func(context.Context, *ihtml.ChatSessionContext, agent.Tool, json.RawMessage) error { return nil }
		cfg.CompleteTurn = func(context.Context, *ihtml.ChatSessionContext, *agent.TurnRequest, *agent.TurnResult, error) error {
			return nil
		}
		cfg.NativeConfirmation = &NativeConfirmationConfig{Pause: func(context.Context, *ihtml.ChatSessionContext, *agent.TurnRequest, *agent.TurnResult) ([]ihtml.ProposedToolCall, error) {
			return nil, nil
		}, PrepareResume: func(context.Context, *ihtml.ChatSessionContext, ihtml.ProposedToolCall, bool) (*agent.TurnRequest, error) {
			return nil, nil
		}, Pending: func(context.Context, *ihtml.ChatSessionContext) ([]ihtml.ProposedToolCall, error) {
			if panicHook {
				panic("host failure")
			}
			return []ihtml.ProposedToolCall{{ID: "restored", Name: "write", RequiresConfirmation: true, ApprovalResume: "checkpoint", ConfirmationToken: "signed", ConfirmationNonce: "nonce"}}, nil
		}}
		b, err := NewChecked(cfg)
		if err != nil {
			t.Fatal(err)
		}
		r := newRecorder()
		s, err := b.NewSession(context.Background(), &ihtml.ChatSessionContext{User: "alice", Emit: r.emit})
		if err != nil {
			t.Fatal(err)
		}
		kind := ihtml.ChatStreamTurnPaused
		if panicHook {
			kind = ihtml.ChatStreamTurnError
		}
		events := r.wait(t, kind, 1)
		s.Close()
		if !panicHook && !events[0].RestoredApproval {
			t.Fatal("restore not marked")
		}
	}
}

func TestNativePauseFailureRunsHostCleanup(t *testing.T) {
	for _, panics := range []bool{false, true} {
		var cleanup atomic.Int32
		cfg := configFor(&nativeFakeEngine{&fakeEngine{run: func(context.Context, *agent.TurnRequest) (*agent.TurnResult, error) {
			return &agent.TurnResult{PendingApproval: &agent.PendingApproval{CheckPointID: "cp"}}, nil
		}}})
		cfg.ToolExecutionGuard = func(context.Context, *ihtml.ChatSessionContext, agent.Tool, json.RawMessage) error { return nil }
		cfg.CompleteTurn = func(_ context.Context, _ *ihtml.ChatSessionContext, _ *agent.TurnRequest, _ *agent.TurnResult, err error) error {
			if err == nil {
				t.Error("failed Pause completed successfully")
			}
			cleanup.Add(1)
			return nil
		}
		cfg.NativeConfirmation = &NativeConfirmationConfig{
			Pause: func(context.Context, *ihtml.ChatSessionContext, *agent.TurnRequest, *agent.TurnResult) ([]ihtml.ProposedToolCall, error) {
				if panics {
					panic("storage failed")
				}
				return nil, errors.New("storage failed")
			},
			PrepareResume: func(context.Context, *ihtml.ChatSessionContext, ihtml.ProposedToolCall, bool) (*agent.TurnRequest, error) {
				return nil, nil
			},
			Pending: func(context.Context, *ihtml.ChatSessionContext) ([]ihtml.ProposedToolCall, error) { return nil, nil },
		}
		b, err := NewChecked(cfg)
		if err != nil {
			t.Fatal(err)
		}
		r := newRecorder()
		s, err := b.NewSession(context.Background(), &ihtml.ChatSessionContext{User: "alice", Emit: r.emit})
		if err != nil {
			t.Fatal(err)
		}
		send(s, "write")
		r.wait(t, ihtml.ChatStreamTurnError, 1)
		s.Close()
		if cleanup.Load() != 1 {
			t.Fatalf("cleanup=%d", cleanup.Load())
		}
	}
}

func TestNativeResumedCardMatchesExactParallelInvocation(t *testing.T) {
	r := newRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &session{ctx: ctx, cancel: cancel, sctx: ihtml.ChatSessionContext{Emit: r.emit}}
	p := newProjection(s, ctx, cancel)
	p.resumedCard = &ihtml.ProposedToolCall{ID: "card-b", Name: "write"}
	p.resumedToolCallID = "call-b"
	// Alpha12 may replay A's completed output before B's rejection.
	p.step(agent.Step{Kind: agent.StepToolCall, ToolName: "write", ToolCallID: "call-a", Args: []byte(`{"value":1}`), Result: "stored"}, nil)
	p.step(agent.Step{Kind: agent.StepToolCall, ToolName: "write", ToolCallID: "call-b", Args: []byte(`{"value":2}`), ApprovalRejected: true}, nil)
	events := r.snapshot()
	if len(events) != 2 || events[0].ToolCall != nil || events[0].ToolID == "card-b" {
		t.Fatalf("unrelated output consumed card: %+v", events)
	}
	if events[1].ToolID != "card-b" || events[1].ToolStatus != ihtml.ChatToolCancelled {
		t.Fatalf("rejection lost exact card binding: %+v", events[1])
	}
}
