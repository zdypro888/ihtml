package agent

import (
	"context"
	"errors"
	"time"
)

// TaskScopeStatus 是当前运行时可停止范围的账本快照，不返回参数、结果或外部作用域标识。
type TaskScopeStatus struct {
	ActiveTasks int
	ActiveWaits int
	CheckedAt   time.Time
}

// ScopeStatus 只读检查与 CancelAll 相同的可信作用域，不创建代次或恢复执行。
// 计数只包含当前代次；旧任务即使尚未完成取消收尾，也不能恢复已停止的操作入口。
func (r *TaskRuntime) ScopeStatus(ctx context.Context) (TaskScopeStatus, error) {
	r.mu.Lock()
	scopes := []string{}
	if r.cfg.Scope != "" {
		scopes = append(scopes, r.cfg.Scope)
	} else {
		for scope := range r.seenScopes {
			scopes = append(scopes, scope)
		}
	}
	r.mu.Unlock()
	status := TaskScopeStatus{CheckedAt: time.Now().UTC()}
	for _, scope := range scopes {
		for {
			if err := ctx.Err(); err != nil {
				return TaskScopeStatus{}, err
			}
			control, err := r.cfg.ControlStore.LoadControl(ctx, r.scopeControlKey(scope))
			if errors.Is(err, ErrTaskNotFound) {
				break
			}
			if err != nil {
				return TaskScopeStatus{}, WrapTaskStorage(err)
			}
			if control.Epoch == "" {
				break
			}
			var tasks, waits int
			after := ""
			for {
				rows, err := r.cfg.Store.ListTasks(ctx, scope, TaskScan{After: after, Limit: 128, ActiveOnly: true})
				if err != nil {
					return TaskScopeStatus{}, WrapTaskStorage(err)
				}
				for _, row := range rows {
					if row.Scope == scope && row.Epoch == control.Epoch && !taskTerminal(row.Info.State) && !row.CancelRequested {
						tasks++
					}
				}
				if len(rows) < 128 {
					break
				}
				after = rows[len(rows)-1].ID
			}
			after = ""
			for {
				rows, err := r.cfg.Store.ListWaits(ctx, scope, TaskScan{After: after, Limit: 128, ActiveOnly: true})
				if err != nil {
					return TaskScopeStatus{}, WrapTaskStorage(err)
				}
				for _, row := range rows {
					if row.Scope == scope && row.Epoch == control.Epoch && (row.State == "waiting" || row.State == "delivering") {
						waits++
					}
				}
				if len(rows) < 128 {
					break
				}
				after = rows[len(rows)-1].ID
			}
			latest, err := r.cfg.ControlStore.LoadControl(ctx, r.scopeControlKey(scope))
			if err != nil {
				return TaskScopeStatus{}, WrapTaskStorage(err)
			}
			// 扫描过程中遇到停止屏障，只重新读取计数，不重放任务或更改授权。
			if latest.Epoch != control.Epoch {
				continue
			}
			status.ActiveTasks += tasks
			status.ActiveWaits += waits
			break
		}
	}
	return status, nil
}
