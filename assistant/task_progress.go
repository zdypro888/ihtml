package assistant

import (
	"time"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/agent"
	"github.com/zdypro888/ihtml/progress"
)

type taskProgressDisplayState struct {
	state    string
	goal     string
	progress *progress.Snapshot
	at       time.Time
}

func taskProgressTerminal(state string) bool {
	switch state {
	case "completed", "failed", "cancelled", "unknown":
		return true
	}
	return false
}

// taskProgressCanAdvance 同时约束事实时间和终态；晚到的工作线程通知不能复活已结束调用。
func taskProgressCanAdvance(previous string, prior *progress.Snapshot, next string, snapshot *progress.Snapshot) bool {
	if taskProgressTerminal(previous) && next != previous {
		// unknown 可被原不可变结果核对为明确终态，但不能借迟到进度恢复执行中。
		if previous != "unknown" || !taskProgressTerminal(next) {
			return false
		}
	}
	if previous == "running" && next == "queued" {
		return false
	}
	if !taskProgressTerminal(next) && prior != nil && snapshot != nil && snapshot.UpdatedAt.Before(prior.UpdatedAt) {
		return false
	}
	return true
}

// publishTaskProgress 不依赖某一轮模型投影；原调用的目标归属由运行时冻结，不能套用当前轮次。
// 展示投影失败只丢弃本次通知，真实进度仍在任务账本中，不能因此取消业务执行。
func (s *session) publishTaskProgress(info agent.TaskInfo) {
	if !info.Background || info.ID == "" || info.GoalID == "" || info.Progress == nil {
		return
	}
	snapshot, ok := progress.Normalize(*info.Progress)
	if !ok || snapshot.UpdatedAt.IsZero() {
		return
	}
	state := info.State
	status := ihtml.ChatToolRunning
	switch state {
	case "queued", "running", "cancelling":
	case "completed":
		status = ihtml.ChatToolAccepted
	case "failed":
		status = ihtml.ChatToolError
	case "cancelled":
		status = ihtml.ChatToolCancelled
	case "unknown":
		status = ihtml.ChatToolUnknown
	default:
		return
	}
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	defer func() { _ = recover() }()
	if s.taskProgress == nil {
		s.taskProgress = map[string]taskProgressDisplayState{}
	}
	key := info.ID
	if previous, exists := s.taskProgress[key]; exists && (previous.goal != info.GoalID || !taskProgressCanAdvance(previous.state, previous.progress, state, &snapshot)) {
		return
	}
	// 有界保留终态防迟到通知；活跃调用数量仍由原共享预算约束。
	if _, exists := s.taskProgress[key]; !exists && len(s.taskProgress) >= 4096 {
		oldest := ""
		var at time.Time
		for id, row := range s.taskProgress {
			if taskProgressTerminal(row.state) && (oldest == "" || row.at.Before(at)) {
				oldest, at = id, row.at
			}
		}
		if oldest == "" {
			return
		}
		delete(s.taskProgress, oldest)
	}
	var provider any
	if s.backend != nil {
		provider = s.backend.cfg.Engine
	}
	title := agent.ResolveToolMetadata(agent.Tool{Name: info.Tool, Title: info.Title}, provider).Title
	now := time.Now().UTC()
	event := ihtml.ChatStreamEvent{Type: ihtml.ChatStreamTaskProgress, FlowID: info.GoalID, TaskID: info.ID, TaskState: state, ToolID: "task-progress/" + info.ID, ToolName: info.Tool, ToolTitle: title, ToolStatus: status, ToolUpdatedAt: &now, StatusUpdatedAt: &snapshot.UpdatedAt, Progress: &snapshot}
	if err := s.persistToolHistory(event); err != nil {
		return
	}
	s.taskProgress[key] = taskProgressDisplayState{state: state, goal: info.GoalID, progress: &snapshot, at: now}
	if s.ctx.Err() == nil && s.sctx.Emit != nil {
		_ = s.sctx.Emit(event)
	}
}

// TaskProgressHistory 从原任务账本投影公开进度；recordUpdatedAt 必须为原记录时间，读取历史不得刷新它。
// 只交付显示字段，不含原参数、结果正文、内部错误或任何执行授权。
func TaskProgressHistory(info agent.TaskInfo, recordUpdatedAt time.Time) (ToolHistoryRecord, bool) {
	if recordUpdatedAt.IsZero() || !info.Background || info.ID == "" || info.GoalID == "" || info.Progress == nil {
		return ToolHistoryRecord{}, false
	}
	snapshot, ok := progress.Normalize(*info.Progress)
	if !ok || snapshot.UpdatedAt.IsZero() {
		return ToolHistoryRecord{}, false
	}
	status := ""
	switch info.State {
	case "queued", "running", "cancelling":
		status = ihtml.ChatToolRunning
	case "completed":
		status = ihtml.ChatToolAccepted
	case "failed":
		status = ihtml.ChatToolError
	case "cancelled":
		status = ihtml.ChatToolCancelled
	case "unknown":
		status = ihtml.ChatToolUnknown
	default:
		return ToolHistoryRecord{}, false
	}
	event := ihtml.ChatStreamEvent{Type: ihtml.ChatStreamTaskProgress, FlowID: info.GoalID, TaskID: info.ID, TaskState: info.State, ToolID: "task-progress/" + info.ID, ToolName: info.Tool, ToolTitle: agent.PublicToolTitle(info.Tool, info.Title), ToolStatus: status, ToolUpdatedAt: &recordUpdatedAt, Progress: &snapshot}
	return ToolHistoryEvent(event)
}

// RestoreTaskProgressHistory 用同一任务的权威快照核对已有进度行，不重放、不挪动原目标和消息锚点。
// 最终回执优先于较晚到达的旧展示事件，因此不按投递时间拒绝已经落库的明确终态。
func RestoreTaskProgressHistory(record ToolHistoryRecord, info agent.TaskInfo, recordUpdatedAt time.Time) ToolHistoryRecord {
	if record.TaskID == "" || record.TaskID != info.ID || record.FlowID == "" || record.FlowID != info.GoalID {
		return record
	}
	snapshot, ok := TaskProgressHistory(info, recordUpdatedAt)
	if !ok || !taskProgressCanAdvance(record.TaskState, record.Progress, snapshot.TaskState, snapshot.Progress) {
		return record
	}
	record.TaskState, record.Status, record.Progress = snapshot.TaskState, snapshot.Status, snapshot.Progress
	record.UpdatedAt = snapshot.UpdatedAt
	return record
}
