package agent

import (
	"context"
	"errors"
	"sync"
	"time"
)

type taskProgressRef struct{ scope, id string }

// 邮箱只保存有界任务引用，同一任务的新进度和终态合并为一次“重读原账本”。
// 溢出不丢弃最终事实：标记分页核对，通知正文始终来自持久真值。
type taskProgressMailbox struct {
	mu      sync.Mutex
	limit   int
	pending map[taskProgressRef]bool
	queue   []taskProgressRef
	rescan  bool
	wake    chan struct{}
}

func newTaskProgressMailbox(limit int) *taskProgressMailbox {
	return &taskProgressMailbox{limit: limit, pending: map[taskProgressRef]bool{}, wake: make(chan struct{}, 1)}
}

func (m *taskProgressMailbox) signal() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}
func (m *taskProgressMailbox) add(ref taskProgressRef) {
	m.mu.Lock()
	if !m.pending[ref] {
		if len(m.queue) >= m.limit {
			m.rescan = true
		} else {
			m.pending[ref] = true
			m.queue = append(m.queue, ref)
		}
	}
	m.mu.Unlock()
	m.signal()
}
func (m *taskProgressMailbox) take(sinceScan int) (taskProgressRef, bool, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// 队列持续繁忙时也要给溢出核对机会，不能让未入队的终态永久饿死。
	if m.rescan && (len(m.queue) == 0 || sinceScan >= m.limit) {
		m.rescan = false
		return taskProgressRef{}, false, true
	}
	if len(m.queue) == 0 {
		return taskProgressRef{}, false, false
	}
	ref := m.queue[0]
	copy(m.queue, m.queue[1:])
	m.queue = m.queue[:len(m.queue)-1]
	delete(m.pending, ref)
	return ref, true, false
}
func (m *taskProgressMailbox) retry() { m.mu.Lock(); m.rescan = true; m.mu.Unlock(); m.signal() }

func (r *TaskRuntime) enqueueProgressCallback(record TaskRecord) {
	if r.cfg.OnProgress == nil || r.ctx.Err() != nil || record.ID == "" || record.Scope == "" || r.cfg.Scope != "" && record.Scope != r.cfg.Scope || record.Info.Progress == nil && !taskTerminal(record.Info.State) {
		return
	}
	// 恢复补账可能从未在本实例 Start；登记已验证的持久记录作用域，保证溢出后仍能核对。
	r.mu.Lock()
	r.seenScopes[record.Scope] = true
	r.mu.Unlock()
	r.progressMailbox.add(taskProgressRef{record.Scope, record.ID})
}

// 每个运行时至多一个展示分发器；慢回调不会占执行槽、租约循环或旧OnComplete回调槽。
// 用户回调若永不返回，只占这一个goroutine；Shutdown不等待不受控的用户回调。
func (r *TaskRuntime) dispatchTaskProgress() {
	sinceScan := 0
	for r.ctx.Err() == nil {
		ref, found, rescan := r.progressMailbox.take(sinceScan)
		var err error
		if rescan {
			sinceScan = 0
			err = r.rescanTaskProgress()
		} else if found {
			sinceScan++
			err = r.deliverTaskProgress(ref)
		} else {
			select {
			case <-r.ctx.Done():
				return
			case <-r.progressMailbox.wake:
			}
			continue
		}
		if err != nil && r.ctx.Err() == nil {
			r.progressMailbox.retry()
			// 仅真实存储故障后的有界重试；正常运行不轮询数据库。
			timer := time.NewTimer(r.cfg.PollInterval)
			select {
			case <-r.ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
}

func (r *TaskRuntime) deliverTaskProgress(ref taskProgressRef) error {
	ctx, stop := context.WithTimeout(r.ctx, 5*time.Second)
	current, err := r.cfg.Store.GetTask(ctx, ref.scope, ref.id)
	stop()
	if errors.Is(err, ErrTaskNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if r.ctx.Err() != nil || current.Info.Progress == nil && !taskTerminal(current.Info.State) {
		return nil
	}
	info := current.Info
	info.Progress = cloneTaskProgress(info.Progress)
	info.Result, info.Error, info.Input, info.Failure, info.ResultRef, info.ResolutionRef = "", "", nil, nil, nil, nil
	// 回调前重读可能得到更新的终态；绝不发送排队时复制的旧running快照。
	func() { defer func() { _ = recover() }(); r.cfg.OnProgress(info) }()
	return nil
}

func (r *TaskRuntime) rescanTaskProgress() error {
	r.mu.Lock()
	scopes := make([]string, 0, len(r.seenScopes))
	if r.cfg.Scope != "" {
		// 固定作用域的新实例也能恢复旧账本，不依赖本实例曾发起过任务。
		scopes = append(scopes, r.cfg.Scope)
	} else {
		for scope := range r.seenScopes {
			scopes = append(scopes, scope)
		}
	}
	r.mu.Unlock()
	for _, scope := range scopes {
		after := ""
		for r.ctx.Err() == nil {
			ctx, stop := context.WithTimeout(r.ctx, 5*time.Second)
			rows, err := r.cfg.Store.ListTasks(ctx, scope, TaskScan{After: after, Limit: 128})
			stop()
			if err != nil {
				return err
			}
			for _, record := range rows {
				if record.Info.Progress != nil || taskTerminal(record.Info.State) {
					if err := r.deliverTaskProgress(taskProgressRef{scope, record.ID}); err != nil {
						return err
					}
				}
			}
			if len(rows) < 128 {
				break
			}
			after = rows[len(rows)-1].ID
		}
	}
	return nil
}
