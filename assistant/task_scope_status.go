package assistant

import (
	"context"
	"time"

	"github.com/zdypro888/ihtml"
)

// SnapshotEvents 用当前任务账本恢复停止入口；历史 task_waiting 只表示过去发生过挂起。
func (s *session) SnapshotEvents(ctx context.Context) ([]ihtml.ChatStreamEvent, error) {
	if s.tasks == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	status, err := s.tasks.ScopeStatus(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	stopped := s.taskWakeStopped
	s.mu.Unlock()
	snapshot := &ihtml.ChatTaskScopeStatus{ActiveTasks: status.ActiveTasks, ActiveWaits: status.ActiveWaits, CanStop: !stopped && (status.ActiveTasks > 0 || status.ActiveWaits > 0), CheckedAt: status.CheckedAt}
	return []ihtml.ChatStreamEvent{{Type: ihtml.ChatStreamTaskScopeStatus, TaskScope: snapshot}}, nil
}

func (s *session) publishTaskScopeStatus() {
	events, err := s.SnapshotEvents(s.ctx)
	if err != nil {
		return
	}
	// 状态订阅是展示能力；断开的订阅不能反过来取消仍在执行的后台任务。
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	defer func() { _ = recover() }()
	if s.ctx.Err() != nil || s.sctx.Emit == nil {
		return
	}
	for _, event := range events {
		_ = s.sctx.Emit(event)
	}
}
