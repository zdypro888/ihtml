package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// 重新连接的实例没有原 goroutine，也必须持久通知原执行者停止；其他作用域不受影响。
func TestCancelAllStopsRemoteScopeAndPreservesOtherScopes(t *testing.T) {
	for _, fixed := range []bool{true, false} {
		t.Run(map[bool]string{true: "fixed_scope", false: "seen_scope"}[fixed], func(t *testing.T) {
			// 本例核对作用域取消，失租另有显式故障测试；避免并发全量回归中的调度抖动先触发60ms租约。
			store := NewMemoryTaskStore()
			executor := taskRuntimeFixture(t, TaskConfig{Store: store, LeaseDuration: time.Second, PollInterval: time.Millisecond})
			entered := make(chan string, 2)
			stopped := make(chan string, 2)
			tool := Tool{Name: "arbitrary_sync", Effect: ToolEffectRead, Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
				entered <- string(raw)
				<-ctx.Done()
				stopped <- string(raw)
				return "", ctx.Err()
			}}
			own, err := executor.Start(t.Context(), "owner", "turn", "key", tool, json.RawMessage(`"owner"`))
			if err != nil {
				t.Fatal(err)
			}
			foreign, err := executor.Start(t.Context(), "foreign", "turn", "key", tool, json.RawMessage(`"foreign"`))
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				select {
				case <-entered:
				case <-time.After(time.Second):
					t.Fatal("original executor did not start")
				}
			}
			cfg := TaskConfig{Store: store, LeaseDuration: time.Second, PollInterval: time.Millisecond}
			if fixed {
				cfg.Scope = "owner"
			}
			controller := taskRuntimeFixture(t, cfg)
			wait, err := controller.Suspend(t.Context(), "owner", "wait", "key", TaskWaitSpec{Timeout: time.Hour, Checkpoint: "original goal"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err = controller.CancelAll(); err != nil {
				t.Fatal(err)
			}
			select {
			case who := <-stopped:
				if who != `"owner"` {
					t.Fatal("stopped another scope", who)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("remote executor ignored persisted stop")
			}
			rows, err := executor.Wait(t.Context(), "owner", []string{own.ID}, map[string]bool{tool.Name: true}, time.Second, true)
			if err != nil || rows[0].State != "cancelled" {
				t.Fatal(rows, err)
			}
			other, err := store.GetTask(t.Context(), "foreign", foreign.ID)
			if err != nil || other.CancelRequested || other.Info.State != "running" {
				t.Fatal("scope stop leaked", other, err)
			}
			w, err := store.GetWait(t.Context(), "owner", wait.ID)
			if err != nil || w.State != "cancelled" {
				t.Fatal("scope stop left wait active", w, err)
			}
		})
	}
}

type failingTaskScanStore struct{ TaskStore }

func (s failingTaskScanStore) ListTasks(context.Context, string, TaskScan) ([]TaskRecord, error) {
	return nil, errors.New("storage unavailable")
}

// 持久取消失败必须可见，同时不能拖延本地的即时取消。
func TestCancelAllReportsPersistentFailureAfterLocalCancellation(t *testing.T) {
	store := failingTaskScanStore{NewMemoryTaskStore()}
	runtime := taskRuntimeFixture(t, TaskConfig{Store: store, Scope: "owner"})
	entered := make(chan struct{})
	stopped := make(chan struct{})
	_, err := runtime.Start(t.Context(), "owner", "turn", "key", Tool{Name: "sync", Effect: ToolEffectRead, Handler: func(ctx context.Context, _ json.RawMessage) (string, error) {
		close(entered)
		<-ctx.Done()
		close(stopped)
		return "", ctx.Err()
	}}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	if err = runtime.CancelAll(); err == nil {
		t.Fatal("persistent cancellation failure was hidden")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("storage failure blocked local cancellation")
	}
}
