package agent

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"
)

// 执行名额与账本数量分离；超过旧的累计上限仍能执行，重建运行时不重放已完成的调用。
func TestDurableTaskLedgerRecreationAndActiveCapacity(t *testing.T) {
	store := NewMemoryTaskStore()
	r := taskRuntimeFixture(t, TaskConfig{Store: store, MaxTasks: 1, PollInterval: time.Millisecond})
	var calls atomic.Int32
	tool := Tool{Name: "unrelated_sync", Effect: ToolEffectRead, Handler: func(context.Context, json.RawMessage) (string, error) { calls.Add(1); return "actual", nil }}
	first := ""
	for i := range 140 {
		key := string(rune(i + 100))
		row, err := r.Start(t.Context(), "owner", "turn", key, tool, json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(i, err)
		}
		if i == 0 {
			first = row.ID
		}
		if _, err = r.Wait(t.Context(), "owner", []string{row.ID}, map[string]bool{tool.Name: true}, time.Second, true); err != nil {
			t.Fatal(err)
		}
	}
	r.Close()
	restored := taskRuntimeFixture(t, TaskConfig{Store: store, MaxTasks: 1})
	old, err := restored.Start(t.Context(), "owner", "turn", string(rune(100)), tool, json.RawMessage(`{}`))
	if err != nil || old.ID != first || old.Result != "actual" || calls.Load() != 140 {
		t.Fatal(old, err, calls.Load())
	}
	rec, _ := store.GetTask(t.Context(), "owner", first)
	rev := rec.Revision
	rec.ResultExpiresAt = time.Now().Add(-time.Second)
	rec.Revision++
	if ok, err := store.ReplaceTask(t.Context(), rec, rev); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err = restored.Collect(t.Context()); err != nil {
		t.Fatal(err)
	}
	old, err = restored.Start(t.Context(), "owner", "turn", string(rune(100)), tool, json.RawMessage(`{}`))
	if err != nil || old.ID != first || old.State != "completed" || old.Result != "" || !old.ResultExpired || calls.Load() != 140 {
		t.Fatal("reclaimed receipt lost dedup/state", old, err, calls.Load())
	}
	if _, err = restored.Read("foreign", []string{first}, map[string]bool{tool.Name: true}); err == nil {
		t.Fatal("scope leaked")
	}
}

// 崩溃租约恢复只转换状态，不能把写入重新提交；原执行者的旧版本不得覆盖 unknown。
func TestDurableTaskRecoveryFencesOldWriterWithoutReplay(t *testing.T) {
	store := NewMemoryTaskStore()
	args := json.RawMessage(`{"target":"fixed"}`)
	old := TaskRecord{ID: "lost", Scope: "owner", Key: taskHash([]string{"owner", "turn", "key"}), Digest: taskHash([]any{"write", args}), Turn: "turn", Effect: ToolEffectWrite, Info: TaskInfo{ID: "lost", Tool: "write", State: "running"}, Owner: "dead-process", Started: true, Revision: 1, LeaseUntil: time.Now().Add(-time.Second), Deadline: time.Now().Add(time.Minute)}
	if _, _, err := store.CreateTask(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	r := taskRuntimeFixture(t, TaskConfig{Store: store})
	var calls atomic.Int32
	row, err := r.Start(t.Context(), "owner", "turn", "key", Tool{Name: "write", Effect: ToolEffectWrite, Handler: func(context.Context, json.RawMessage) (string, error) { calls.Add(1); return "replayed", nil }}, args)
	if err != nil || row.State != "unknown" || calls.Load() != 0 {
		t.Fatal(row, err, calls.Load())
	}
	old.Revision++
	old.Info.State = "completed"
	if ok, err := store.ReplaceTask(t.Context(), old, 1); err != nil || ok {
		t.Fatal("stale worker overwritten recovered state", ok, err)
	}
}

// 等待记录独立于 goroutine；条件晚注册、跨实例认领、交付租约和取消都使用同一持久协议。
func TestDurableTaskWaitClaimAcrossRuntimeRecreation(t *testing.T) {
	store := NewMemoryTaskStore()
	r := taskRuntimeFixture(t, TaskConfig{Store: store, LeaseDuration: 60 * time.Millisecond, PollInterval: time.Millisecond})
	row, err := r.Start(t.Context(), "owner", "turn", "call", Tool{Name: "read", Effect: ToolEffectRead, Handler: func(context.Context, json.RawMessage) (string, error) { return "done", nil }}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Wait(t.Context(), "owner", []string{row.ID}, map[string]bool{"read": true}, time.Second, true); err != nil {
		t.Fatal(err)
	}
	spec := TaskWaitSpec{IDs: []string{row.ID}, All: true, Timeout: time.Minute, Checkpoint: "original goal; fixed scope; inspect receipt"}
	w, err := r.Suspend(t.Context(), "owner", "turn", "suspend", spec, map[string]bool{"read": true})
	if err != nil {
		t.Fatal(err)
	}
	same, err := r.Suspend(t.Context(), "owner", "turn", "suspend", spec, map[string]bool{"read": true})
	if err != nil || same.ID != w.ID || !same.Spec.Deadline.Equal(w.Spec.Deadline) {
		t.Fatal("registration replay changed condition", same, err)
	}
	r.Close()
	r2 := taskRuntimeFixture(t, TaskConfig{Store: store, LeaseDuration: 60 * time.Millisecond, PollInterval: time.Millisecond})
	claimed, err := r2.ClaimWake(t.Context(), "owner", "consumer-a")
	if err != nil || claimed == nil || claimed.ID != w.ID {
		t.Fatal(claimed, err)
	}
	if duplicate, err := r2.ClaimWake(t.Context(), "owner", "consumer-b"); err != nil || duplicate != nil {
		t.Fatal("wake delivered twice", duplicate, err)
	}
	// 直接过期交付租约模拟消费进程崩溃，测试不依赖长时间 sleep。
	rows, _ := store.ListWaits(t.Context(), "owner", TaskScan{Limit: 10})
	rec := rows[0]
	rev := rec.Revision
	rec.LeaseUntil = time.Now().Add(-time.Second)
	rec.Revision++
	store.ReplaceWait(t.Context(), rec, rev)
	retry, err := r2.ClaimWake(t.Context(), "owner", "consumer-b")
	if err != nil || retry == nil || retry.ID != w.ID {
		t.Fatal(retry, err)
	}
	if err = r2.UpdateWake(t.Context(), "owner", w.ID, "consumer-a", "ack"); err == nil {
		t.Fatal("old consumer ack accepted")
	}
	if err = r2.UpdateWake(t.Context(), "owner", w.ID, "consumer-b", "ack"); err != nil {
		t.Fatal(err)
	}
	if again, err := r2.ClaimWake(t.Context(), "owner", "third"); err != nil || again != nil {
		t.Fatal(again, err)
	}
	timer, err := r2.Suspend(t.Context(), "owner", "next", "timer", TaskWaitSpec{Timeout: time.Millisecond, Checkpoint: "check status later"}, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if err = r2.CancelWaits(t.Context(), "owner"); err != nil {
		t.Fatal(err)
	}
	if event, err := r2.ClaimWake(t.Context(), "owner", "after-cancel"); err != nil || event != nil {
		t.Fatal("cancelled wait woke up", timer, event, err)
	}
}

// 未获模型准入的忙碌重投不消耗推理次数；实际失败恢复有上限，后续新用户目标可重新开始。
func TestTaskWakeBudgetsAndScopedRuntime(t *testing.T) {
	store := NewMemoryTaskStore()
	r := taskRuntimeFixture(t, TaskConfig{Store: store, Scope: "owner", MaxWakeAttempts: 2, MaxWakeDepth: 1, PollInterval: time.Millisecond})
	w, err := r.Suspend(t.Context(), "owner", "turn", "key", TaskWaitSpec{Timeout: time.Millisecond, Checkpoint: "one step"}, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := store.GetWait(t.Context(), "owner", w.ID)
	rev := rec.Revision
	rec.Spec.Deadline = time.Now().Add(-time.Second)
	rec.Revision++
	store.ReplaceWait(t.Context(), rec, rev)
	for range 4 {
		claim, err := r.ClaimWake(t.Context(), "owner", "busy")
		if err != nil || claim == nil {
			t.Fatal(claim, err)
		}
		if err = r.UpdateWake(t.Context(), "owner", w.ID, "busy", "release"); err != nil {
			t.Fatal(err)
		}
		rec, _ = store.GetWait(t.Context(), "owner", w.ID)
		rev = rec.Revision
		rec.RetryAt = time.Time{}
		rec.Revision++
		store.ReplaceWait(t.Context(), rec, rev)
	}
	rec, _ = store.GetWait(t.Context(), "owner", w.ID)
	if rec.Attempts != 0 {
		t.Fatal("busy consumed model budget", rec)
	}
	for i := range 3 {
		claim, err := r.ClaimWake(t.Context(), "owner", "consumer")
		if err != nil || claim == nil {
			t.Fatal(claim, err)
		}
		err = r.UpdateWake(t.Context(), "owner", w.ID, "consumer", "admit")
		if i == 2 {
			if err == nil {
				t.Fatal("wake budget not enforced")
			}
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if err = r.UpdateWake(t.Context(), "owner", w.ID, "consumer", "release"); err != nil {
			t.Fatal(err)
		}
		rec, _ = store.GetWait(t.Context(), "owner", w.ID)
		rev = rec.Revision
		rec.RetryAt = time.Time{}
		rec.Revision++
		store.ReplaceWait(t.Context(), rec, rev)
	}
	if _, err = r.Suspend(WithTaskWake(t.Context(), w.ID, 1), "owner", "next", "key", TaskWaitSpec{Timeout: time.Second, Checkpoint: "another automatic step"}, map[string]bool{}); err == nil {
		t.Fatal("automatic wake chain unbounded")
	}
	if _, err = r.Read("", nil, map[string]bool{}); err == nil {
		t.Fatal("empty scope bypass")
	}
	if _, err = r.ClaimWake(t.Context(), "other", "consumer"); err == nil {
		t.Fatal("scope override")
	}
}
