package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// 共享接纳不是按Runtime累计：同一池的多个资源同时检查，拒绝不得留下部分预留。
func TestSharedTaskBudgetsAreAtomicAndWaitsDeduplicate(t *testing.T) {
	store := NewMemoryTaskStore()
	controls := NewMemoryTaskControlStore()
	config := TaskConfig{Store: store, ControlStore: controls, BudgetPool: "pool", Scope: "scope", PollInterval: time.Millisecond, Budgets: []TaskBudgetLimit{{Key: "global", MaxActive: 1, MaxConcurrent: 1, MaxWaiting: 1}, {Key: "user", MaxActive: 1, MaxConcurrent: 1, MaxWaiting: 1}}}
	a := taskRuntimeFixture(t, config)
	b := taskRuntimeFixture(t, config)
	wait, err := a.Suspend(t.Context(), "scope", "turn", "timer", TaskWaitSpec{Timeout: time.Hour, Checkpoint: "original"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	same, err := b.Suspend(t.Context(), "scope", "turn", "timer", TaskWaitSpec{Timeout: time.Hour, Checkpoint: "original"}, nil)
	if err != nil || same.ID != wait.ID {
		t.Fatal("duplicate consumed wait capacity", same, err)
	}
	_, err = b.Suspend(t.Context(), "scope", "turn2", "timer", TaskWaitSpec{Timeout: time.Hour, Checkpoint: "second"}, nil)
	var budget *TaskBudgetError
	if !errors.As(err, &budget) {
		t.Fatal("wait budget bypassed", err)
	}
	views, err := a.Budget(t.Context())
	if err != nil || len(views) != 2 || views[0].WaitingRemaining != 0 || views[1].WaitingRemaining != 0 {
		t.Fatal(views, err)
	}
	if _, err = b.Cancel("scope", wait.ID, map[string]bool{"task_suspend": true}); err != nil {
		t.Fatal(err)
	}
	views, err = a.Budget(t.Context())
	if err != nil || views[0].WaitingRemaining != 1 || views[1].WaitingRemaining != 1 {
		t.Fatal("wait permit not released across owner", views, err)
	}
	gate := make(chan struct{})
	defer close(gate)
	row, err := a.Start(t.Context(), "scope", "task-turn", "first", Tool{Name: "sync", Effect: ToolEffectRead, Handler: func(ctx context.Context, _ json.RawMessage) (string, error) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-gate:
			return "actual", nil
		}
	}}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.Start(t.Context(), "scope", "task-turn", "second", Tool{Name: "other", Effect: ToolEffectRead, Handler: func(context.Context, json.RawMessage) (string, error) {
		t.Error("unadmitted handler ran")
		return "", nil
	}}, json.RawMessage(`{}`))
	if !errors.As(err, &budget) {
		t.Fatal("active budget bypassed", err)
	}
	if _, err = a.Cancel("scope", row.ID, map[string]bool{"sync": true}); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Wait(t.Context(), "scope", []string{row.ID}, map[string]bool{"sync": true}, time.Second, true); err != nil {
		t.Fatal(err)
	}
	// Wait观察到终态后也等本地真实收尾，因此执行资源应可再次接纳。
	deadline := time.Now().Add(time.Second)
	for {
		views, err = a.Budget(t.Context())
		if err == nil && views[0].ActiveRemaining == 1 && views[1].ActiveRemaining == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("task permits leaked", views, err)
		}
		time.Sleep(time.Millisecond)
	}
}

// 准备阶段冻结的代次不能被停止后的模型新工具调用刷新；新明确轮次才可取得新代次。
func TestTaskPlanningContextCannotCrossStopBarrier(t *testing.T) {
	store := NewMemoryTaskStore()
	a := taskRuntimeFixture(t, TaskConfig{Store: store, Scope: "scope"})
	b := taskRuntimeFixture(t, TaskConfig{Store: store, Scope: "scope"})
	old, err := a.BindContext(t.Context(), "scope")
	if err != nil {
		t.Fatal(err)
	}
	if err = b.CancelAll(); err != nil {
		t.Fatal(err)
	}
	tool := Tool{Name: "sync", Effect: ToolEffectRead, Handler: func(context.Context, json.RawMessage) (string, error) { return "actual", nil }}
	if _, err = a.Start(old, "scope", "old-plan", "call", tool, json.RawMessage(`{}`)); !errors.Is(err, ErrTaskStopped) {
		t.Fatal("old planning context crossed stop barrier", err)
	}
	fresh, err := a.BindContext(old, "scope")
	if err != nil {
		t.Fatal(err)
	}
	row, err := a.Start(fresh, "scope", "new-explicit-plan", "call", tool, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Wait(t.Context(), "scope", []string{row.ID}, map[string]bool{"sync": true}, time.Second, true); err != nil {
		t.Fatal(err)
	}
}

// 期限触发但尚未交付的等待仍计入共享积压；终态提交后的释放丢失由维护补偿。
func TestWaitingBudgetPersistsUntilTerminalAndReconcilesRelease(t *testing.T) {
	store := NewMemoryTaskStore()
	r := taskRuntimeFixture(t, TaskConfig{Store: store, Scope: "scope", BudgetPool: "pool", Budgets: []TaskBudgetLimit{{Key: "global", MaxActive: 1, MaxConcurrent: 1, MaxWaiting: 1}}})
	wait, err := r.Suspend(t.Context(), "scope", "turn", "timer", TaskWaitSpec{Timeout: time.Hour, Checkpoint: "original"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	row, _ := store.GetWait(t.Context(), "scope", wait.ID)
	revision := row.Revision
	row.Spec.Deadline = time.Now().Add(-time.Hour)
	row.Revision++
	if ok, err := store.ReplaceWait(t.Context(), row, revision); err != nil || !ok {
		t.Fatal(ok, err)
	}
	views, err := r.Budget(t.Context())
	if err != nil || views[0].WaitingRemaining != 0 {
		t.Fatal("unprocessed deadline lost waiting budget", views, err)
	}
	// 模拟终态已持久但进程在释放pool前退出，Collect只补释放、不执行任何业务。
	revision = row.Revision
	row.State = "delivered"
	row.Revision++
	if ok, err := store.ReplaceWait(t.Context(), row, revision); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err = r.Collect(t.Context()); err != nil {
		t.Fatal(err)
	}
	views, err = r.Budget(t.Context())
	if err != nil || views[0].WaitingRemaining != 1 {
		t.Fatal("terminal budget release was not reconciled", views, err)
	}
}
