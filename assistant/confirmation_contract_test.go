package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/agent"
	"github.com/zdypro888/ihtml/confirmation"
)

func TestConfirmationContractStoredArgsAndConnectionBoundary(t *testing.T) {
	var executed atomic.Int32
	var receipts atomic.Int32
	var storedKey atomic.Value
	engine := &fakeEngine{run: func(ctx context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
		args := json.RawMessage(`{"secret":"server-only"}`)
		value, err := req.Tools[0].Handler(agent.WithToolInvocationKey(ctx, "runtime-key"), args)
		if err != nil {
			return nil, err
		}
		if strings.Contains(value, "secret") || strings.Contains(value, "confirmation_token") {
			t.Errorf("model received private arguments or confirmation credentials: %s", value)
		}
		req.OnEvent(agent.Step{Kind: agent.StepToolCall, ToolName: "write", Args: args, Result: value})
		return &agent.TurnResult{Text: "Awaiting confirmation."}, nil
	}}
	cfg := configFor(engine)
	prepare := cfg.PrepareTurn
	cfg.PrepareTurn = func(ctx context.Context, sc *ihtml.ChatSessionContext, ev *ihtml.ChatClientEvent) (*agent.TurnRequest, error) {
		req, err := prepare(ctx, sc, ev)
		req.InvocationScope = "host-turn"
		req.Tools = []agent.Tool{{Name: "write", Effect: agent.ToolEffectWrite, ApprovalRequired: true,
			Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
				if string(args) != `{"secret":"server-only"}` {
					t.Errorf("handler did not receive stored original args: %s", args)
				}
				storedKey.Store(agent.ToolInvocationKey(ctx))
				executed.Add(1)
				return "private-result", nil
			}}}
		return req, err
	}
	cfg.ToolExecutionGuard = func(context.Context, *ihtml.ChatSessionContext, agent.Tool, json.RawMessage) error { return nil }
	signer, err := confirmation.NewChecked(confirmation.Config{})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Confirmation = &ConfirmationConfig{Signer: signer,
		Scope: func(_ context.Context, _ *ihtml.ChatSessionContext, req *agent.TurnRequest) (confirmation.Scope, error) {
			return confirmation.NewScope(req.SessionID, "host-write"), nil
		},
		Preview: func(context.Context, *ihtml.ChatSessionContext, agent.Tool, json.RawMessage) (ihtml.ProposedToolCall, error) {
			return ihtml.ProposedToolCall{Title: "Public update", Risk: "high", Body: map[string]any{"summary": "redacted"}}, nil
		}}
	cfg.CompleteTool = func(_ context.Context, _ *ihtml.ChatSessionContext, result *ConfirmedToolResult) error {
		if result.Err != nil || result.Result != "private-result" || result.InvocationScope != "host-turn" {
			t.Errorf("bad actual execution evidence: %#v", result)
		}
		receipts.Add(1)
		return nil
	}
	first := newRecorder()
	s := openSession(t, cfg, "alice", first)
	send(s, "propose")
	events := first.wait(t, ihtml.ChatStreamAssistantDone, 1)
	var card *ihtml.ProposedToolCall
	for _, ev := range events {
		if ev.Type == ihtml.ChatStreamConfirmationRequired {
			card = ev.ToolCall
		}
		if ev.Type == ihtml.ChatStreamToolResult {
			t.Fatal("pending invocation falsely projected as completed")
		}
	}
	if card == nil || executed.Load() != 0 {
		t.Fatal("missing pending card or handler ran before confirmation")
	}
	second := newRecorder()
	other := openSession(t, cfg, "alice", second)
	other.HandleEvent(context.Background(), &ihtml.ChatClientEvent{Type: ihtml.ChatClientConfirmTool, ToolCall: card})
	second.wait(t, ihtml.ChatStreamTurnError, 1)
	if executed.Load() != 0 {
		t.Fatal("card crossed connection boundary")
	}
	tampered, err := cloneToolCall(*card)
	if err != nil {
		t.Fatal(err)
	}
	tampered.Body = map[string]any{"summary": "changed"}
	s.HandleEvent(context.Background(), &ihtml.ChatClientEvent{Type: ihtml.ChatClientConfirmTool, ToolCall: &tampered})
	first.wait(t, ihtml.ChatStreamTurnError, 1)
	if executed.Load() != 0 {
		t.Fatal("tampered card executed")
	}
	s.HandleEvent(context.Background(), &ihtml.ChatClientEvent{Type: ihtml.ChatClientConfirmTool, ToolCall: card})
	events = first.wait(t, ihtml.ChatStreamAssistantDone, 2)
	if executed.Load() != 1 || receipts.Load() != 1 || storedKey.Load() != "runtime-key" {
		t.Fatalf("execution identity or receipt lost: calls=%d receipts=%d key=%v", executed.Load(), receipts.Load(), storedKey.Load())
	}
	encoded, _ := json.Marshal(events)
	if strings.Contains(string(encoded), "server-only") || strings.Contains(string(encoded), "private-result") {
		t.Fatal("private execution evidence exposed to browser")
	}
	s.HandleEvent(context.Background(), &ihtml.ChatClientEvent{Type: ihtml.ChatClientConfirmTool, ToolCall: card})
	first.wait(t, ihtml.ChatStreamTurnError, 2)
	if executed.Load() != 1 {
		t.Fatal("confirmation replay executed")
	}
}
