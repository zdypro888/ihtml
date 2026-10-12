package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/agent"
	"github.com/zdypro888/ihtml/confirmation"
)

func approvalConfig(t *testing.T, handler func(context.Context, json.RawMessage) (string, error)) Config {
	t.Helper()
	signer, err := confirmation.NewChecked(confirmation.Config{Secret: []byte("approval tests")})
	if err != nil {
		t.Fatal(err)
	}
	e := &fakeEngine{run: func(ctx context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
		if req.UserText != "propose" {
			return &agent.TurnResult{Text: "plain"}, nil
		}
		args := json.RawMessage(`{"secret":"original"}`)
		result, err := req.Tools[0].Handler(agent.WithToolInvocationKey(ctx, "invocation-secret"), args)
		req.OnEvent(agent.Step{Kind: agent.StepToolCall, ToolName: req.Tools[0].Name, Args: args, Result: result, Err: func() string {
			if err != nil {
				return err.Error()
			}
			return ""
		}()})
		return &agent.TurnResult{Text: "Proposal only; not executed."}, err
	}}
	cfg := configFor(e)
	cfg.PrepareTurn = func(_ context.Context, sc *ihtml.ChatSessionContext, ev *ihtml.ChatClientEvent) (*agent.TurnRequest, error) {
		text := ""
		if len(ev.Messages) > 0 {
			text = ev.Messages[0].Content
		}
		return &agent.TurnRequest{SessionID: sc.User, InvocationScope: "host-turn", UserText: text,
			Tools: []agent.Tool{{Name: "destroy", Effect: agent.ToolEffectWrite, ApprovalRequired: true, Handler: handler}}}, nil
	}
	cfg.ToolExecutionGuard = func(context.Context, *ihtml.ChatSessionContext, agent.Tool, json.RawMessage) error { return nil }
	cfg.Confirmation = &ConfirmationConfig{Signer: signer, ToolTimeout: 200 * time.Millisecond,
		Scope: func(_ context.Context, sc *ihtml.ChatSessionContext, req *agent.TurnRequest) (confirmation.Scope, error) {
			return confirmation.NewScope(req.SessionID, "capability"), nil
		},
		Preview: func(context.Context, *ihtml.ChatSessionContext, agent.Tool, json.RawMessage) (ihtml.ProposedToolCall, error) {
			return ihtml.ProposedToolCall{Title: "Delete record", Method: "TOOL", Path: "ui://destroy", Risk: "high", Body: map[string]any{"record": "public"}}, nil
		},
	}
	return cfg
}

func proposedCard(t *testing.T, s ihtml.ChatSession, r *recorder) ihtml.ProposedToolCall {
	t.Helper()
	send(s, "propose")
	events := r.wait(t, ihtml.ChatStreamAssistantDone, 1)
	for _, ev := range events {
		if ev.Type == ihtml.ChatStreamConfirmationRequired {
			return *ev.ToolCall
		}
	}
	t.Fatal("no card")
	return ihtml.ProposedToolCall{}
}

func confirmCard(s ihtml.ChatSession, c ihtml.ProposedToolCall) {
	s.HandleEvent(context.Background(), &ihtml.ChatClientEvent{Type: ihtml.ChatClientConfirmTool, ToolCall: &c})
}
func dismissCard(s ihtml.ChatSession, c ihtml.ProposedToolCall) {
	s.HandleEvent(context.Background(), &ihtml.ChatClientEvent{Type: ihtml.ChatClientCancelTool, ToolCall: &c})
}

func TestConfirmationInvalidThenValidAndSafeEvidence(t *testing.T) {
	r := newRecorder()
	var calls atomic.Int32
	var persisted atomic.Int32
	cfg := approvalConfig(t, func(ctx context.Context, args json.RawMessage) (string, error) {
		calls.Add(1)
		if agent.ToolInvocationKey(ctx) != "invocation-secret" {
			t.Error("invocation key lost")
		}
		if string(args) != `{"secret":"original"}` {
			t.Error("untrusted args executed", string(args))
		}
		return "PRIVATE_RESULT", nil
	})
	cfg.CompleteTool = func(_ context.Context, _ *ihtml.ChatSessionContext, result *ConfirmedToolResult) error {
		if !result.Started || result.Err != nil || result.Result != "PRIVATE_RESULT" || result.InvocationScope != "host-turn" {
			t.Error("invalid evidence", result)
		}
		persisted.Add(1)
		return nil
	}
	s := openSession(t, cfg, "alice", r)
	c := proposedCard(t, s, r)
	if calls.Load() != 0 {
		t.Fatal("executed before approval")
	}
	for _, ev := range r.snapshot() {
		if ev.Type == ihtml.ChatStreamToolResult {
			t.Fatal("pending card overwritten")
		}
	}
	bad := c
	bad.Body = "tamper"
	confirmCard(s, bad)
	r.wait(t, ihtml.ChatStreamTurnError, 1)
	if calls.Load() != 0 {
		t.Fatal("tampered card executed")
	}
	confirmCard(s, c)
	events := r.wait(t, ihtml.ChatStreamAssistantDone, 2)
	if calls.Load() != 1 || persisted.Load() != 1 {
		t.Fatal(calls.Load(), persisted.Load())
	}
	raw, _ := json.Marshal(events)
	if strings.Contains(string(raw), "PRIVATE_RESULT") || strings.Contains(string(raw), "invocation-secret") || strings.Contains(string(raw), "original") {
		t.Fatal("private execution data projected", string(raw))
	}
	starts := 0
	lastRun := ""
	seq := int64(0)
	for _, ev := range events {
		if ev.Type == ihtml.ChatStreamTurnStart {
			starts++
			lastRun = ev.RunID
			seq = 0
			if !ev.ModelReady {
				t.Fatal("live readiness lost")
			}
		}
		if ev.RunID != lastRun || ev.Seq != seq+1 {
			t.Fatal("bad serial envelope", ev)
		}
		seq = ev.Seq
	}
	if starts != 3 {
		t.Fatal(starts)
	}
	confirmCard(s, c)
	r.wait(t, ihtml.ChatStreamTurnError, 2)
	if calls.Load() != 1 {
		t.Fatal("replay executed")
	}
}

func TestConfirmationCancelConfirmRace(t *testing.T) {
	for i := 0; i < 12; i++ {
		r := newRecorder()
		var calls atomic.Int32
		cfg := approvalConfig(t, func(context.Context, json.RawMessage) (string, error) { calls.Add(1); return "ok", nil })
		s := openSession(t, cfg, "alice", r)
		c := proposedCard(t, s, r)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); confirmCard(s, c) }()
		go func() { defer wg.Done(); dismissCard(s, c) }()
		wg.Wait()
		r.wait(t, ihtml.ChatStreamCancelToolResult, 1)
		deadline := time.After(3 * time.Second)
		for {
			events := r.snapshot()
			terminal := 0
			for _, ev := range events {
				if ev.Type == ihtml.ChatStreamAssistantDone || ev.Type == ihtml.ChatStreamTurnError {
					terminal++
				}
			}
			if terminal >= 2 {
				break
			}
			select {
			case <-r.notify:
			case <-deadline:
				t.Fatal("no confirm terminal")
			}
		}
		cancelled := false
		for _, ev := range r.snapshot() {
			if ev.Type == ihtml.ChatStreamCancelToolResult {
				cancelled = ev.ToolStatus == ihtml.ChatToolCancelled
				if ev.ToolID != c.ID || ev.ConfirmationNonce != c.ConfirmationNonce {
					t.Fatal("uncorrelated ack")
				}
			}
		}
		if calls.Load() > 1 || (cancelled && calls.Load() != 0) || (!cancelled && calls.Load() != 1) {
			t.Fatal("CAS race", calls.Load(), cancelled)
		}
		s.Close()
	}
}

func TestConfirmationLateRevocationCompleteFailureAndTimeout(t *testing.T) {
	for _, scenario := range []string{"guard", "timeout", "complete", "panic", "guard-panic", "complete-panic"} {
		t.Run(scenario, func(t *testing.T) {
			r := newRecorder()
			var calls atomic.Int32
			var evidence *ConfirmedToolResult
			cfg := approvalConfig(t, func(ctx context.Context, _ json.RawMessage) (string, error) {
				calls.Add(1)
				if scenario == "panic" {
					panic("PRIVATE_ERROR")
				}
				if scenario == "timeout" {
					<-ctx.Done()
					return "partial PRIVATE_RESULT", ctx.Err()
				}
				return "PRIVATE_RESULT", nil
			})
			cfg.ToolExecutionGuard = func(context.Context, *ihtml.ChatSessionContext, agent.Tool, json.RawMessage) error {
				if scenario == "guard" {
					return errors.New("PRIVATE_ERROR")
				}
				if scenario == "guard-panic" {
					panic("PRIVATE_ERROR")
				}
				return nil
			}
			cfg.CompleteTool = func(_ context.Context, _ *ihtml.ChatSessionContext, r *ConfirmedToolResult) error {
				copy := *r
				evidence = &copy
				if scenario == "complete" {
					return errors.New("PRIVATE_ERROR")
				}
				if scenario == "complete-panic" {
					panic("PRIVATE_ERROR")
				}
				return nil
			}
			s := openSession(t, cfg, "alice", r)
			c := proposedCard(t, s, r)
			confirmCard(s, c)
			events := r.wait(t, ihtml.ChatStreamTurnError, 1)
			if evidence == nil {
				t.Fatal("no execution evidence")
			}
			pre := scenario == "guard" || scenario == "guard-panic"
			if evidence.Started == pre {
				t.Fatal("incorrect started state", evidence.Started)
			}
			if pre && calls.Load() != 0 {
				t.Fatal("revoked tool ran")
			}
			for _, ev := range events {
				if ev.Type == ihtml.ChatStreamAssistantDone && ev.FinishReason == "manual_tool_only" {
					t.Fatal("false success")
				}
				if strings.Contains(ev.Error+ev.ToolSummary, "PRIVATE") {
					t.Fatal("raw failure leaked")
				}
			}
		})
	}
}

func TestConfirmationQueueFullAndCancelReleasesQueuedFlags(t *testing.T) {
	for _, scenario := range []string{"full", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			r := newRecorder()
			var calls atomic.Int32
			entered := make(chan struct{})
			release := make(chan struct{})
			cfg := approvalConfig(t, func(context.Context, json.RawMessage) (string, error) { calls.Add(1); return "ok", nil })
			cfg.MaxPendingTurns = 1
			base := cfg.Engine.(*fakeEngine).run
			cfg.Engine = &fakeEngine{run: func(ctx context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
				if req.UserText == "block" {
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
					return &agent.TurnResult{Text: "active survived"}, nil
				}
				return base(ctx, req)
			}}
			s := openSession(t, cfg, "alice", r)
			c := proposedCard(t, s, r)
			send(s, "block")
			waitSignal(t, entered)
			if scenario == "full" {
				send(s, "queued")
			}
			confirmCard(s, c)
			if scenario == "cancel" {
				s.HandleEvent(context.Background(), &ihtml.ChatClientEvent{Type: ihtml.ChatClientCancel})
			} else {
				close(release)
			}
			r.wait(t, ihtml.ChatStreamTurnError, 1)
			if calls.Load() != 0 {
				t.Fatal("dropped/rejected confirmation executed")
			}
			ss := s.(*session)
			ss.pendingMu.Lock()
			queued := ss.pendingTools[c.ConfirmationNonce].queued
			ss.pendingMu.Unlock()
			if queued {
				t.Fatal("stuck queued flag")
			}
			if scenario == "full" {
				events := r.wait(t, ihtml.ChatStreamAssistantDone, 3)
				activeRun := ""
				for _, ev := range events {
					if ev.Type == ihtml.ChatStreamAssistantDone && ev.Message == "active survived" {
						activeRun = ev.RunID
					}
				}
				if activeRun == "" {
					t.Fatal("active interrupted")
				}
				for _, ev := range events {
					if ev.Type == ihtml.ChatStreamTurnError && ev.RunID == activeRun {
						t.Fatal("rejection terminated active")
					}
				}
			}
			confirmCard(s, c)
			r.wait(t, ihtml.ChatStreamToolStart, 1)
			r.wait(t, ihtml.ChatStreamAssistantDone, func() int {
				if scenario == "full" {
					return 4
				}
				return 2
			}())
			if calls.Load() != 1 {
				t.Fatal("valid retry ignored")
			}
		})
	}
}

func TestConfirmationCloseRevokesAndReconnectRejects(t *testing.T) {
	r := newRecorder()
	var calls atomic.Int32
	cfg := approvalConfig(t, func(context.Context, json.RawMessage) (string, error) { calls.Add(1); return "ok", nil })
	s := openSession(t, cfg, "alice", r)
	c := proposedCard(t, s, r)
	s.Close()
	if ok, err := cfg.Confirmation.Signer.Consume(context.Background(), "alice", confirmation.NewScope("alice", "capability"), c); ok || err != nil {
		t.Fatal("close did not revoke", ok, err)
	}
	nextRecorder := newRecorder()
	next := openSession(t, cfg, "alice", nextRecorder)
	confirmCard(next, c)
	events := nextRecorder.wait(t, ihtml.ChatStreamTurnError, 1)
	if calls.Load() != 0 {
		t.Fatal("disconnected card executed")
	}
	if events[0].Type != ihtml.ChatStreamTurnStart || events[len(events)-2].ToolCall.ID != c.ID {
		t.Fatal("missing busy termination correlation")
	}
}

func TestConfirmationValidationLimitsAndPanics(t *testing.T) {
	for _, scenario := range []string{"blocked", "args", "preview", "metadata", "scope-panic", "preview-panic", "scope-error", "preview-error"} {
		t.Run(scenario, func(t *testing.T) {
			r := newRecorder()
			cfg := approvalConfig(t, func(context.Context, json.RawMessage) (string, error) { t.Fatal("executed"); return "", nil })
			if scenario == "args" {
				cfg.Engine = &fakeEngine{run: func(ctx context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
					_, err := req.Tools[0].Handler(ctx, json.RawMessage(`{"x":"`+strings.Repeat("x", 64<<10)+`"}`))
					return nil, err
				}}
			}
			preview := cfg.Confirmation.Preview
			cfg.Confirmation.Preview = func(ctx context.Context, sc *ihtml.ChatSessionContext, tool agent.Tool, args json.RawMessage) (ihtml.ProposedToolCall, error) {
				if scenario == "preview-panic" {
					panic("PRIVATE")
				}
				if scenario == "preview-error" {
					return ihtml.ProposedToolCall{}, errors.New("PRIVATE")
				}
				c, err := preview(ctx, sc, tool, args)
				switch scenario {
				case "blocked":
					c.Risk = "blocked"
				case "preview":
					c.Body = strings.Repeat("x", 256<<10)
				case "metadata":
					c.Title = strings.Repeat("x", 1025)
				}
				return c, err
			}
			if scenario == "scope-panic" {
				cfg.Confirmation.Scope = func(context.Context, *ihtml.ChatSessionContext, *agent.TurnRequest) (confirmation.Scope, error) {
					panic("PRIVATE")
				}
			}
			if scenario == "scope-error" {
				cfg.Confirmation.Scope = func(context.Context, *ihtml.ChatSessionContext, *agent.TurnRequest) (confirmation.Scope, error) {
					return confirmation.Scope{}, errors.New("PRIVATE")
				}
			}
			s := openSession(t, cfg, "alice", r)
			send(s, "propose")
			r.wait(t, ihtml.ChatStreamTurnError, 1)
			for _, ev := range r.snapshot() {
				if ev.Type == ihtml.ChatStreamConfirmationRequired {
					t.Fatal("invalid card issued")
				}
			}
			ss := s.(*session)
			ss.pendingMu.Lock()
			n := len(ss.pendingTools)
			ss.pendingMu.Unlock()
			if n != 0 {
				t.Fatal("invalid pending retained")
			}
		})
	}
	cfg := approvalConfig(t, func(context.Context, json.RawMessage) (string, error) { return "", nil })
	for _, mutate := range []func(*Config){func(c *Config) { c.ToolExecutionGuard = nil }, func(c *Config) { c.Confirmation.Signer = nil }, func(c *Config) { c.Confirmation.Signer = &confirmation.Signer{} }, func(c *Config) { c.Confirmation.Scope = nil }, func(c *Config) { c.Confirmation.Preview = nil }, func(c *Config) { c.Confirmation.MaxPendingTools = 257 }, func(c *Config) { c.Confirmation.ToolTimeout = -1 }} {
		copy := cfg
		cc := *cfg.Confirmation
		copy.Confirmation = &cc
		mutate(&copy)
		if _, err := NewChecked(copy); err == nil {
			t.Fatal("invalid dependency accepted")
		}
	}
	b, err := NewChecked(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Confirmation.Scope = nil
	cfg.Confirmation.MaxPendingTools = 999
	if b.cfg.Confirmation.Scope == nil || b.cfg.Confirmation.MaxPendingTools == 999 {
		t.Fatal("configuration aliased")
	}
}
