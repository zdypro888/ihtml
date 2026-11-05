package agent

import (
	"context"
	"crypto/rand"
	"errors"
	"time"
)

// Suspend 保存 AI 主动声明的等待条件。该记录本身不包含新的业务授权。
func (r *TaskRuntime) Suspend(ctx context.Context, scope, turn, key string, spec TaskWaitSpec, allowed map[string]bool) (TaskWaitRecord, error) {
	if TaskWakeDepth(ctx) >= r.cfg.MaxWakeDepth {
		return TaskWaitRecord{}, ErrTaskWakeBudget
	}
	requested := spec
	if spec.Timeout > 0 {
		spec.Deadline = time.Now().UTC().Add(spec.Timeout)
		requested.Deadline = time.Time{}
	}
	if scope == "" || len(scope) > 256 || r.cfg.Scope != "" && r.cfg.Scope != scope || turn == "" || key == "" || len(spec.IDs) > 128 || len(spec.Checkpoint) == 0 || len(spec.Checkpoint) > 8192 || !spec.Deadline.After(time.Now()) || spec.Deadline.After(time.Now().Add(24*time.Hour+time.Second)) {
		return TaskWaitRecord{}, errors.New("agent: invalid suspension")
	}
	epoch, err := r.scopeEpoch(ctx, scope)
	if err != nil {
		return TaskWaitRecord{}, err
	}
	if err = r.checkEpoch(ctx, scope, epoch, true); err != nil {
		return TaskWaitRecord{}, err
	}
	r.mu.Lock()
	r.seenScopes[scope] = true
	r.mu.Unlock()
	seen := map[string]bool{}
	for _, id := range spec.IDs {
		if seen[id] {
			return TaskWaitRecord{}, errors.New("agent: duplicate wait handle")
		}
		seen[id] = true
		rec, err := r.cfg.Store.GetTask(ctx, scope, id)
		if err != nil || !allowed[rec.Info.Tool] {
			return TaskWaitRecord{}, ErrTaskNotFound
		}
	}
	// 事件注册原子去重；同一工具调用改变条件不能复用旧注册。
	record := TaskWaitRecord{ID: rand.Text(), Scope: scope, Turn: turn, Key: taskHash([]string{scope, turn, key}), Digest: taskHash([]any{requested.IDs, requested.All, requested.Timeout, requested.Deadline, requested.Checkpoint}), Spec: spec, State: "waiting", Depth: TaskWakeDepth(ctx) + 1, Revision: 1, CreatedAt: time.Now().UTC(), ExpiresAt: spec.Deadline.Add(r.cfg.LedgerRetention)}
	record.Epoch = epoch
	if prior, err := r.cfg.Store.FindWait(ctx, scope, record.Key); err == nil {
		if prior.Digest != record.Digest {
			return prior, ErrTaskConflict
		}
		return prior, nil
	} else if !errors.Is(err, ErrTaskNotFound) {
		return TaskWaitRecord{}, err
	}
	// 未交付的到期事件仍占等待额度；只有终态释放或账本保留期结束才可回收。
	if err = r.acquireBudget(ctx, scope, epoch, record.ID, "wait", record.ExpiresAt); err != nil {
		return TaskWaitRecord{}, err
	}
	old, created, err := r.cfg.Store.CreateWait(ctx, record)
	if err != nil || !created {
		r.releaseBudget(record.ID)
	}
	if err != nil {
		return old, err
	}
	if old.Digest != record.Digest {
		return old, ErrTaskConflict
	}
	if err = r.checkEpoch(ctx, scope, epoch, false); err != nil {
		_, _ = r.cancelWait(ctx, scope, old.ID)
		return old, err
	}
	return old, nil
}

// ClaimWake 只按 AI 保存的条件匹配。CAS 与可续租交付防止多个实例重复入队。
// 不查询任何业务特定字段，也不自动重试原 Handler。
func (r *TaskRuntime) ClaimWake(ctx context.Context, scope, consumer string) (*TaskWaitRecord, error) {
	if scope == "" || r.cfg.Scope != "" && r.cfg.Scope != scope || consumer == "" {
		return nil, errors.New("agent: wake scope required")
	}
	after := ""
	now := time.Now().UTC()
	for {
		rows, err := r.cfg.Store.ListWaits(ctx, scope, TaskScan{After: after, Limit: 128, ActiveOnly: true})
		if err != nil {
			return nil, err
		}
		for _, w := range rows {
			if err := r.checkEpoch(ctx, scope, w.Epoch, false); err != nil {
				if !errors.Is(err, ErrTaskStopped) {
					return nil, err
				}
				if _, err = r.cancelWait(ctx, scope, w.ID); err != nil {
					return nil, err
				}
				continue
			}
			if w.State != "waiting" && !(w.State == "delivering" && !w.LeaseUntil.After(now)) {
				continue
			}
			if w.RetryAt.After(now) {
				continue
			}
			ready := false
			reason := "tasks_finished"
			if !w.Spec.Deadline.After(now) {
				ready = true
				reason = "deadline"
			} else if len(w.Spec.IDs) > 0 {
				finished := 0
				for _, id := range w.Spec.IDs {
					rec, e := r.cfg.Store.GetTask(ctx, scope, id)
					if errors.Is(e, ErrTaskNotFound) {
						reason = "record_unavailable"
						finished++
						continue
					}
					if e != nil {
						return nil, e
					}
					rec, e = r.refresh(ctx, rec)
					if e != nil {
						return nil, e
					}
					if taskTerminal(rec.Info.State) {
						finished++
					}
				}
				ready = finished == len(w.Spec.IDs) || !w.Spec.All && finished > 0
			}
			if !ready {
				continue
			}
			rev := w.Revision

			w.State = "delivering"
			w.Owner = consumer
			w.Reason = reason
			w.LeaseUntil = now.Add(r.cfg.LeaseDuration)
			w.Revision++
			ok, e := r.cfg.Store.ReplaceWait(ctx, w, rev)
			if e != nil {
				return nil, e
			}
			if ok {
				return &w, nil
			}
		}
		if len(rows) < 128 {
			return nil, nil
		}
		after = rows[len(rows)-1].ID
	}
}

// UpdateWake 对交付租约做 CAS；只有持有者可续租/确认。失败重投仍使用同一个 RequestID。
func (r *TaskRuntime) UpdateWake(ctx context.Context, scope, id, consumer, action string) error {
	if scope == "" || r.cfg.Scope != "" && r.cfg.Scope != scope {
		return ErrTaskNotFound
	}
	w, err := r.cfg.Store.GetWait(ctx, scope, id)
	if err != nil {
		return err
	}
	if w.State == "delivered" && action == "ack" {
		return nil
	}
	if w.State != "delivering" || w.Owner != consumer || !w.LeaseUntil.After(time.Now()) {
		return ErrTaskConflict
	}
	if action == "renew" || action == "admit" {
		if err := r.checkEpoch(ctx, scope, w.Epoch, action == "admit"); err != nil {
			return err
		}
	}
	rev := w.Revision
	budgetExhausted := false
	switch action {
	case "admit":
		if w.Attempts >= r.cfg.MaxWakeAttempts {
			w.State = "failed"
			w.LeaseUntil = time.Time{}
			budgetExhausted = true
		} else {
			w.Attempts++
		}
	case "renew":
		w.LeaseUntil = time.Now().Add(r.cfg.LeaseDuration)
	case "ack":
		w.State = "delivered"
		w.LeaseUntil = time.Time{}
	case "release":
		w.State = "waiting"
		w.LeaseUntil = time.Time{}
		w.RetryAt = time.Now().Add(r.cfg.PollInterval * 5)
	case "cancel":
		w.State = "cancelled"
		w.LeaseUntil = time.Time{}
	default:
		return errors.New("agent: invalid wake action")
	}
	w.Revision++
	ok, err := r.cfg.Store.ReplaceWait(ctx, w, rev)
	if err != nil {
		return err
	}
	if !ok {
		return ErrTaskConflict
	}
	if w.State == "delivered" || w.State == "cancelled" || w.State == "failed" {
		r.releaseBudget(w.ID)
	}
	if budgetExhausted {
		return ErrTaskWakeBudget
	}
	return nil
}

// CancelWaits 逐项重读并 CAS，不能把并发续租导致的版本冲突当成取消成功。
func (r *TaskRuntime) CancelWaits(ctx context.Context, scope string) error {
	if scope == "" || r.cfg.Scope != "" && r.cfg.Scope != scope {
		return ErrTaskNotFound
	}
	after := ""
	for {
		rows, err := r.cfg.Store.ListWaits(ctx, scope, TaskScan{After: after, Limit: 128, ActiveOnly: true})
		if err != nil {
			return err
		}
		for _, w := range rows {
			if _, err = r.cancelWait(ctx, scope, w.ID); err != nil {
				return err
			}
		}
		if len(rows) < 128 {
			return nil
		}
		after = rows[len(rows)-1].ID
	}
}

type taskWakeKey struct{}
type taskWakeContext struct {
	ID    string
	Depth int
}

// WithTaskWake 仅供可信宿主附加恢复身份和预算深度，不赋予工具权限。
func WithTaskWake(ctx context.Context, id string, depth ...int) context.Context {
	n := 0
	if len(depth) > 0 {
		n = depth[0]
	}
	return context.WithValue(ctx, taskWakeKey{}, taskWakeContext{id, n})
}

// TaskWakeID 读取可信队列的恢复身份，不采用客户端 RequestID 作为权限依据。
func TaskWakeID(ctx context.Context) string {
	w, _ := ctx.Value(taskWakeKey{}).(taskWakeContext)
	return w.ID
}

// TaskWakeDepth 保留同一目标的恢复深度，不能随轮次重建而重置预算。
func TaskWakeDepth(ctx context.Context) int {
	w, _ := ctx.Value(taskWakeKey{}).(taskWakeContext)
	return w.Depth
}
