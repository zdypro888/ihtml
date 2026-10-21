package assistant

// The bounded serial queue and cancellation pattern is adapted from
// github.com/zdypro888/nbco gateway/httpapi/ihtml_chat.go (MIT), through the
// HJGAS host bridge. Authorization and business behavior remain host-owned.

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/zdypro888/ihtml"
)

var (
	ErrSessionEventTooLarge = errors.New("assistant: session event exceeds the byte limit")
	ErrSessionRunnerPanic   = errors.New("assistant: session runner hook panicked")
	// ErrTurnStopped identifies an explicit user stop, independently of a lost
	// connection or a parent deadline. Test context.Cause with errors.Is.
	ErrTurnStopped         = errors.New("assistant: turn stopped by user")
	ErrSessionQueueFull    = errors.New("assistant: session queue is full")
	ErrSessionRunnerClosed = errors.New("assistant: session runner is closed")
	ErrSessionRunnerInput  = errors.New("assistant: invalid session runner input")
)

// SessionRunnerConfig keeps lifecycle scheduling independent of an Engine or
// event projection. Run owns trusted output, business authorization and tools.
// A panic aborts the connection and drops pending work before OnPanic runs.
// Hooks must honor context cancellation. Close must not be called reentrantly
// from Run or hooks because it waits for all active work to unwind.
type SessionRunnerConfig struct {
	MaxEventBytes int // encoded JSON ceiling; defaults to 2 MiB; maximum 16 MiB
	MaxPending    int // defaults to 16; excludes the active event; maximum 1024
	Run           func(context.Context, *ihtml.ChatClientEvent)
	OnQueueFull   func(context.Context)
	OnPanic       func(context.Context, any)
}

type sessionRunnerEvent struct {
	parent context.Context
	event  ihtml.ChatClientEvent
}

// SessionRunner serializes host-owned chat work with bounded memory. It does
// not filter event types, change messages, sign cards, or choose a projection.
// Pending frames are bounded by MaxPending * MaxEventBytes; caller-side input
// limits still own memory allocated before Submit.
// Hosts can handle control frames synchronously and submit only queued work.
type SessionRunner struct {
	cfg    SessionRunnerConfig
	ctx    context.Context
	cancel context.CancelCauseFunc
	mu     sync.Mutex
	hooks  sync.WaitGroup
	queue  []sessionRunnerEvent
	active context.CancelCauseFunc
	closed bool
	wake   chan struct{}
	done   chan struct{}
}

func NewSessionRunner(ctx context.Context, cfg SessionRunnerConfig) (*SessionRunner, error) {
	if ctx == nil || cfg.Run == nil || cfg.MaxPending < 0 || cfg.MaxPending > 1024 || cfg.MaxEventBytes < 0 || cfg.MaxEventBytes > 16<<20 {
		return nil, ErrSessionRunnerInput
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cfg.MaxPending == 0 {
		cfg.MaxPending = 16
	}
	if cfg.MaxEventBytes == 0 {
		cfg.MaxEventBytes = 2 << 20
	}
	ctx, cancel := context.WithCancelCause(ctx)
	runner := &SessionRunner{cfg: cfg, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), done: make(chan struct{})}
	go runner.loop()
	return runner, nil
}

func (r *SessionRunner) Context() context.Context { return r.ctx }
func (r *SessionRunner) Done() <-chan struct{}    { return r.done }

// Submit never waits for the active task. The complete frame is JSON-cloned
// before enqueueing so later caller mutation cannot change a pending command.
// Queue-full callbacks run outside the scheduler lock and may call Stop/Abort.
func (r *SessionRunner) Submit(parent context.Context, event *ihtml.ChatClientEvent) error {
	if parent == nil || event == nil {
		return ErrSessionRunnerInput
	}
	if err := parent.Err(); err != nil {
		return err
	}
	copied, err := cloneSessionEvent(event, r.cfg.MaxEventBytes)
	if err != nil {
		return err
	}
	r.mu.Lock()
	if r.closed || r.ctx.Err() != nil {
		r.mu.Unlock()
		return ErrSessionRunnerClosed
	}
	if len(r.queue) >= r.cfg.MaxPending {
		r.hooks.Add(1)
		r.mu.Unlock()
		defer r.hooks.Done()
		_ = r.runInlineTracked(parent, func(ctx context.Context) {
			if r.cfg.OnQueueFull != nil {
				r.cfg.OnQueueFull(ctx)
			}
		})
		return ErrSessionQueueFull
	}
	r.queue = append(r.queue, sessionRunnerEvent{parent: parent, event: copied})
	select {
	case r.wake <- struct{}{}:
	default:
	}
	r.mu.Unlock()
	return nil
}

// RunInline immediately executes a tracked control callback outside the FIFO.
// It is intended for synchronous cancellation acknowledgements and trusted
// rejection replies. Close and Done still wait for its actual return, including
// host work deliberately continued with context.WithoutCancel after acceptance.
// The returned error describes lifecycle cancellation/admission, never whether
// a business operation succeeded. The callback owns that outcome and its reply.
func (r *SessionRunner) RunInline(parent context.Context, run func(context.Context)) error {
	if parent == nil || run == nil {
		return ErrSessionRunnerInput
	}
	if err := parent.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	if r.closed || r.ctx.Err() != nil {
		r.mu.Unlock()
		return ErrSessionRunnerClosed
	}
	r.hooks.Add(1)
	r.mu.Unlock()
	defer r.hooks.Done()
	return r.runInlineTracked(parent, run)
}

func (r *SessionRunner) runInlineTracked(parent context.Context, run func(context.Context)) error {
	ctx, _, finish := sessionExecution(r.ctx, parent)
	defer finish()
	if err := ctx.Err(); err != nil {
		return err
	}
	r.invokeHook(ctx, func() { run(ctx) })
	if errors.Is(context.Cause(r.ctx), ErrSessionRunnerPanic) {
		return ErrSessionRunnerPanic
	}
	return ctx.Err()
}

// Stop atomically retires waiting work and cancels the active event. The
// connection stays open; an event submitted after Stop can run normally.
func (r *SessionRunner) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.queue = nil
	if r.active != nil {
		r.active(ErrTurnStopped)
	}
}

// Abort closes the connection lifecycle without waiting. It is safe from Run,
// Emit and other hooks, for example when the host cannot deliver an event.
func (r *SessionRunner) Abort(cause error) {
	if cause == nil {
		cause = ErrSessionRunnerClosed
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	r.queue = nil
	r.cancel(cause)
	if r.active != nil {
		r.active(cause)
	}
}

// Close cancels active and queued work and waits for Run to actually return.
// Concurrent callers are safe; cancellation is never mistaken for completion.
func (r *SessionRunner) Close() { r.Abort(ErrSessionRunnerClosed); <-r.done }

func (r *SessionRunner) loop() {
	defer close(r.done)
	defer r.hooks.Wait()
	defer func() { r.mu.Lock(); r.closed = true; r.queue = nil; r.mu.Unlock() }()
	for {
		r.mu.Lock()
		if r.closed || r.ctx.Err() != nil {
			r.mu.Unlock()
			return
		}
		if len(r.queue) == 0 {
			r.mu.Unlock()
			select {
			case <-r.ctx.Done():
			case <-r.wake:
			}
			continue
		}
		next := r.queue[0]
		r.queue[0] = sessionRunnerEvent{}
		r.queue = r.queue[1:]
		ctx, cancel, finish := sessionExecution(r.ctx, next.parent)
		r.active = cancel
		r.mu.Unlock()
		if ctx.Err() == nil {
			r.invokeHook(ctx, func() { r.cfg.Run(ctx, &next.event) })
		}
		finish()
		r.mu.Lock()
		r.active = nil
		r.mu.Unlock()
	}
}

func (r *SessionRunner) invokeHook(ctx context.Context, run func()) {
	defer func() {
		if value := recover(); value != nil {
			// A host may already have performed a partial operation. Panic is
			// never a completed turn and no queued command may start after it.
			r.Abort(ErrSessionRunnerPanic)
			if r.cfg.OnPanic != nil {
				func() { defer func() { _ = recover() }(); r.cfg.OnPanic(ctx, value) }()
			}
		}
	}()
	run()
}

func cloneSessionEvent(event *ihtml.ChatClientEvent, maxBytes int) (ihtml.ChatClientEvent, error) {
	var out ihtml.ChatClientEvent
	raw, err := json.Marshal(event)
	if maxBytes > 0 && len(raw) > maxBytes {
		return out, ErrSessionEventTooLarge
	}
	if err == nil {
		err = json.Unmarshal(raw, &out)
	}
	return out, err
}

// sessionExecution is shared by the host runner and Backend's confirmation
// scheduler. The latter retains its rejection/async-cancel lanes and approval
// restoration; both schedulers use the same parent-cancellation boundary.
func sessionExecution(session, parent context.Context) (context.Context, context.CancelCauseFunc, func()) {
	ctx, cancel := context.WithCancelCause(session)
	stop := context.AfterFunc(parent, func() { cancel(context.Cause(parent)) })
	if parent.Err() != nil {
		cancel(context.Cause(parent))
	}
	return ctx, cancel, func() { stop(); cancel(context.Canceled) }
}
