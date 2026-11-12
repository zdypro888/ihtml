package agent

import (
	"sync"

	"github.com/zdypro888/ihtml/progress"
)

// taskProgress 只缓存一个有界快照并合并通知；高频上报不会创建额外 goroutine 或无界写队列。
type taskProgress struct {
	mu       sync.Mutex
	flushMu  sync.Mutex
	latest   *progress.Snapshot
	revision uint64
	saved    uint64
	changed  chan struct{}
}

func newTaskProgress() *taskProgress { return &taskProgress{changed: make(chan struct{}, 1)} }

func cloneTaskProgress(snapshot *progress.Snapshot) *progress.Snapshot {
	if snapshot == nil {
		return nil
	}
	copy, ok := progress.Normalize(*snapshot)
	if !ok {
		return nil
	}
	return &copy
}

func (p *taskProgress) report(snapshot progress.Snapshot) {
	copy := cloneTaskProgress(&snapshot)
	if copy == nil {
		return
	}
	p.mu.Lock()
	// 迟到事件不倒退已观测的公开进度；相同时间允许同一来源补充事实。
	if p.latest != nil && copy.UpdatedAt.Before(p.latest.UpdatedAt) {
		p.mu.Unlock()
		return
	}
	p.latest = copy
	p.revision++
	p.mu.Unlock()
	select {
	case p.changed <- struct{}{}:
	default:
	}
}

// flushTaskProgress 复用原 owner/lease/revision 的 CAS，进度永远不能恢复已结束或失去租约的调用。
// 存储失败保留 dirty 快照，下一次维护或终态前再尝试，不把展示问题升级成业务失败。
func (r *TaskRuntime) flushTaskProgress(t *toolTask) {
	p := t.progress
	if p == nil {
		return
	}
	p.flushMu.Lock()
	defer p.flushMu.Unlock()
	p.mu.Lock()
	if p.revision == p.saved || p.latest == nil {
		p.mu.Unlock()
		return
	}
	snapshot, revision := cloneTaskProgress(p.latest), p.revision
	p.mu.Unlock()
	ctx, stop := r.operation()
	defer stop()
	record, err := r.ownedUpdate(ctx, t, func(rec *TaskRecord) {
		if rec.Info.Progress == nil || !snapshot.UpdatedAt.Before(rec.Info.Progress.UpdatedAt) {
			rec.Info.Progress = snapshot
		}
	})
	if err != nil {
		return
	}
	p.mu.Lock()
	p.saved = revision
	p.mu.Unlock()
	r.signal()
	r.enqueueProgressCallback(record)
}
