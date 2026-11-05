package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// 操作期限只约束控制存储请求，不能泄漏到整轮模型的上下文。
type boundedTaskControlStore struct {
	TaskControlStore
	checks atomic.Int32
}

func (s *boundedTaskControlStore) LoadControl(ctx context.Context, key string) (TaskControlState, error) {
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 5*time.Second {
		return TaskControlState{}, errors.New("control operation has no bounded deadline")
	}
	s.checks.Add(1)
	return s.TaskControlStore.LoadControl(ctx, key)
}
func (s *boundedTaskControlStore) CompareControl(ctx context.Context, state TaskControlState, revision int64) (bool, error) {
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 5*time.Second {
		return false, errors.New("control operation has no bounded deadline")
	}
	return s.TaskControlStore.CompareControl(ctx, state, revision)
}

func TestTaskTurnContextKeepsLifetimeAndJoinsObserver(t *testing.T) {
	control := &boundedTaskControlStore{TaskControlStore: NewMemoryTaskControlStore()}
	r := taskRuntimeFixture(t, TaskConfig{ControlStore: control, Scope: "scope", PollInterval: time.Millisecond})
	bound, err := r.BindContext(t.Context(), "scope")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := bound.Deadline(); ok {
		if original, had := t.Context().Deadline(); !had || got != original {
			t.Fatal("control operation deadline shortened the accepted turn")
		}
	}
	watched, release, err := r.WatchContext(bound, "scope")
	if err != nil {
		t.Fatal(err)
	}
	release()
	if watched.Err() == nil {
		t.Fatal("observer release did not cancel its lifetime")
	}
	before := control.checks.Load()
	time.Sleep(5 * time.Millisecond)
	if after := control.checks.Load(); after != before {
		t.Fatal("observer kept querying after release", before, after)
	}
	if err = r.CancelAll(); err != nil {
		t.Fatal(err)
	}
	if _, err = r.UseContext(bound, "scope"); !errors.Is(err, ErrTaskStopped) {
		t.Fatal("stale accepted context was rebound", err)
	}
	if _, err = r.BindContext(t.Context(), "scope"); err != nil {
		t.Fatal("new explicit admission did not acquire the current generation", err)
	}
}
