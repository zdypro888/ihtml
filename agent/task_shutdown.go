package agent

import (
	"context"
	"time"
)

// enterAdmission 使等待登记和维护提交也参与退出屏障，避免 Drain 返回后才写入新的受理记录。
func (r *TaskRuntime) enterAdmission() (func(), error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.ctx.Err() != nil {
		return nil, ErrTaskDraining
	}
	r.wg.Add(1)
	return r.wg.Done, nil
}

// TaskRuntimeStatus 只包含本实例的容量和通知损失；共享权威状态仍以持久存储为准。
type TaskRuntimeStatus struct {
	Draining        bool   `json:"draining"`
	Active          int    `json:"local_active"`
	Admitting       int    `json:"local_admitting"`
	MaxActive       int    `json:"max_active"`
	MaxConcurrent   int    `json:"max_concurrent"`
	CallbackDropped uint64 `json:"callback_dropped"`
}

// Status 是不触发维护或业务调用的瞬时诊断快照。
func (r *TaskRuntime) Status() TaskRuntimeStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return TaskRuntimeStatus{Draining: r.closed, Active: len(r.tasks), Admitting: r.reserved, MaxActive: r.cfg.MaxTasks, MaxConcurrent: r.cfg.MaxConcurrent, CallbackDropped: r.callbackDropped.Load()}
}

// Drain 拒绝新的执行接纳，等待已受理工作真实收尾；不提升停止代次，不重放工作。
func (r *TaskRuntime) Drain(ctx context.Context) error {
	r.beginDrain()
	select {
	case <-r.workersDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (r *TaskRuntime) beginDrain() {
	r.mu.Lock()
	r.closed = true
	r.drainOnce.Do(func() { go func() { r.wg.Wait(); close(r.workersDone) }() })
	r.mu.Unlock()
}

// Shutdown 有界取消并等待；超时只报告仍未退出，不能把忽略取消的执行者伪称已停止。
func (r *TaskRuntime) Shutdown(ctx context.Context) error {
	r.beginDrain()
	r.cancel()
	r.shutdownOnce.Do(func() {
		go func() {
			<-r.workersDone
			<-r.maintenanceDone
			r.closeOnce.Do(func() { close(r.completed); r.observer.close() })
			close(r.shutdownDone)
		}()
	})
	select {
	case <-r.shutdownDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// AwaitResult 统一提供带取消和错误的等待；远端存储失败必须返回错误，不能只遗留一个永不关闭的 channel。
func (r *TaskRuntime) AwaitResult(ctx context.Context, scope, id string, allowed map[string]bool) (TaskInfo, error) {
	for {
		rows, err := r.Wait(ctx, scope, []string{id}, allowed, 30*time.Second, true)
		if err != nil {
			return TaskInfo{}, err
		}
		if len(rows) == 1 && taskTerminal(rows[0].State) {
			return rows[0], nil
		}
	}
}

// enqueueCallback 兼容旧无context回调，但只允许一个受阻回调，不能拖住调用收尾或无限制造 goroutine。
func (r *TaskRuntime) enqueueCallback(info TaskInfo) {
	if r.cfg.OnComplete == nil {
		return
	}
	// 回调与完成通道拥有各自的展示快照，宿主不能通过修改计数器影响其他消费者。
	info.Progress = cloneTaskProgress(info.Progress)
	select {
	case r.callbackSlot <- struct{}{}:
	default:
		r.callbackDropped.Add(1)
		return
	}
	go func() { defer func() { <-r.callbackSlot; _ = recover() }(); r.cfg.OnComplete(info) }()
}
