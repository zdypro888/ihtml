package assistant

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"sync/atomic"

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
	if item == nil {
		return false
	}
	if !item.background {
		// 同步批准也记入同一账本，但保持原队列顺序；持久化不改变模型选择的执行方式。
		copied := *event
		card := *event.ToolCall
		copied.ToolCall = &card
		tool := item.tool
		// Call 可在运行时停止后先于 Handler 退出；启动标记跨 goroutine 读写必须同步。
		var started atomic.Bool
		tool.Handler = func(callCtx context.Context, _ json.RawMessage) (string, error) {
			started.Store(true)
			return s.runConfirmedTool(callCtx, func() {}, &copied, false)
		}
		var parent context.Context = sessionParentValues{Context: ctx, parent: item.invocation}
		parent, bindErr := s.tasks.ContinueGoal(parent, item.taskScope, item.invocation)
		if bindErr == nil {
			parent, bindErr = s.tasks.UseContext(parent, item.taskScope)
		}
		if bindErr != nil {
			s.runConfirmedTool(ctx, func() {}, &copied, true)
			return true
		}
		_, err := s.tasks.Call(parent, item.taskScope, "confirmed/"+item.invocationScope, card.ConfirmationNonce+"/"+rand.Text(), tool, item.args)
		if err != nil && !started.Load() {
			s.runConfirmedTool(ctx, func() {}, &copied, true)
		}
		return true
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
	// 保留批准事件的代次和追踪值，后台生命周期不跟随短暂的确认调度轮次。
	var parent context.Context = sessionParentValues{Context: context.WithoutCancel(ctx), parent: item.invocation}
	parent, bindErr := s.tasks.ContinueGoal(parent, item.taskScope, item.invocation)
	if bindErr == nil {
		parent, bindErr = s.tasks.UseContext(parent, item.taskScope)
	}
	if bindErr != nil {
		finish()
		s.runConfirmedTool(ctx, func() {}, &copied, true)
		return true
	}
	tool := item.tool
	tool.Handler = func(callCtx context.Context, _ json.RawMessage) (string, error) {
		return s.runConfirmedTool(callCtx, func() {}, &copied, false)
	}
	// 每次显式批准是一次新的调度尝试，业务授权仍由同一个一次性 nonce 决定。
	// 不能复用已取消队列项的句柄，否则再次批准只会读回旧 cancelled 而永远不执行。
	info, err := s.tasks.Start(parent, item.taskScope, "confirmed/"+item.invocationScope, card.ConfirmationNonce+"/"+rand.Text(), tool, item.args)
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
			go func() {
				defer s.confirmationTasks.Done()
				<-done
				// 未进入 Handler 的取消/排队超时不会消费 nonce；释放预留，允许用户重新批准原卡。
				// 已消费的卡已从 pendingTools 移除，此处不改变原防重放边界。
				s.releaseQueued(card)
				finish()
			}()
		}
	}
	return true
}
