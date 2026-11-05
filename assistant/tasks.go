package assistant

import (
	"context"
	"encoding/json"

	"github.com/zdypro888/ihtml"
)

// scheduleConfirmedTask 只沿模型原先选择的后台模式调度已批准的确切调用。
// 验签、消费 nonce、实时权限、业务执行、持久回执和续办仍全部复用原确认执行路径。
func (s *session) scheduleConfirmedTask(ctx context.Context, event *ihtml.ChatClientEvent, rejected bool) bool {
	if rejected || s.tasks == nil || event.ToolCall == nil || ctx.Err() != nil {
		return false
	}
	s.pendingMu.Lock()
	item := s.pendingTools[event.ToolCall.ConfirmationNonce]
	s.pendingMu.Unlock()
	if item == nil || !item.background {
		return false
	}
	copied := *event
	card := *event.ToolCall
	copied.ToolCall = &card
	s.mu.Lock()
	s.asyncConfirmations++
	s.mu.Unlock()
	finish := func() {
		s.mu.Lock()
		s.asyncConfirmations--
		s.mu.Unlock()
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
	// 原提案的可信上下文保留父调用句柄及 invocation；会话关闭、显式停止和执行期限由执行器处理。
	parent := context.WithValue(item.invocation, batchIntermediateContextKey{}, ctx.Value(batchIntermediateContextKey{}))
	tool := item.tool
	tool.Handler = func(callCtx context.Context, _ json.RawMessage) (string, error) {
		return s.runConfirmedTool(callCtx, func() {}, &copied, false)
	}
	info, err := s.tasks.Start(parent, item.taskScope, "confirmed/"+item.invocationScope, card.ConfirmationNonce, tool, item.args)
	if err != nil {
		finish()
		// 资源拒绝发生在验签消费与业务执行前，恢复原卡可重试状态，不改成同步执行。
		s.runConfirmedTool(ctx, func() {}, &copied, true)
	} else {
		done, doneErr := s.tasks.Done(item.taskScope, info.ID, map[string]bool{tool.Name: true})
		if doneErr != nil {
			finish()
		} else {
			s.confirmationTasks.Add(1)
			go func() { defer s.confirmationTasks.Done(); <-done; finish() }()
		}
	}
	return true
}
