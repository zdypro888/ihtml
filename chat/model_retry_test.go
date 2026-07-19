package chat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
)

type modelStatusError struct {
	StatusCode int
}

type retryAfterError struct{ delay time.Duration }

func (e retryAfterError) Error() string             { return "status code: 429" }
func (e retryAfterError) RetryAfter() time.Duration { return e.delay }

func (e *modelStatusError) Error() string { return "provider request failed" }

func TestModelRetryClassification(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"cancelled", context.Canceled, false},
		{"timeout", context.DeadlineExceeded, true},
		{"unexpected eof", io.ErrUnexpectedEOF, true},
		{"network timeout", &net.OpError{Op: "read", Net: "tcp", Err: context.DeadlineExceeded}, true},
		{"temporary dns", &net.DNSError{Err: "temporary", Name: "model.example", IsTemporary: true}, true},
		{"missing dns", &net.DNSError{Err: "no such host", Name: "model.invalid", IsNotFound: true}, false},
		{"invalid address", net.InvalidAddrError("bad address"), false},
		{"rate limit status", errors.New("error, status code: 429, too many requests"), true},
		{"underscore status", errors.New("provider status_code=502"), true},
		{"upstream status", errors.New("HTTP status=503"), true},
		{"typed overloaded status", &modelStatusError{StatusCode: 529}, true},
		{"typed auth status", &modelStatusError{StatusCode: 401}, false},
		{"auth", errors.New("status code: 401 unauthorized"), false},
		{"bad request", errors.New("status code: 400 invalid request"), false},
		{"quota", errors.New("status code: 429 insufficient quota"), false},
		{"plain model error", errors.New("tool schema rejected"), false},
		{"local output limit", &ModelOutputLimitError{Scope: ModelOutputLimitTurnBytes, Limit: 1024}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRetryableModelError(context.Background(), tc.err); got != tc.want {
				t.Fatalf("isRetryableModelError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if isRetryableModelError(cancelled, context.DeadlineExceeded) {
		t.Fatal("an expired turn context must suppress retries")
	}
}

func TestModelRetryConfigAndBackoff(t *testing.T) {
	if newModelRetryConfig(0) != nil || newModelRetryConfig(-1) != nil {
		t.Fatal("disabled retries returned a config")
	}
	cfg := newModelRetryConfig(2)
	if cfg == nil || cfg.MaxRetries != 2 || cfg.ShouldRetry == nil {
		t.Fatalf("retry config = %#v", cfg)
	}
	decision := cfg.ShouldRetry(context.Background(), &adk.RetryContext{
		RetryAttempt: 1,
		Err:          fmt.Errorf("gateway: %w", context.DeadlineExceeded),
	})
	if decision == nil || !decision.Retry || decision.Backoff < 400*time.Millisecond || decision.Backoff > 600*time.Millisecond {
		t.Fatalf("retry decision = %#v", decision)
	}
	decision = cfg.ShouldRetry(context.Background(), &adk.RetryContext{RetryAttempt: 1, Err: errors.New("401 unauthorized")})
	if decision == nil || decision.Retry {
		t.Fatalf("non-retry decision = %#v", decision)
	}
	decision = cfg.ShouldRetry(context.Background(), &adk.RetryContext{RetryAttempt: 1, Err: retryAfterError{delay: 2 * time.Second}})
	if !decision.Retry || decision.Backoff < 2*time.Second || decision.Backoff > 2200*time.Millisecond {
		t.Fatalf("Retry-After decision = %#v", decision)
	}
}
