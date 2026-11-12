package agent

import "context"

// ExecutionEpoch 返回当前作用域的执行代次，供持久授权等受控能力绑定停止屏障。
func (r *TaskRuntime) ExecutionEpoch(ctx context.Context, scope string) (string, error) {
	return r.scopeEpoch(ctx, scope)
}

// CheckExecutionEpoch 查询当前权威代次；停止后的旧授权不能在新轮次取得新的执行资格。
func (r *TaskRuntime) CheckExecutionEpoch(ctx context.Context, scope, epoch string) error {
	return r.checkEpoch(ctx, scope, epoch, false)
}
