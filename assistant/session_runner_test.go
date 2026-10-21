package assistant

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zdypro888/ihtml"
)

func runnerEvent(text string) *ihtml.ChatClientEvent {
	return &ihtml.ChatClientEvent{Type: ihtml.ChatClientUserMessage, Messages: []ihtml.ChatMessage{{Role: "user", Content: text}}}
}
func runnerReceive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("session runner did not make progress")
		var zero T
		return zero
	}
}
func runnerSubmit(t *testing.T, r *SessionRunner, ctx context.Context, event *ihtml.ChatClientEvent) {
	t.Helper()
	if err := r.Submit(ctx, event); err != nil {
		t.Fatal(err)
	}
}

func TestSessionRunnerStopClearsPendingAndKeepsConnection(t *testing.T) {
	entered := make(chan string, 8)
	causes := make(chan error, 1)
	r, err := NewSessionRunner(t.Context(), SessionRunnerConfig{Run: func(ctx context.Context, ev *ihtml.ChatClientEvent) {
		text := ev.Messages[0].Content
		entered <- text
		if text == "A" {
			<-ctx.Done()
			causes <- context.Cause(ctx)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	runnerSubmit(t, r, t.Context(), runnerEvent("A"))
	if runnerReceive(t, entered) != "A" {
		t.Fatal("first event mismatch")
	}
	runnerSubmit(t, r, t.Context(), runnerEvent("B"))
	runnerSubmit(t, r, t.Context(), runnerEvent("C"))
	r.Stop()
	if !errors.Is(runnerReceive(t, causes), ErrTurnStopped) {
		t.Fatal("stop cause lost")
	}
	runnerSubmit(t, r, t.Context(), runnerEvent("D"))
	if runnerReceive(t, entered) != "D" {
		t.Fatal("queued command survived stop")
	}
	if r.Context().Err() != nil {
		t.Fatal("stop closed the connection")
	}
}

func TestSessionRunnerParentsKeepTheirOwnCancellationCause(t *testing.T) {
	entered := make(chan string, 8)
	causes := make(chan error, 1)
	root, cancelRoot := context.WithCancelCause(t.Context())
	defer cancelRoot(nil)
	r, err := NewSessionRunner(root, SessionRunnerConfig{Run: func(ctx context.Context, ev *ihtml.ChatClientEvent) {
		text := ev.Messages[0].Content
		entered <- text
		if text == "A" {
			<-ctx.Done()
			causes <- context.Cause(ctx)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	parent, cancel := context.WithCancelCause(t.Context())
	runnerSubmit(t, r, parent, runnerEvent("A"))
	runnerReceive(t, entered)
	queued, cancelQueued := context.WithCancel(t.Context())
	runnerSubmit(t, r, queued, runnerEvent("B"))
	cancelQueued()
	parentCause := errors.New("caller left")
	cancel(parentCause)
	if !errors.Is(runnerReceive(t, causes), parentCause) {
		t.Fatal("parent cancellation classified as user stop")
	}
	runnerSubmit(t, r, t.Context(), runnerEvent("D"))
	if runnerReceive(t, entered) != "D" {
		t.Fatal("cancelled pending parent ran")
	}
	rootCause := errors.New("connection ended")
	cancelRoot(rootCause)
	runnerReceive(t, r.Done())
	if !errors.Is(context.Cause(r.Context()), rootCause) {
		t.Fatal("connection cause lost")
	}
	if !errors.Is(r.Submit(t.Context(), runnerEvent("E")), ErrSessionRunnerClosed) {
		t.Fatal("closed runner accepted input")
	}
}

func TestSessionRunnerCloseWaitsForActualUnwind(t *testing.T) {
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	started := make(chan struct{}, 2)
	r, err := NewSessionRunner(t.Context(), SessionRunnerConfig{Run: func(ctx context.Context, _ *ihtml.ChatClientEvent) {
		started <- struct{}{}
		close(entered)
		<-ctx.Done()
		close(cancelled)
		<-release
	}})
	if err != nil {
		t.Fatal(err)
	}
	runnerSubmit(t, r, t.Context(), runnerEvent("A"))
	runnerReceive(t, entered)
	runnerSubmit(t, r, t.Context(), runnerEvent("B"))
	closed := make(chan struct{})
	go func() { r.Close(); close(closed) }()
	runnerReceive(t, cancelled)
	select {
	case <-closed:
		t.Fatal("close returned before host unwind")
	case <-r.Done():
		t.Fatal("Done closed before host unwind")
	default:
	}
	close(release)
	runnerReceive(t, closed)
	r.Close()
	if len(started) != 1 {
		t.Fatal("queued work ran during Close")
	}
}

func TestSessionRunnerQueueFullHookIsOutsideLockAndCloseWaitsForIt(t *testing.T) {
	entered := make(chan struct{})
	hookEntered := make(chan struct{})
	hookRelease := make(chan struct{})
	var r *SessionRunner
	var err error
	r, err = NewSessionRunner(t.Context(), SessionRunnerConfig{MaxPending: 1,
		Run:         func(ctx context.Context, _ *ihtml.ChatClientEvent) { close(entered); <-ctx.Done() },
		OnQueueFull: func(context.Context) { r.Stop(); close(hookEntered); <-hookRelease },
	})
	if err != nil {
		t.Fatal(err)
	}
	runnerSubmit(t, r, t.Context(), runnerEvent("A"))
	runnerReceive(t, entered)
	runnerSubmit(t, r, t.Context(), runnerEvent("B"))
	full := make(chan error, 1)
	go func() { full <- r.Submit(t.Context(), runnerEvent("C")) }()
	runnerReceive(t, hookEntered)
	closed := make(chan struct{})
	go func() { r.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("close skipped queue-full hook")
	default:
	}
	close(hookRelease)
	if !errors.Is(runnerReceive(t, full), ErrSessionQueueFull) {
		t.Fatal("queue-full error lost")
	}
	runnerReceive(t, closed)
}

func TestSessionRunnerPanicFailsClosedEvenIfReporterPanics(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	reported := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	r, err := NewSessionRunner(t.Context(), SessionRunnerConfig{
		Run: func(context.Context, *ihtml.ChatClientEvent) {
			mu.Lock()
			calls++
			mu.Unlock()
			close(entered)
			<-release
			panic("host panic")
		},
		OnPanic: func(ctx context.Context, _ any) {
			if !errors.Is(context.Cause(ctx), ErrSessionRunnerPanic) {
				t.Error("panic not cancelled before reporting")
			}
			close(reported)
			panic("reporter panic")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	runnerSubmit(t, r, t.Context(), runnerEvent("A"))
	runnerReceive(t, entered)
	runnerSubmit(t, r, t.Context(), runnerEvent("B"))
	close(release)
	runnerReceive(t, reported)
	runnerReceive(t, r.Done())
	r.Close()
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatal("panic did not retire pending work")
	}
	if !errors.Is(context.Cause(r.Context()), ErrSessionRunnerPanic) {
		t.Fatal("panic cause lost")
	}
}

func TestSessionRunnerClonesEveryFrameAndBoundsEncodedInput(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	received := make(chan ihtml.ChatClientEvent, 1)
	r, err := NewSessionRunner(t.Context(), SessionRunnerConfig{MaxEventBytes: 2048, Run: func(_ context.Context, ev *ihtml.ChatClientEvent) {
		if ev.Messages[0].Content == "A" {
			close(entered)
			<-release
			return
		}
		received <- *ev
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	runnerSubmit(t, r, t.Context(), runnerEvent("A"))
	runnerReceive(t, entered)
	event := runnerEvent("original")
	event.ClientContext = ihtml.ChatClientContext{Page: ihtml.ChatClientPage{QueryFilters: []ihtml.ChatQueryFilter{{Name: "station", Value: "original"}}}}
	event.ToolCall = &ihtml.ProposedToolCall{Body: map[string]any{"nested": map[string]any{"value": "original"}}, Query: map[string]any{"nested": []any{"original"}}}
	runnerSubmit(t, r, t.Context(), event)
	event.Messages[0].Content = "changed"
	event.ClientContext.Page.QueryFilters[0].Value = "changed"
	event.ToolCall.Body.(map[string]any)["nested"].(map[string]any)["value"] = "changed"
	event.ToolCall.Query["nested"].([]any)[0] = "changed"
	if !errors.Is(r.Submit(t.Context(), runnerEvent(strings.Repeat("x", 3000))), ErrSessionEventTooLarge) {
		t.Fatal("oversized frame admitted")
	}
	invalid := runnerEvent("invalid")
	invalid.ToolCall = &ihtml.ProposedToolCall{Body: make(chan int)}
	if r.Submit(t.Context(), invalid) == nil {
		t.Fatal("non-JSON frame admitted")
	}
	close(release)
	got := runnerReceive(t, received)
	if got.Messages[0].Content != "original" || got.ClientContext.Page.QueryFilters[0].Value != "original" || got.ToolCall.Body.(map[string]any)["nested"].(map[string]any)["value"] != "original" || got.ToolCall.Query["nested"].([]any)[0] != "original" {
		t.Fatal("pending event shares caller memory")
	}
}

func TestSessionRunnerRejectsInvalidConfiguration(t *testing.T) {
	noop := func(context.Context, *ihtml.ChatClientEvent) {}
	for _, cfg := range []SessionRunnerConfig{{}, {Run: noop, MaxPending: -1}, {Run: noop, MaxPending: 1025}, {Run: noop, MaxEventBytes: -1}, {Run: noop, MaxEventBytes: 17 << 20}} {
		if _, err := NewSessionRunner(t.Context(), cfg); !errors.Is(err, ErrSessionRunnerInput) {
			t.Fatal("invalid config accepted")
		}
	}
	if _, err := NewSessionRunner(nil, SessionRunnerConfig{Run: noop}); !errors.Is(err, ErrSessionRunnerInput) {
		t.Fatal("nil context accepted")
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := NewSessionRunner(cancelled, SessionRunnerConfig{Run: noop}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled connection accepted")
	}
}

func TestSessionRunnerInlineCloseWaitsForAcceptedUncancelledWork(t *testing.T) {
	r, err := NewSessionRunner(t.Context(), SessionRunnerConfig{Run: func(context.Context, *ihtml.ChatClientEvent) {}})
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	returned := make(chan error, 1)
	go func() {
		returned <- r.RunInline(t.Context(), func(ctx context.Context) {
			close(entered)
			<-ctx.Done()
			close(cancelled)
			accepted := context.WithoutCancel(ctx)
			if accepted.Err() != nil {
				t.Error("accepted host continuation unexpectedly cancelled")
			}
			<-release
		})
	}()
	runnerReceive(t, entered)
	closed := make(chan struct{})
	go func() { r.Close(); close(closed) }()
	runnerReceive(t, cancelled)
	select {
	case <-closed:
		t.Fatal("Close skipped accepted inline work")
	case <-r.Done():
		t.Fatal("Done skipped inline work")
	default:
	}
	close(release)
	if !errors.Is(runnerReceive(t, returned), context.Canceled) {
		t.Fatal("inline lifecycle cancellation lost")
	}
	runnerReceive(t, closed)
	called := false
	if !errors.Is(r.RunInline(t.Context(), func(context.Context) { called = true }), ErrSessionRunnerClosed) || called {
		t.Fatal("closed inline callback ran")
	}
}

func TestSessionRunnerInlineParentCauseAndPanic(t *testing.T) {
	r, err := NewSessionRunner(t.Context(), SessionRunnerConfig{Run: func(context.Context, *ihtml.ChatClientEvent) {}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	parent, cancel := context.WithCancelCause(t.Context())
	cause := errors.New("request ended")
	entered := make(chan struct{})
	returned := make(chan error, 1)
	go func() {
		returned <- r.RunInline(parent, func(ctx context.Context) {
			close(entered)
			<-ctx.Done()
			if !errors.Is(context.Cause(ctx), cause) {
				t.Error("inline parent cause lost")
			}
		})
	}()
	runnerReceive(t, entered)
	cancel(cause)
	if !errors.Is(runnerReceive(t, returned), context.Canceled) {
		t.Fatal("inline parent cancellation lost")
	}
	if r.Context().Err() != nil {
		t.Fatal("one inline parent closed the whole connection")
	}
	if !errors.Is(r.RunInline(t.Context(), func(context.Context) { panic("control panic") }), ErrSessionRunnerPanic) {
		t.Fatal("inline panic was not fail-closed")
	}
	runnerReceive(t, r.Done())
}
