package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/agent"
)

type fakeEngine struct {
	run func(context.Context, *agent.TurnRequest) (*agent.TurnResult, error)
}

func (f *fakeEngine) Name() string { return "fake" }
func (f *fakeEngine) RunTurn(ctx context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
	return f.run(ctx, req)
}

type recorder struct {
	mu     sync.Mutex
	events []ihtml.ChatStreamEvent
	notify chan struct{}
}

func newRecorder() *recorder { return &recorder{notify: make(chan struct{}, 100)} }
func (r *recorder) emit(ev ihtml.ChatStreamEvent) error {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
	r.notify <- struct{}{}
	return nil
}
func (r *recorder) snapshot() []ihtml.ChatStreamEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ihtml.ChatStreamEvent(nil), r.events...)
}
func (r *recorder) wait(t *testing.T, kind string, n int) []ihtml.ChatStreamEvent {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		events := r.snapshot()
		count := 0
		for _, ev := range events {
			if ev.Type == kind {
				count++
			}
		}
		if count >= n {
			return events
		}
		select {
		case <-r.notify:
		case <-deadline:
			t.Fatalf("missing %s #%d: %#v", kind, n, events)
		}
	}
}
func configFor(e agent.Engine) Config {
	return Config{Engine: e, Models: &ihtml.ChatModels{ModelReady: true, CurrentModel: "fake", Models: []string{"fake"}},
		PrepareTurn: func(_ context.Context, sc *ihtml.ChatSessionContext, ev *ihtml.ChatClientEvent) (*agent.TurnRequest, error) {
			text := ""
			if len(ev.Messages) > 0 {
				text = ev.Messages[0].Content
			}
			return &agent.TurnRequest{SessionID: sc.User, UserText: text}, nil
		}}
}
func openSession(t *testing.T, cfg Config, user string, r *recorder) ihtml.ChatSession {
	t.Helper()
	b, err := NewChecked(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s, err := b.NewSession(context.Background(), &ihtml.ChatSessionContext{User: user, Emit: r.emit})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}
func send(s ihtml.ChatSession, text string) {
	s.HandleEvent(context.Background(), &ihtml.ChatClientEvent{Type: ihtml.ChatClientUserMessage, Messages: []ihtml.ChatMessage{{Role: "user", Content: text}}})
}
func waitSignal(t *testing.T, c <-chan struct{}) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out")
	}
}

func TestSnapshotResetAndSafeToolProjection(t *testing.T) {
	r := newRecorder()
	e := &fakeEngine{run: func(_ context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
		for _, text := range []string{"he", "hello", "hello", "new", "new answer", ""} {
			req.OnDelta(text)
		}
		req.OnEvent(agent.Step{Kind: agent.StepToolCall, ToolName: "lookup", Args: json.RawMessage(`{"secret":"args"}`), Result: "private-result"})
		req.OnEvent(agent.Step{Kind: agent.StepToolCall, ToolName: "dispatch", Completion: agent.ToolCompletionAsynchronous})
		req.OnEvent(agent.Step{Kind: agent.StepToolCall, ToolName: "failure", Err: "secret-key"})
		return &agent.TurnResult{Text: "authoritative", Usage: agent.Usage{InputTokens: 2, OutputTokens: 3}}, nil
	}}
	s := openSession(t, configFor(e), "alice", r)
	send(s, "hello")
	events := r.wait(t, ihtml.ChatStreamAssistantDone, 1)
	var deltas []string
	resets := 0
	for i, ev := range events {
		if ev.Seq != int64(i+1) || ev.RunID == "" || ev.RunID != events[0].RunID || ev.TurnID != events[0].TurnID {
			t.Fatalf("bad envelope: %#v", ev)
		}
		if ev.Type == ihtml.ChatStreamAssistantDelta {
			deltas = append(deltas, ev.Delta)
		}
		if ev.Type == ihtml.ChatStreamAssistantReset {
			resets++
		}
		if ev.ToolResult != nil || ev.ToolInput != "" || ev.ToolDebug != nil || ev.ToolCall != nil {
			t.Fatal("raw tool fields exposed")
		}
	}
	if !reflect.DeepEqual(deltas, []string{"he", "llo", "new", " answer"}) || resets != 2 {
		t.Fatalf("deltas=%v resets=%d", deltas, resets)
	}
	encoded, _ := json.Marshal(events)
	for _, secret := range []string{"private-result", "secret-key", "args"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("leaked %s", secret)
		}
	}
	last := events[len(events)-1]
	if last.Message != "authoritative" || last.Usage.TotalTokens != 5 {
		t.Fatalf("bad final: %#v", last)
	}
}

func TestQueueCancelAndClose(t *testing.T) {
	started := make(chan struct{})
	var calls atomic.Int32
	e := &fakeEngine{run: func(ctx context.Context, _ *agent.TurnRequest) (*agent.TurnResult, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-ctx.Done()
		return &agent.TurnResult{Text: "not-success"}, ctx.Err()
	}}
	r := newRecorder()
	s := openSession(t, configFor(e), "alice", r)
	send(s, "first")
	waitSignal(t, started)
	send(s, "queued")
	s.HandleEvent(context.Background(), &ihtml.ChatClientEvent{Type: ihtml.ChatClientCancel})
	r.wait(t, ihtml.ChatStreamTurnError, 1)
	s.Close()
	if calls.Load() != 1 {
		t.Fatalf("cancel did not discard queue: %d", calls.Load())
	}
	for _, ev := range r.snapshot() {
		if ev.Type == ihtml.ChatStreamAssistantDone {
			t.Fatal("cancel reported success")
		}
	}
	send(s, "after close")
}

func TestFIFOAndPrepareRunsAfterPreviousCompletion(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var prepared atomic.Int32
	e := &fakeEngine{run: func(ctx context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
		if req.UserText == "first" {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return &agent.TurnResult{Text: req.UserText}, nil
	}}
	cfg := configFor(e)
	prepare := cfg.PrepareTurn
	cfg.PrepareTurn = func(ctx context.Context, sc *ihtml.ChatSessionContext, ev *ihtml.ChatClientEvent) (*agent.TurnRequest, error) {
		prepared.Add(1)
		return prepare(ctx, sc, ev)
	}
	r := newRecorder()
	s := openSession(t, cfg, "alice", r)
	send(s, "first")
	waitSignal(t, started)
	send(s, "second")
	send(s, "third")
	if prepared.Load() != 1 {
		t.Fatal("queued turn prepared before previous completion")
	}
	close(release)
	events := r.wait(t, ihtml.ChatStreamAssistantDone, 3)
	var finals []string
	seqs := map[string]int64{}
	for _, ev := range events {
		if ev.Seq != seqs[ev.RunID]+1 {
			t.Fatalf("bad sequence: %#v", ev)
		}
		seqs[ev.RunID] = ev.Seq
		if ev.Type == ihtml.ChatStreamAssistantDone {
			finals = append(finals, ev.Message)
		}
	}
	if !reflect.DeepEqual(finals, []string{"first", "second", "third"}) || len(seqs) != 3 {
		t.Fatalf("order=%v runs=%d", finals, len(seqs))
	}
}

func TestCloseWaitsForUnwindAndConcurrentClose(t *testing.T) {
	started, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	e := &fakeEngine{run: func(ctx context.Context, _ *agent.TurnRequest) (*agent.TurnResult, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		<-release
		return nil, ctx.Err()
	}}
	r := newRecorder()
	s := openSession(t, configFor(e), "alice", r)
	send(s, "first")
	waitSignal(t, started)
	send(s, "queued")
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	waitSignal(t, cancelled)
	select {
	case <-closed:
		t.Fatal("Close detached running engine")
	default:
	}
	close(release)
	waitSignal(t, closed)
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(s.Close)
	}
	wg.Wait()
	if len(r.snapshot()) != 1 {
		t.Fatalf("events after close: %#v", r.snapshot())
	}
}

func TestQueueFullDoesNotTerminateActiveTurn(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	e := &fakeEngine{run: func(ctx context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
		calls.Add(1)
		if req.UserText == "active" {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return &agent.TurnResult{Text: req.UserText}, nil
	}}
	cfg := configFor(e)
	cfg.MaxPendingTurns = 1
	r := newRecorder()
	s := openSession(t, cfg, "alice", r)
	send(s, "active")
	waitSignal(t, started)
	send(s, "queued")
	send(s, "rejected")
	events := r.wait(t, ihtml.ChatStreamAssistantStatus, 1)
	for _, ev := range events {
		if ev.Type == ihtml.ChatStreamTurnError || ev.Type == ihtml.ChatStreamAssistantDone || ev.RunID != events[0].RunID || ev.TurnID != events[0].TurnID {
			t.Fatalf("queue rejection disrupted active turn: %#v", ev)
		}
	}
	close(release)
	events = r.wait(t, ihtml.ChatStreamAssistantDone, 2)
	var finals []string
	for _, ev := range events {
		if ev.Type == ihtml.ChatStreamAssistantDone {
			finals = append(finals, ev.Message)
		}
		if ev.Type == ihtml.ChatStreamTurnError {
			t.Fatal("queue overflow became terminal")
		}
	}
	if !reflect.DeepEqual(finals, []string{"active", "queued"}) || calls.Load() != 2 {
		t.Fatalf("overflow executed/aborted work: %v calls=%d", finals, calls.Load())
	}
}

func TestQueueRejectionWaitsForTurnStart(t *testing.T) {
	metadataStarted, metadataRelease := make(chan struct{}), make(chan struct{})
	var modelsCalls atomic.Int32
	e := &fakeEngine{run: func(_ context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
		return &agent.TurnResult{Text: req.UserText}, nil
	}}
	cfg := configFor(e)
	cfg.MaxPendingTurns = 1
	cfg.Models = nil
	cfg.ModelsProvider = func(ctx context.Context) (ihtml.ChatModels, error) {
		if modelsCalls.Add(1) == 1 {
			close(metadataStarted)
			select {
			case <-metadataRelease:
			case <-ctx.Done():
				return ihtml.ChatModels{}, ctx.Err()
			}
		}
		return ihtml.ChatModels{ModelReady: true, Models: []string{"fake"}}, nil
	}
	r := newRecorder()
	s := openSession(t, cfg, "alice", r)
	send(s, "active")
	waitSignal(t, metadataStarted)
	send(s, "queued")
	send(s, "rejected")
	if len(r.snapshot()) != 0 {
		t.Fatal("queue rejection emitted before turn_start")
	}
	close(metadataRelease)
	events := r.wait(t, ihtml.ChatStreamAssistantDone, 2)
	if len(events) < 4 || events[0].Type != ihtml.ChatStreamTurnStart || events[1].Type != ihtml.ChatStreamAssistantStatus || events[1].RunID != events[0].RunID || events[1].Seq != 2 {
		t.Fatalf("bad deferred notice: %#v", events)
	}
	for _, ev := range events {
		if !ev.ModelReady {
			t.Fatal("model readiness lost")
		}
	}
}

func TestModelMetadataFailureHasBoundTerminal(t *testing.T) {
	var calls atomic.Int32
	e := &fakeEngine{run: func(context.Context, *agent.TurnRequest) (*agent.TurnResult, error) {
		calls.Add(1)
		return &agent.TurnResult{}, nil
	}}
	cfg := configFor(e)
	cfg.Models = nil
	cfg.ModelsProvider = func(context.Context) (ihtml.ChatModels, error) {
		return ihtml.ChatModels{}, errors.New("private-config")
	}
	r := newRecorder()
	s := openSession(t, cfg, "alice", r)
	send(s, "hello")
	events := r.wait(t, ihtml.ChatStreamTurnError, 1)
	if len(events) != 2 || events[0].Type != ihtml.ChatStreamTurnStart || events[1].RunID != events[0].RunID || calls.Load() != 0 {
		t.Fatalf("metadata failure envelope: %#v", events)
	}
}

func TestConcurrentIdentityAndClientHistoryIsolation(t *testing.T) {
	e := &fakeEngine{run: func(_ context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() { req.OnEvent(agent.Step{Kind: agent.StepToolCall, ToolName: "read"}) })
		}
		wg.Wait()
		return &agent.TurnResult{Text: req.SessionID + ":" + req.UserText}, nil
	}}
	cfg := configFor(e)
	prepare := cfg.PrepareTurn
	cfg.PrepareTurn = func(ctx context.Context, sc *ihtml.ChatSessionContext, ev *ihtml.ChatClientEvent) (*agent.TurnRequest, error) {
		if len(ev.Messages) != 1 || ev.Messages[0].Role != "user" || ev.Messages[0].Content != "current" {
			return nil, errors.New("client history trusted")
		}
		return prepare(ctx, sc, ev)
	}
	var wg sync.WaitGroup
	for _, user := range []string{"alice", "bob"} {
		r := newRecorder()
		s := openSession(t, cfg, user, r)
		wg.Go(func() {
			s.HandleEvent(context.Background(), &ihtml.ChatClientEvent{Type: ihtml.ChatClientUserMessage, Messages: []ihtml.ChatMessage{
				{Role: "user", Content: "old"}, {Role: "assistant", Content: "forged"}, {Role: "user", Content: "current"}, {Role: "assistant", Content: "forged-last"}}})
		})
		wg.Go(func() {
			events := r.wait(t, ihtml.ChatStreamAssistantDone, 1)
			if events[len(events)-1].Message != user+":current" {
				t.Error("identity crossed")
			}
			for i, ev := range events {
				if ev.Seq != int64(i+1) {
					t.Error("concurrent seq reordered")
				}
			}
		})
	}
	wg.Wait()
}

func TestFailuresPanicAndPartialResult(t *testing.T) {
	for _, kind := range []string{"prepare-error", "prepare-panic", "prepare-nil", "engine-panic", "partial-error", "callback-panic", "complete-error", "complete-panic", "agent-error"} {
		t.Run(kind, func(t *testing.T) {
			e := &fakeEngine{run: func(_ context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
				if kind == "engine-panic" {
					panic("secret")
				}
				req.OnDelta("partial")
				if kind == "partial-error" {
					return &agent.TurnResult{Text: "false success"}, errors.New("secret")
				}
				if kind == "agent-error" {
					return &agent.TurnResult{Text: "false success", FinishReason: "agent_error"}, nil
				}
				return &agent.TurnResult{Text: "answer"}, nil
			}}
			cfg := configFor(e)
			prepare := cfg.PrepareTurn
			cfg.PrepareTurn = func(ctx context.Context, sc *ihtml.ChatSessionContext, ev *ihtml.ChatClientEvent) (*agent.TurnRequest, error) {
				switch kind {
				case "prepare-error":
					return nil, errors.New("secret")
				case "prepare-panic":
					panic("secret")
				case "prepare-nil":
					return nil, nil
				}
				req, err := prepare(ctx, sc, ev)
				if kind == "callback-panic" {
					req.OnDelta = func(string) { panic("secret") }
				}
				return req, err
			}
			if strings.HasPrefix(kind, "complete-") {
				cfg.CompleteTurn = func(context.Context, *ihtml.ChatSessionContext, *agent.TurnRequest, *agent.TurnResult, error) error {
					if kind == "complete-panic" {
						panic("secret")
					}
					return errors.New("secret")
				}
			}
			r := newRecorder()
			s := openSession(t, cfg, "alice", r)
			send(s, "test")
			events := r.wait(t, ihtml.ChatStreamTurnError, 1)
			for _, ev := range events {
				if ev.Type == ihtml.ChatStreamAssistantDone || strings.Contains(ev.Error, "secret") {
					t.Fatalf("bad failure: %#v", ev)
				}
			}
		})
	}
}

func TestEmitFailureCancelsEngineAndQueue(t *testing.T) {
	for _, panicEmit := range []bool{false, true} {
		t.Run(string(rune('0'+map[bool]int{false: 0, true: 1}[panicEmit])), func(t *testing.T) {
			cancelled := make(chan struct{})
			started := make(chan struct{})
			release := make(chan struct{})
			e := &fakeEngine{run: func(ctx context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
				close(started)
				<-release
				req.OnDelta("text")
				<-ctx.Done()
				close(cancelled)
				return nil, ctx.Err()
			}}
			b, err := NewChecked(configFor(e))
			if err != nil {
				t.Fatal(err)
			}
			s, err := b.NewSession(context.Background(), &ihtml.ChatSessionContext{User: "alice", Emit: func(ev ihtml.ChatStreamEvent) error {
				if ev.Type == ihtml.ChatStreamAssistantDelta {
					if panicEmit {
						panic("private")
					}
					return errors.New("disconnected")
				}
				return nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(s.Close)
			send(s, "first")
			waitSignal(t, started)
			send(s, "queued")
			close(release)
			waitSignal(t, cancelled)
			s.Close()
		})
	}
}

func TestGovernanceAndExecutionGuard(t *testing.T) {
	for _, mode := range []string{"readonly", "disabled", "no-guard", "guard"} {
		t.Run(mode, func(t *testing.T) {
			var ran, guarded atomic.Int32
			e := &fakeEngine{run: func(ctx context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
				var names []string
				for _, tool := range req.Tools {
					names = append(names, tool.Name)
					_, _ = tool.Handler(ctx, json.RawMessage(`{}`))
				}
				want := map[string][]string{"readonly": {"read"}, "disabled": nil, "no-guard": {"read", "write", "unknown"}, "guard": {"read", "write", "unknown", "approval"}}[mode]
				if !reflect.DeepEqual(names, want) {
					return nil, errors.New("unsafe tool catalog")
				}
				return &agent.TurnResult{Text: "ok"}, nil
			}}
			cfg := configFor(e)
			prepare := cfg.PrepareTurn
			cfg.PrepareTurn = func(ctx context.Context, sc *ihtml.ChatSessionContext, ev *ihtml.ChatClientEvent) (*agent.TurnRequest, error) {
				req, err := prepare(ctx, sc, ev)
				for _, name := range []string{"read", "write", "unknown", "approval"} {
					effect := agent.ToolEffectUnknown
					if name == "read" {
						effect = agent.ToolEffectRead
					}
					if name == "write" {
						effect = agent.ToolEffectWrite
					}
					req.Tools = append(req.Tools, agent.Tool{Name: name, Effect: effect, ApprovalRequired: name == "approval", Handler: func(context.Context, json.RawMessage) (string, error) { ran.Add(1); return "private", nil }})
				}
				return req, err
			}
			if mode == "guard" {
				cfg.ToolExecutionGuard = func(context.Context, *ihtml.ChatSessionContext, agent.Tool, json.RawMessage) error {
					guarded.Add(1)
					return ihtml.ErrAccessDenied
				}
			}
			r := newRecorder()
			b, err := NewChecked(cfg)
			if err != nil {
				t.Fatal(err)
			}
			s, err := b.NewSession(context.Background(), &ihtml.ChatSessionContext{User: "alice", Emit: r.emit, Access: ihtml.Access{ReadOnly: mode == "readonly"}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(s.Close)
			ev := &ihtml.ChatClientEvent{Type: ihtml.ChatClientUserMessage, Messages: []ihtml.ChatMessage{{Role: "user", Content: "test"}}}
			if mode == "disabled" {
				off := false
				ev.ToolsEnabled = &off
			}
			s.HandleEvent(context.Background(), ev)
			r.wait(t, ihtml.ChatStreamAssistantDone, 1)
			if mode == "guard" && (ran.Load() != 0 || guarded.Load() != 4) {
				t.Fatal("guard did not protect execution")
			}
			if mode == "readonly" && ran.Load() != 1 {
				t.Fatal("readonly executed mutation")
			}
			if mode == "disabled" && ran.Load() != 0 {
				t.Fatal("disabled tools ran")
			}
		})
	}
}

func TestCheckedDependenciesAndModels(t *testing.T) {
	e := &fakeEngine{}
	valid := configFor(e)
	var typedNil *fakeEngine
	for _, mutate := range []func(*Config){func(c *Config) { c.Engine = nil }, func(c *Config) { c.Engine = typedNil }, func(c *Config) { c.PrepareTurn = nil }, func(c *Config) { c.Models = nil }, func(c *Config) {
		c.ModelsProvider = func(context.Context) (ihtml.ChatModels, error) { return ihtml.ChatModels{}, nil }
	}, func(c *Config) { c.MaxPendingTurns = -1 }} {
		cfg := valid
		mutate(&cfg)
		if _, err := NewChecked(cfg); err == nil {
			t.Fatal("accepted nil/invalid dependency")
		}
	}
	b, err := NewChecked(valid)
	if err != nil {
		t.Fatal(err)
	}
	for _, sc := range []*ihtml.ChatSessionContext{nil, {}, {User: "alice"}, {User: "alice", Emit: func(ihtml.ChatStreamEvent) error { return nil }, Access: ihtml.Access{ChatDisabled: true}}} {
		if _, err := b.NewSession(context.Background(), sc); err == nil {
			t.Fatal("accepted invalid session")
		}
	}
	valid.Models.Models[0] = "mutated"
	models, err := b.Models(context.Background())
	if err != nil || models.Models[0] != "fake" {
		t.Fatal("models aliased config")
	}
	models.Models[0] = "again"
	models, _ = b.Models(context.Background())
	if models.Models[0] != "fake" {
		t.Fatal("models alias result")
	}
	cfg := configFor(e)
	cfg.Models = nil
	cfg.ModelsProvider = func(context.Context) (ihtml.ChatModels, error) {
		return ihtml.ChatModels{DeepApprovalResume: "checkpoint", PreviewOnly: true}, nil
	}
	b, err = NewChecked(cfg)
	if err != nil {
		t.Fatal(err)
	}
	models, err = b.Models(context.Background())
	if err != nil || models.DeepApprovalResume != "" || !models.PreviewOnly {
		t.Fatal("false approval capability")
	}
	cfg.ModelsProvider = func(context.Context) (ihtml.ChatModels, error) { panic("private") }
	b, _ = NewChecked(cfg)
	if _, err = b.Models(context.Background()); err == nil {
		t.Fatal("provider panic escaped")
	}
}
