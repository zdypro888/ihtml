package chat

import (
	"context"
	"errors"
	"io"
	rand "math/rand/v2"
	"net"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/cloudwego/eino/adk"
)

var modelHTTPStatusPattern = regexp.MustCompile(`(?i)(?:status(?:[\s_-]+code)?|http(?:[\s_-]+status)?)\s*[:=]?\s*(\d{3})`)

type httpStatusCoder interface{ HTTPStatusCode() int }
type statusCoder interface{ StatusCode() int }
type retryAfterCoder interface{ RetryAfter() time.Duration }

func normalizedModelRetries(configured int) int {
	if configured < 0 {
		return 0
	}
	if configured == 0 {
		return 1
	}
	return configured
}

// newModelRetryConfig centralizes retry policy at the Agent boundary. Provider
// adapters must disable their own implicit retries so every actual attempt is
// charged to the shared turn model budget. This layer also handles mid-stream
// failures and emits Eino retry events that the WebSocket projection can reset
// safely.
func newModelRetryConfig(maxRetries int) *adk.ModelRetryConfig {
	if maxRetries <= 0 {
		return nil
	}
	return &adk.ModelRetryConfig{
		MaxRetries: maxRetries,
		ShouldRetry: func(ctx context.Context, retryCtx *adk.RetryContext) *adk.RetryDecision {
			if retryCtx == nil || !isRetryableModelError(ctx, retryCtx.Err) {
				return &adk.RetryDecision{}
			}
			return &adk.RetryDecision{
				Retry:        true,
				Backoff:      modelRetryDelay(retryCtx.RetryAttempt, retryCtx.Err),
				RejectReason: "模型服务暂时不可用，正在重试",
			}
		},
	}
}

func modelRetryDelay(attempt int, err error) time.Duration {
	base := modelRetryBackoff(attempt)
	serverDelay := modelRetryAfter(err)
	if serverDelay > base {
		// Retry-After is a lower bound. Add only positive jitter so independent
		// replicas do not all retry on the same boundary while still respecting it.
		spread := max(time.Millisecond, serverDelay/10)
		return serverDelay + time.Duration(rand.Int64N(int64(spread)+1))
	}
	spread := max(time.Millisecond, base/5)
	return base - spread + time.Duration(rand.Int64N(int64(2*spread)+1))
}

func modelRetryAfter(err error) time.Duration {
	for current := err; current != nil; current = errors.Unwrap(current) {
		provider, ok := current.(retryAfterCoder)
		if !ok {
			continue
		}
		delay := provider.RetryAfter()
		if delay <= 0 {
			return 0
		}
		if delay > 30*time.Second {
			return 30 * time.Second
		}
		return delay
	}
	return 0
}

func modelRetryBackoff(attempt int) time.Duration {
	switch {
	case attempt <= 1:
		return 500 * time.Millisecond
	case attempt == 2:
		return 1500 * time.Millisecond
	default:
		return 4 * time.Second
	}
}

// isRetryableModelError is deliberately conservative: authentication, request
// validation and policy failures will not improve on retry. Only cancellation-
// aware network failures, throttling and transient upstream statuses qualify.
func isRetryableModelError(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, adk.ErrStreamCanceled) {
		return false
	}
	if errors.Is(err, ErrModelOutputLimit) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary()) {
		return true
	}

	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"unauthorized", "forbidden", "invalid api key", "invalid_api_key",
		"authentication", "permission denied", "billing", "insufficient quota",
		"invalid request", "invalid_request", "context length", "content policy",
		"content_filter", "not found", "unsupported model",
	} {
		if strings.Contains(message, marker) {
			return false
		}
	}
	status := modelErrorStatus(err)
	if status == 0 {
		status = modelHTTPStatus(message)
	}
	if status != 0 {
		switch status {
		case 408, 409, 425, 429, 500, 502, 503, 504, 529:
			return true
		default:
			return false
		}
	}
	for _, marker := range []string{
		"rate limit", "too many requests", "overloaded", "temporarily unavailable",
		"service unavailable", "gateway timeout", "bad gateway",
		"connection reset", "connection refused", "connection closed",
		"server closed idle connection", "broken pipe", "tls handshake timeout",
		"unexpected eof", "timeout", "deadline exceeded",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// Provider SDKs expose status codes through different concrete error structs.
// Walk the unwrap chain and read only well-known integer field names, keeping
// the SDK core independent from those provider implementation packages.
func modelErrorStatus(err error) int {
	for current := err; current != nil; current = errors.Unwrap(current) {
		if value, ok := current.(httpStatusCoder); ok {
			if status := value.HTTPStatusCode(); status >= 100 && status <= 599 {
				return status
			}
		}
		if value, ok := current.(statusCoder); ok {
			if status := value.StatusCode(); status >= 100 && status <= 599 {
				return status
			}
		}
		value := reflect.ValueOf(current)
		for value.IsValid() && (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface) {
			if value.IsNil() {
				break
			}
			value = value.Elem()
		}
		if !value.IsValid() || value.Kind() != reflect.Struct {
			continue
		}
		for _, fieldName := range []string{"HTTPStatusCode", "StatusCode"} {
			field := value.FieldByName(fieldName)
			if field.IsValid() && field.CanInt() {
				status := int(field.Int())
				if status >= 100 && status <= 599 {
					return status
				}
			}
			if field.IsValid() && field.CanUint() {
				status := int(field.Uint())
				if status >= 100 && status <= 599 {
					return status
				}
			}
		}
	}
	return 0
}

func modelHTTPStatus(message string) int {
	match := modelHTTPStatusPattern.FindStringSubmatch(message)
	if len(match) != 2 {
		return 0
	}
	status := 0
	for _, ch := range match[1] {
		status = status*10 + int(ch-'0')
	}
	return status
}
