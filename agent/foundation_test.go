package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 同一明确目标的并发调用与挂起恢复共用额度；AI 上下文 ID 不参与预算身份。
func TestGoalBudgetSurvivesWakeAndConcurrentRuntimes(t *testing.T) {
	store := NewMemoryTaskStore()
	cfg := TaskConfig{Store: store, Scope: "scope", GoalLimits: TaskGoalLimits{ModelCalls: 7, ToolCalls: 2, Duration: time.Hour}}
	r := taskRuntimeFixture(t, cfg)
	bound, err := r.BindContext(t.Context(), "scope")
	if err != nil {
		t.Fatal(err)
	}
	var admitted atomic.Int32
	var wg sync.WaitGroup
	for range 30 {
		wg.Go(func() {
			if r.ChargeGoal(bound, TaskGoalCharge{ModelCalls: 1}) == nil {
				admitted.Add(1)
			}
		})
	}
	wg.Wait()
	if admitted.Load() != 7 {
		t.Fatal("non-atomic budget", admitted.Load())
	}
	wait, err := r.Suspend(bound, "scope", "turn", "wait", TaskWaitSpec{Timeout: time.Hour, Checkpoint: "public facts"}, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	restored := taskRuntimeFixture(t, cfg)
	wake, err := restored.BindWaitContext(t.Context(), "scope", wait.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = restored.ChargeGoal(wake, TaskGoalCharge{ModelCalls: 1}); err == nil {
		t.Fatal("wake reset budget")
	}
	view, err := restored.GoalBudget(wake)
	if err != nil || view.Reserved.ModelCalls != 7 {
		t.Fatal(view, err)
	}
	fresh, err := restored.BindContext(t.Context(), "scope")
	if err != nil {
		t.Fatal(err)
	}
	if err = restored.ChargeGoal(fresh, TaskGoalCharge{ModelCalls: 1}); err != nil {
		t.Fatal("explicit new instruction rejected", err)
	}
}

func TestGoalBudgetRequiresReliableQuoteAndKeepsUnknownReservation(t *testing.T) {
	r := taskRuntimeFixture(t, TaskConfig{GoalLimits: TaskGoalLimits{CostMicros: 10, InputTokens: 100, OutputTokens: 50}})
	ctx, err := r.BindContext(t.Context(), "scope")
	if err != nil {
		t.Fatal(err)
	}
	if r.ChargeGoal(ctx, TaskGoalCharge{ModelCalls: 1}) == nil {
		t.Fatal("unpriced call admitted")
	}
	if err = r.ChargeGoal(ctx, TaskGoalCharge{ModelCalls: 1, InputTokens: 20, OutputTokens: 30, CostMicros: 7, Quoted: true}); err != nil {
		t.Fatal(err)
	}
	if r.ChargeGoal(ctx, TaskGoalCharge{ModelCalls: 1, CostMicros: 4, Quoted: true}) == nil {
		t.Fatal("cost overspend")
	}
	view, _ := r.GoalBudget(ctx)
	if view.Reserved.CostMicros != 7 {
		t.Fatal(view)
	}
}

func TestReferenceInputKeepsExactIntegersAndChecksPermission(t *testing.T) {
	r := taskRuntimeFixture(t, TaskConfig{})
	source := Tool{Name: "source", Effect: ToolEffectRead, Handler: func(context.Context, json.RawMessage) (string, error) {
		return `{"x/y":[9007199254740993,"你好"]}`, nil
	}}
	info, err := r.Start(t.Context(), "scope", "turn", "source", source, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := r.Wait(t.Context(), "scope", []string{info.ID}, map[string]bool{"source": true}, time.Second, true)
	if err != nil {
		t.Fatal(err)
	}
	ref := TaskResultInput{ID: info.ID, SHA256: rows[0].ResultRef.SHA256, Pointer: "/x~1y/0"}
	ctx := r.ResultInputContext(t.Context(), "scope", []Tool{source})
	data, err := ReadTaskResultInput(ctx, ref)
	if err != nil || string(data) != "9007199254740993" {
		t.Fatal(string(data), err)
	}
	if _, err = ReadTaskResultInput(r.ResultInputContext(t.Context(), "scope", nil), ref); !errors.Is(err, ErrTaskNotFound) {
		t.Fatal("permission leaked", err)
	}
	ref.SHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
	if _, err = ReadTaskResultInput(ctx, ref); !errors.Is(err, ErrTaskConflict) {
		t.Fatal(err)
	}
}

// 资源冲突使用共享 CAS，真正的提供者仍须检查 fencing 和 IfVersion。
func TestResourceLeasesSerializeAndFenceAcrossRuntimes(t *testing.T) {
	control := NewMemoryTaskControlStore()
	cfg := TaskConfig{ControlStore: control, BudgetPool: "pool", PollInterval: time.Millisecond, LeaseDuration: time.Second}
	a, b := taskRuntimeFixture(t, cfg), taskRuntimeFixture(t, cfg)
	claims := []TaskResourceRequest{{Key: "tenant/item", IfVersion: "rev-1"}}
	until := time.Now().Add(time.Second)
	if err := a.acquireBudget(t.Context(), "a", "epoch", "a", "task", until, claims...); err != nil {
		t.Fatal(err)
	}
	if err := b.acquireBudget(t.Context(), "b", "epoch", "b", "task", until, claims...); err != nil {
		t.Fatal(err)
	}
	if err := b.updateBudget(t.Context(), "b", "run", until); err == nil {
		t.Fatal("FIFO violated")
	}
	if err := a.updateBudget(t.Context(), "a", "run", until); err != nil {
		t.Fatal(err)
	}
	ctx, err := a.resourceContext(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	first := TaskResourceGrants(ctx)[0]
	if first.IfVersion != "rev-1" || first.Fence < 1 {
		t.Fatal(first)
	}
	if b.updateBudget(t.Context(), "b", "run", until) == nil {
		t.Fatal("conflicting resource ran")
	}
	a.releaseBudget("a")
	if err = b.updateBudget(t.Context(), "b", "run", until); err != nil {
		t.Fatal(err)
	}
	ctx, err = b.resourceContext(t.Context(), "b")
	if err != nil {
		t.Fatal(err)
	}
	if TaskResourceGrants(ctx)[0].Fence <= first.Fence {
		t.Fatal("fence did not advance")
	}
	b.releaseBudget("b")
}

func TestSharedRateCooldownAndPolicyVersion(t *testing.T) {
	control := NewMemoryTaskControlStore()
	limits := []TaskBudgetLimit{{Key: "shared", MaxActive: 20, MaxConcurrent: 20, MaxWaiting: 20}}
	cfg := TaskConfig{ControlStore: control, BudgetPool: "pool", Budgets: limits, QoS: TaskQoSConfig{Policies: []TaskResourcePolicy{{Key: "shared", RatePerSecond: 1000, Burst: 1, Version: 1}}}}
	a := taskRuntimeFixture(t, cfg)
	until := time.Now().Add(time.Hour)
	if err := a.acquireBudget(t.Context(), "s", "e", "a", "task", until); err != nil {
		t.Fatal(err)
	}
	if err := a.CooldownResource(t.Context(), "shared", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if a.updateBudget(t.Context(), "a", "run", until) == nil {
		t.Fatal("cooldown bypassed")
	}
	a.releaseBudget("a")
	cfg.BudgetPolicyVersion = 2
	cfg.Budgets = []TaskBudgetLimit{{Key: "shared", MaxActive: 1, MaxConcurrent: 1, MaxWaiting: 1}}
	b := taskRuntimeFixture(t, cfg)
	if err := b.acquireBudget(t.Context(), "s", "e", "new", "task", until); err != nil {
		t.Fatal(err)
	}
	if a.acquireBudget(t.Context(), "s", "e", "old", "task", until) == nil {
		t.Fatal("stale capacity policy admitted")
	}
	b.releaseBudget("new")
	// rate 桶的 CAS 状态重建后保留，不通过新 limiter 获得额外 burst。
	c := taskRuntimeFixture(t, TaskConfig{ControlStore: control, BudgetPool: "rate-only", QoS: TaskQoSConfig{Policies: []TaskResourcePolicy{{Key: "q", RatePerSecond: 10, Burst: 1, Version: 1}}}})
	if err := c.acquireBudget(t.Context(), "s", "e", "c", "task", until); err != nil {
		t.Fatal(err)
	}
	if c.updateBudget(t.Context(), "c", "run", until) == nil {
		t.Fatal("new policy should start empty")
	}
	time.Sleep(110 * time.Millisecond)
	if err := c.updateBudget(t.Context(), "c", "run", until); err != nil {
		t.Fatal(err)
	}
	if err := c.acquireBudget(t.Context(), "s", "e", "d", "task", until); err != nil {
		t.Fatal(err)
	}
	if c.updateBudget(t.Context(), "d", "run", until) == nil {
		t.Fatal("rate double spent")
	}
	c.releaseBudget("c")
	c.releaseBudget("d")
}

type faultOutbox struct {
	entry     TaskOutboxEntry
	ackLost   bool
	delivered bool
}

func (s *faultOutbox) ClaimOutbox(_ context.Context, owner string, _ time.Duration) (TaskOutboxEntry, bool, error) {
	if s.delivered {
		return s.entry, false, nil
	}
	s.entry.Owner = owner
	s.entry.Revision++
	s.entry.LeaseUntil = time.Now().Add(time.Second)
	return s.entry, true, nil
}
func (s *faultOutbox) AckOutbox(context.Context, TaskOutboxEntry) error {
	if !s.ackLost {
		s.ackLost = true
		return errors.New("lost ack")
	}
	s.delivered = true
	return nil
}
func (s *faultOutbox) RetryOutbox(context.Context, TaskOutboxEntry, time.Time) error { return nil }

func TestOutboxAckLossOnlyRedeliversFact(t *testing.T) {
	r := taskRuntimeFixture(t, TaskConfig{})
	p := r.EventPublisher()
	source, err := p.OpenSource(t.Context(), "scope", "native-transaction", "facts")
	if err != nil {
		t.Fatal(err)
	}
	entry, err := NewTaskOutboxEntry("scope", source.ID, "stable-business-commit", "changed", json.RawMessage(`{"revision":9}`), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	store := &faultOutbox{entry: entry}
	if _, err = DeliverTaskOutbox(t.Context(), store, p, "worker-1"); err == nil {
		t.Fatal("expected lost ack")
	}
	if _, err = DeliverTaskOutbox(t.Context(), store, p, "worker-2"); err != nil {
		t.Fatal(err)
	}
	page, err := r.ReadEvents(t.Context(), "scope", source.ID, 0, 8)
	if err != nil || len(page.Events) != 1 || !store.delivered {
		t.Fatal(page, err)
	}
}

func TestGoalBudgetCannotResetThroughNativeResumeOrExplicitApproval(t *testing.T) {
	r := taskRuntimeFixture(t, TaskConfig{Scope: "scope", GoalLimits: TaskGoalLimits{ModelCalls: 1}})
	first, err := r.UseTurnGoal(t.Context(), "scope", "trusted-original-invocation")
	if err != nil {
		t.Fatal(err)
	}
	if err = r.ChargeGoal(first, TaskGoalCharge{ModelCalls: 1}); err != nil {
		t.Fatal(err)
	}
	if err = r.CancelAll(); err != nil {
		t.Fatal(err)
	}
	current, err := r.BindContext(t.Context(), "scope")
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := r.UseTurnGoal(current, "scope", "trusted-original-invocation")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.UseContext(resumed, "scope"); err != nil {
		t.Fatal("resume borrowed old stopped epoch", err)
	}
	if r.ChargeGoal(resumed, TaskGoalCharge{ModelCalls: 1}) == nil {
		t.Fatal("native resume reset quota")
	}
	confirmed, err := r.ContinueGoal(current, "scope", first)
	if err != nil {
		t.Fatal(err)
	}
	if r.ChargeGoal(confirmed, TaskGoalCharge{ModelCalls: 1}) == nil {
		t.Fatal("approval reset quota")
	}
}

func TestMissingGoalAccountFailsClosed(t *testing.T) {
	control := NewMemoryTaskControlStore()
	r := taskRuntimeFixture(t, TaskConfig{ControlStore: control, GoalLimits: TaskGoalLimits{ModelCalls: 1}})
	ctx, err := r.BindContext(t.Context(), "scope")
	if err != nil {
		t.Fatal(err)
	}
	if err = r.ChargeGoal(ctx, TaskGoalCharge{ModelCalls: 1}); err != nil {
		t.Fatal(err)
	}
	b := goalBinding(ctx)
	control.mu.Lock()
	delete(control.rows, "goal/"+taskHash([]string{r.cfg.ControlNamespace, b.Scope, b.ID}))
	control.mu.Unlock()
	if err = r.ChargeGoal(ctx, TaskGoalCharge{ModelCalls: 1}); !errors.Is(err, ErrTaskNotFound) {
		t.Fatal("lost account recreated fresh quota", err)
	}
}

func TestRateLimitedHeadDoesNotBlockUnrelatedEligibleWork(t *testing.T) {
	control := NewMemoryTaskControlStore()
	base := TaskConfig{ControlStore: control, BudgetPool: "fair", Budgets: []TaskBudgetLimit{{Key: "global", MaxActive: 10, MaxConcurrent: 10, MaxWaiting: 10}}}
	slow := base
	slow.QoS = TaskQoSConfig{Policies: []TaskResourcePolicy{{Key: "slow-provider", RatePerSecond: 0.1, Burst: 1, Version: 1}}}
	a, b := taskRuntimeFixture(t, slow), taskRuntimeFixture(t, base)
	until := time.Now().Add(time.Minute)
	if err := a.acquireBudget(t.Context(), "a", "e", "a", "task", until); err != nil {
		t.Fatal(err)
	}
	if err := b.acquireBudget(t.Context(), "b", "e", "b", "task", until); err != nil {
		t.Fatal(err)
	}
	if err := b.updateBudget(t.Context(), "b", "run", until); err != nil {
		t.Fatal("ineligible earlier provider blocked other work", err)
	}
	a.releaseBudget("a")
	b.releaseBudget("b")
}

// 禁用用带更高版本的零策略发布；重建实例不继承旧桶/冷却，旧实例也不能复活限流。
func TestSharedRateCanBeExplicitlyDisabledWithoutResurrection(t *testing.T) {
	control := NewMemoryTaskControlStore()
	cfg := TaskConfig{ControlStore: control, BudgetPool: "disable-rate", Budgets: []TaskBudgetLimit{{Key: "shared", MaxActive: 10, MaxConcurrent: 10, MaxWaiting: 10}}, QoS: TaskQoSConfig{Policies: []TaskResourcePolicy{{Key: "shared", RatePerSecond: 0.1, Burst: 1, Version: 1}}}}
	old := taskRuntimeFixture(t, cfg)
	until := time.Now().Add(time.Hour)
	if err := old.acquireBudget(t.Context(), "scope", "epoch", "old", "task", until); err != nil {
		t.Fatal(err)
	}
	if err := old.CooldownResource(t.Context(), "shared", until); err != nil {
		t.Fatal(err)
	}
	old.releaseBudget("old")
	cfg.QoS.Policies = []TaskResourcePolicy{{Key: "shared", Version: 2}}
	disabled := taskRuntimeFixture(t, cfg)
	for _, id := range []string{"one", "two"} {
		if err := disabled.acquireBudget(t.Context(), "scope", "epoch", id, "task", until); err != nil {
			t.Fatal(err)
		}
		if err := disabled.updateBudget(t.Context(), id, "run", until); err != nil {
			t.Fatal("disabled bucket/cooldown still enforced", err)
		}
		disabled.releaseBudget(id)
	}
	if err := old.acquireBudget(t.Context(), "scope", "epoch", "stale", "task", until); !errors.Is(err, ErrTaskPolicyConflict) {
		t.Fatal("old instance resurrected limiter", err)
	}
	if err := old.CooldownResource(t.Context(), "shared", until); !errors.Is(err, ErrTaskPolicyConflict) {
		t.Fatal("old instance restored cooldown", err)
	}
}
