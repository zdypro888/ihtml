package agent

import (
	"context"
	"errors"
	"testing"
	"time"
)

// 终态失败也是已返回事实，通用等待不解析成功状态或重试任何原调用。
func TestPollUntilPreservesTerminalFactsAndNeverRetriesErrors(t *testing.T) {
	calls := 0
	failure := errors.New("probe unavailable")
	_, err := PollUntil(t.Context(), time.Millisecond, func(context.Context) (PollResult[string], error) { calls++; return PollResult[string]{}, failure }, nil)
	if !errors.Is(err, failure) || calls != 1 {
		t.Fatal(err, calls)
	}
	got, err := PollUntil(t.Context(), time.Millisecond, func(context.Context) (PollResult[string], error) {
		return PollResult[string]{Value: `{"state":"failed"}`, Done: true}, nil
	}, nil)
	if err != nil || got != `{"state":"failed"}` {
		t.Fatal(got, err)
	}
}

// 探针忽略取消后返回的成功不能越过停止边界；也不能发出过时进度。
func TestPollUntilCancellationDominatesProbeReturn(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	notifications := 0
	_, err := PollUntil(ctx, time.Second, func(context.Context) (PollResult[int], error) {
		cancel()
		return PollResult[int]{Value: 1, Done: true, Progress: "late"}, nil
	}, func(string) { notifications++ })
	if !errors.Is(err, context.Canceled) || notifications != 0 {
		t.Fatal(err, notifications)
	}
}

// 长等待中的取消立即结束，不等待整个轮询间隔，也不取消任何远端作业。
func TestPollUntilCancellationInterruptsInterval(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	_, err := PollUntil(ctx, time.Hour, func(context.Context) (PollResult[int], error) {
		calls++
		return PollResult[int]{Progress: "waiting"}, nil
	}, func(string) { cancel() })
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatal(err, calls)
	}
}

func TestPollUntilSpacesFastProbes(t *testing.T) {
	const interval = 20 * time.Millisecond
	var first time.Time
	calls := 0
	got, err := PollUntil(t.Context(), interval, func(context.Context) (PollResult[int], error) {
		calls++
		if calls == 1 {
			first = time.Now()
			return PollResult[int]{}, nil
		}
		if time.Since(first) < interval {
			t.Error("busy polling")
		}
		return PollResult[int]{Value: 42, Done: true}, nil
	}, nil)
	if err != nil || got != 42 || calls != 2 {
		t.Fatal(got, err, calls)
	}
}
