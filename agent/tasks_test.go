package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func taskRuntimeFixture(t *testing.T, cfg TaskConfig) *TaskRuntime {
	t.Helper()
	r, err := NewTaskRuntime(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	return r
}

// Handler 真正并行并按句柄保存结果，启动返回不等同步 Handler 完成。
func TestTaskRuntimeStartsSynchronousHandlersAndNotifies(t *testing.T) {
	r := taskRuntimeFixture(t, TaskConfig{MaxConcurrent: 2})
	release := make(chan struct{})
	defer close(release)
	started := make(chan struct{}, 2)
	tool := Tool{Name: "arbitrary_sync", Effect: ToolEffectRead, Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
		started <- struct{}{}
		select {
		case <-release:
			return string(args), nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}}
	first, err := r.Start(t.Context(), "owner", "turn", "a", tool, json.RawMessage(`{"value":"a"}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.Start(t.Context(), "owner", "turn", "b", tool, json.RawMessage(`{"value":"b"}`))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("handlers did not overlap")
		}
	}
	rows, err := r.Wait(t.Context(), "owner", []string{first.ID, second.ID}, map[string]bool{tool.Name: true}, 0, true)
	if err != nil || len(rows) != 2 || rows[0].Result != "" {
		t.Fatal(rows, err)
	}
	// 测试屏障与上下文分开，释放后可真实取得各自返回值。
	r.EndTurn("owner", "turn", true)
	// 通过取消单独的等待证明等待结束不取消原任务。
	c, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = r.Wait(c, "owner", []string{first.ID}, map[string]bool{tool.Name: true}, time.Second, true); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestTaskRuntimeRetainsResultsAcrossNormalTurnAndChecksScope(t *testing.T) {
	r := taskRuntimeFixture(t, TaskConfig{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	release := make(chan struct{})
	started := make(chan struct{})
	row, err := r.Start(ctx, "scope", "turn", "key", Tool{Name: "sync_read", Effect: ToolEffectRead, Handler: func(ctx context.Context, _ json.RawMessage) (string, error) {
		close(started)
		select {
		case <-release:
			return "actual-result", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	<-started
	r.EndTurn("scope", "turn", true)
	cancel()
	close(release)
	rows, err := r.Wait(t.Context(), "scope", []string{row.ID}, map[string]bool{"sync_read": true}, time.Second, true)
	if err != nil || rows[0].State != "completed" || rows[0].Result != "actual-result" {
		t.Fatal(rows, err)
	}
	select {
	case event := <-r.Completed():
		if event.ID != row.ID || event.Result != "" {
			t.Fatal(event)
		}
	case <-time.After(time.Second):
		t.Fatal("missing completion channel event")
	}
	if _, err = r.Read("other", []string{row.ID}, map[string]bool{"sync_read": true}); err == nil {
		t.Fatal("cross-owner result exposed")
	}
	if _, err = r.Read("scope", []string{row.ID}, map[string]bool{}); err == nil {
		t.Fatal("revoked tool result exposed")
	}
	rows, _ = r.Read("scope", nil, map[string]bool{"sync_read": true})
	if rows[0].Result != "" {
		t.Fatal("listing leaked payload")
	}
}

func TestTaskRuntimeCancellationWaitsForUnwindAndPreservesUnknown(t *testing.T) {
	r := taskRuntimeFixture(t, TaskConfig{})
	entered := make(chan struct{})
	cancelSeen := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	tool := Tool{Name: "sync_write", Effect: ToolEffectWrite, Handler: func(ctx context.Context, _ json.RawMessage) (string, error) {
		calls.Add(1)
		close(entered)
		<-ctx.Done()
		close(cancelSeen)
		<-release
		return "", errors.New("private-token")
	}}
	row, err := r.Start(t.Context(), "owner", "turn", "key", tool, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	same, err := r.Start(t.Context(), "owner", "turn", "key", tool, json.RawMessage(`{}`))
	if err != nil || same.ID != row.ID {
		t.Fatal(same, err)
	}
	if _, err = r.Start(t.Context(), "owner", "turn", "key", tool, json.RawMessage(`{"changed":true}`)); err == nil {
		t.Fatal("changed invocation accepted")
	}
	cancelled, err := r.Cancel("owner", row.ID, map[string]bool{tool.Name: true})
	if err != nil || cancelled.State != "cancelling" {
		t.Fatal(cancelled, err)
	}
	<-cancelSeen
	rows, _ := r.Read("owner", []string{row.ID}, map[string]bool{tool.Name: true})
	if rows[0].State != "cancelling" || calls.Load() != 1 {
		t.Fatal(rows, calls.Load())
	}
	// 显式释放仅用于测试收尾，真实终态不能在 Handler 返回前发布。
	close(release)
	rows, err = r.Wait(t.Context(), "owner", []string{row.ID}, map[string]bool{tool.Name: true}, time.Second, true)
	if err != nil || rows[0].State != "unknown" || strings.Contains(rows[0].Error, "private-token") {
		t.Fatal(rows, err)
	}
}

func TestTaskRuntimeBoundsFailuresAndNeverReplays(t *testing.T) {
	for _, effect := range []string{ToolEffectRead, ToolEffectWrite} {
		t.Run(effect, func(t *testing.T) {
			r := taskRuntimeFixture(t, TaskConfig{MaxTasks: 1, MaxResultBytes: 8})
			var calls atomic.Int32
			tool := Tool{Name: "bounded", Effect: effect, Handler: func(context.Context, json.RawMessage) (string, error) {
				calls.Add(1)
				return strings.Repeat("x", 9), nil
			}}
			row, err := r.Start(t.Context(), "owner", "turn", "same", tool, json.RawMessage(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			rows, err := r.Wait(t.Context(), "owner", []string{row.ID}, map[string]bool{tool.Name: true}, time.Second, true)
			if err != nil || rows[0].Result != "" {
				t.Fatal(rows, err)
			}
			// 内联额度只决定读取形式，完整结果通过引用保留，不改变实际执行事实。
			if rows[0].State != "completed" || rows[0].ResultRef == nil || rows[0].ResultRef.Size != 9 {
				t.Fatal(rows)
			}
			again, err := r.Start(t.Context(), "owner", "turn", "same", tool, json.RawMessage(`{}`))
			if err != nil || again.ID != row.ID || calls.Load() != 1 {
				t.Fatal(again, err, calls.Load())
			}
			if _, err = r.Start(t.Context(), "owner", "turn", "new", tool, json.RawMessage(`{}`)); err != nil {
				t.Fatal("completed history consumed active capacity", err)
			}
		})
	}
}

func TestTaskRuntimeCancelsQueuedWorkAndClosesNotifications(t *testing.T) {
	r := taskRuntimeFixture(t, TaskConfig{MaxConcurrent: 1})
	entered := make(chan struct{})
	var calls atomic.Int32
	tool := Tool{Name: "queued_read", Effect: ToolEffectRead, Handler: func(ctx context.Context, _ json.RawMessage) (string, error) {
		calls.Add(1)
		close(entered)
		<-ctx.Done()
		return "", ctx.Err()
	}}
	first, err := r.Start(t.Context(), "owner", "turn", "first", tool, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	second, err := r.Start(t.Context(), "owner", "turn", "second", tool, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{tool.Name: true}
	done, err := r.Done("owner", second.ID, allowed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Cancel("owner", second.ID, allowed); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("queued cancellation not completed")
	}
	rows, err := r.Read("owner", []string{second.ID}, allowed)
	if err != nil || rows[0].State != "cancelled" || calls.Load() != 1 {
		t.Fatal(rows, err, calls.Load())
	}
	r.CancelAll()
	if _, err = r.Wait(t.Context(), "owner", []string{first.ID}, allowed, time.Second, true); err != nil {
		t.Fatal(err)
	}
	r.Close()
	r.Close()
	n := 0
	for range r.Completed() {
		n++
	}
	if n != 2 {
		t.Fatal("missing terminal notifications", n)
	}
}
