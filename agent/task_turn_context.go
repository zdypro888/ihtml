package agent

import (
	"context"
	"crypto/rand"
	"time"
)

// BindContext 为新的明确指令冻结当前停止代次。存储期限独立于返回上下文，不能缩短整轮生命周期。
func (r *TaskRuntime) BindContext(ctx context.Context, scope string) (context.Context, error) {
	if ctx == nil || scope == "" || len(scope) > 256 || r.cfg.Scope != "" && r.cfg.Scope != scope {
		return nil, ErrTaskNotFound
	}
	r.mu.Lock()
	r.seenScopes[scope] = true
	r.mu.Unlock()
	query, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	row, err := r.editControl(query, r.scopeControlKey(scope), func(row *TaskControlState) error {
		if row.Epoch == "" {
			row.Epoch = rand.Text()
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return context.WithValue(ctx, taskEpochKey{}, taskEpochContext{r.scopeControlKey(scope), row.Epoch}), nil
}

// UseContext 保留已经受理的代次并验证它仍有效；只有未绑定的可信调用才取得当前代次。
// 旧排队消息或后台续办不能通过出队时重新绑定，洗成停止之后的新指令。
func (r *TaskRuntime) UseContext(ctx context.Context, scope string) (context.Context, error) {
	if ctx == nil || scope == "" || len(scope) > 256 || r.cfg.Scope != "" && r.cfg.Scope != scope {
		return nil, ErrTaskNotFound
	}
	binding, ok := ctx.Value(taskEpochKey{}).(taskEpochContext)
	if !ok || binding.Key != r.scopeControlKey(scope) {
		return r.BindContext(ctx, scope)
	}
	query, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	if err := r.checkEpoch(query, scope, binding.Epoch, false); err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.seenScopes[scope] = true
	r.mu.Unlock()
	return ctx, nil
}

// WatchContext 将整轮推理绑定到停止代次，覆盖尚未调用工具的规划和排队后的执行。
// 观察只查询控制记录，不调用模型；存储不可确认时停止旧消费者，调用方须在收尾调用返回的 release。
func (r *TaskRuntime) WatchContext(ctx context.Context, scope string) (context.Context, func(), error) {
	bound, err := r.UseContext(ctx, scope)
	if err != nil {
		return nil, nil, err
	}
	binding := bound.Value(taskEpochKey{}).(taskEpochContext)
	watched, cancel := context.WithCancelCause(bound)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(r.cfg.PollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-watched.Done():
				return
			case <-r.ctx.Done():
				cancel(context.Cause(r.ctx))
				return
			case <-ticker.C:
				query, stop := context.WithTimeout(watched, 5*time.Second)
				err := r.checkEpoch(query, scope, binding.Epoch, false)
				stop()
				if err != nil {
					cancel(err)
					return
				}
			}
		}
	}()
	return watched, func() { cancel(context.Canceled); <-done }, nil
}
