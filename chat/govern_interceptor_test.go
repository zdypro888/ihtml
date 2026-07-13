package chat

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRunHostInterceptorBoundedHonorsParentDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	started := time.Now()
	err := runHostInterceptorBounded(ctx, func(callCtx context.Context) error {
		<-callCtx.Done()
		return callCtx.Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("interceptor returned too late: %s", elapsed)
	}
}

func TestRunHostInterceptorBoundedContainsPanic(t *testing.T) {
	err := runHostInterceptorBounded(context.Background(), func(context.Context) error {
		panic("host secret must not crash the server")
	})
	if !errors.Is(err, errHostInterceptor) {
		t.Fatalf("error = %v, want host interceptor failure", err)
	}
	if got := userFacingError(err); got == err.Error() {
		t.Fatalf("internal interceptor error leaked to user: %q", got)
	}
}

func TestRunHostInterceptorBoundedPreservesReviewedError(t *testing.T) {
	err := runHostInterceptorBounded(context.Background(), func(context.Context) error {
		return NewUserFacingError("本月 AI 配额已用完")
	})
	if got := userFacingError(err); got != "本月 AI 配额已用完" {
		t.Fatalf("user-facing error = %q", got)
	}
}

func TestRunHostInterceptorBoundedCapsCallbacksThatIgnoreContext(t *testing.T) {
	limiter := newHostCallbackLimiter(1)
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- runHostInterceptorBounded(context.Background(), func(context.Context) error {
			close(started)
			<-release
			return nil
		}, limiter)
	}()
	<-started

	err := runHostInterceptorBounded(context.Background(), func(context.Context) error {
		t.Fatal("saturated limiter executed another callback")
		return nil
	}, limiter)
	if !errors.Is(err, errHostInterceptor) {
		t.Fatalf("saturated error = %v, want host interceptor failure", err)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("first callback: %v", err)
	}
	if err := runHostInterceptorBounded(context.Background(), func(context.Context) error { return nil }, limiter); err != nil {
		t.Fatalf("released slot was not reusable: %v", err)
	}
}
