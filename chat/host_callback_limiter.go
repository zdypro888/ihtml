package chat

import (
	"context"
	"time"
)

// A host callback may be implemented by third-party code and ignore the
// context it receives. Go cannot terminate such a goroutine, so the SDK puts a
// hard ceiling on callbacks that have not *actually* returned. Timeouts bound
// caller latency; this limiter separately bounds permanent resource leakage.
const defaultMaxOutstandingHostCallbacks = 32

// Observation exporters are intentionally isolated from safety-critical host
// callbacks. A slow metrics backend may lose best-effort snapshots, but it must
// never consume the slots needed by authorization or confirmation handling.
const defaultMaxOutstandingObservations = 32

type hostCallbackLimiter struct {
	slots chan struct{}
}

var fallbackHostCallbackLimiter = newHostCallbackLimiter(defaultMaxOutstandingHostCallbacks)

var fallbackObservationCallbackLimiter = newHostCallbackLimiter(defaultMaxOutstandingObservations)

func newHostCallbackLimiter(limit int) *hostCallbackLimiter {
	if limit <= 0 {
		limit = defaultMaxOutstandingHostCallbacks
	}
	return &hostCallbackLimiter{slots: make(chan struct{}, limit)}
}

func selectHostCallbackLimiter(candidates ...*hostCallbackLimiter) *hostCallbackLimiter {
	for _, candidate := range candidates {
		if candidate != nil {
			return candidate
		}
	}
	return fallbackHostCallbackLimiter
}

func selectObservationCallbackLimiter(candidates ...*hostCallbackLimiter) *hostCallbackLimiter {
	for _, candidate := range candidates {
		if candidate != nil {
			return candidate
		}
	}
	return fallbackObservationCallbackLimiter
}

func (l *hostCallbackLimiter) tryAcquire() bool {
	if l == nil {
		return false
	}
	select {
	case l.slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (l *hostCallbackLimiter) release() {
	if l == nil {
		return
	}
	<-l.slots
}

func runHostValueBounded[T any](parent context.Context, limiter *hostCallbackLimiter, timeout time.Duration, callback func(context.Context) (T, error)) (T, error) {
	var zero T
	if callback == nil {
		return zero, nil
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx := parent
	cancel := func() {}
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(parent, timeout)
	}
	defer cancel()
	limiter = selectHostCallbackLimiter(limiter)
	if !limiter.tryAcquire() {
		return zero, errHostInterceptor
	}

	type hostResult struct {
		value T
		err   error
	}
	result := make(chan hostResult, 1)
	go func() {
		defer limiter.release()
		out := hostResult{}
		defer func() {
			if recover() != nil {
				out.value = zero
				out.err = errHostInterceptor
			}
			result <- out
		}()
		out.value, out.err = callback(ctx)
	}()

	select {
	case <-ctx.Done():
		return zero, ctx.Err()
	case out := <-result:
		return out.value, out.err
	}
}

// runHostValueUntilReturn is used for ordered nonce CAS operations. It runs on
// the per-session approval worker, so a broken store cannot block the websocket
// reader. The global slot cap bounds stuck workers, while waiting for the real
// return prevents a timed-out CAS from overtaking the next approval frame.
func runHostValueUntilReturn[T any](parent context.Context, limiter *hostCallbackLimiter, timeout time.Duration, callback func(context.Context) (value T, err error)) (value T, err error) {
	if callback == nil {
		return value, nil
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx := parent
	cancel := func() {}
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(parent, timeout)
	}
	defer cancel()
	limiter = selectHostCallbackLimiter(limiter)
	if !limiter.tryAcquire() {
		return value, errHostInterceptor
	}
	defer limiter.release()
	defer func() {
		if recover() != nil {
			var zero T
			value = zero
			err = errHostInterceptor
		}
	}()
	return callback(ctx)
}
