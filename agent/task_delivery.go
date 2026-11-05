package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

// TaskOutboxFailureStore 在同一 Owner/Revision 租约下记录失败、死信和下一次可领取时间。
type TaskOutboxFailureStore interface {
	FailOutbox(context.Context, TaskOutboxEntry, string, time.Time, bool) error
}

// TaskOutboxAdminStore 供可信宿主诊断和显式重投；重投只保留原事实，不能替换载荷或重做业务。
type TaskOutboxAdminStore interface {
	ListOutbox(context.Context, string, int) ([]TaskOutboxEntry, error)
	RequeueOutbox(context.Context, string, int64) error
}

// DeliverTaskOutboxBatch 有界推进已提交事实，不调度业务。失败项已隔离时继续处理其余项，避免批首阻塞。
func DeliverTaskOutboxBatch(ctx context.Context, store TaskOutboxStore, publisher TaskEventPublisher, owner string, limit int) (int, error) {
	if limit < 1 || limit > 128 { return 0, ErrTaskConflict }
	var failures error
	count := 0
	for range limit {
		if err:=ctx.Err();err!=nil { return count, errors.Join(failures,err) }
		claimed,err:=DeliverTaskOutbox(ctx,store,publisher,owner)
		failures=errors.Join(failures,err)
		if !claimed { break };count++
	}
	return count,failures
}

func failTaskDelivery(ctx context.Context, s TaskOutboxStore, e TaskOutboxEntry, cause error) error {
	code := TaskFailureFrom(cause, "not_started", false, "", nil).Code
	dead := errors.Is(cause, ErrTaskResultExpired) || errors.Is(cause, ErrTaskConflict) || errors.Is(cause, ErrTaskNotFound) || errors.Is(cause, ErrTaskEventClosed) || errors.Is(cause, ErrTaskEventCapacity)
	// 退避只控制事实重投；故障首项暂时退出就绪队列，避免抢占其他可投递事实。
	delay := time.Second * time.Duration(1<<max(0, min(e.Attempts+1, 8)))
	if delay > 5*time.Minute {
		delay = 5 * time.Minute
	}
	next := time.Now().UTC().Add(delay)
	if f, ok := s.(TaskOutboxFailureStore); ok {
		return f.FailOutbox(ctx, e, code, next, dead)
	}
	return s.RetryOutbox(ctx, e, next)
}

// TaskOutboxGroup 复用同一投递器接入多个实际事务边界，Route 仅为本地可信适配选择。
type TaskOutboxGroup struct {
	Stores []TaskOutboxStore
	next   atomic.Uint64
}

func (g *TaskOutboxGroup) ClaimOutbox(ctx context.Context, owner string, lease time.Duration) (TaskOutboxEntry, bool, error) {
	if len(g.Stores) == 0 {
		return TaskOutboxEntry{}, false, nil
	}
	start := int(g.next.Add(1)-1) % len(g.Stores)
	for n := 0; n < len(g.Stores); n++ {
		i := (start + n) % len(g.Stores)
		s := g.Stores[i]
		e, ok, err := s.ClaimOutbox(ctx, owner, lease)
		if err != nil {
			return e, false, err
		}
		if ok {
			e.Route = i
			return e, true, nil
		}
	}
	return TaskOutboxEntry{}, false, nil
}
func (g *TaskOutboxGroup) store(e TaskOutboxEntry) (TaskOutboxStore, error) {
	if e.Route < 0 || e.Route >= len(g.Stores) {
		return nil, ErrTaskConflict
	}
	return g.Stores[e.Route], nil
}
func (g *TaskOutboxGroup) AckOutbox(ctx context.Context, e TaskOutboxEntry) error {
	s, err := g.store(e)
	if err != nil {
		return err
	}
	return s.AckOutbox(ctx, e)
}
func (g *TaskOutboxGroup) RetryOutbox(ctx context.Context, e TaskOutboxEntry, at time.Time) error {
	s, err := g.store(e)
	if err != nil {
		return err
	}
	return s.RetryOutbox(ctx, e, at)
}
func (g *TaskOutboxGroup) FailOutbox(ctx context.Context, e TaskOutboxEntry, code string, at time.Time, dead bool) error {
	s, err := g.store(e)
	if err != nil {
		return err
	}
	if f, ok := s.(TaskOutboxFailureStore); ok {
		return f.FailOutbox(ctx, e, code, at, dead)
	}
	return s.RetryOutbox(ctx, e, at)
}

// TaskStoreHealth 检查各真实投递边界，未配置发布器不导致副作用。
func (g *TaskOutboxGroup) TaskStoreHealth(ctx context.Context) error {
	for _, s := range g.Stores {
		if h, ok := s.(TaskStoreHealth); ok {
			if err := h.TaskStoreHealth(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}
