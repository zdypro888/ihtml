package agent

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"time"
)

// TaskGoalLimits 由宿主限定一个明确指令及其所有恢复轮次的累计消耗；零额度表示不限制该维度。
// 金额采用宿主统一币种的微单位；仅可信供应商报价可作为费用上界，不能用字符估算费用。
type TaskGoalLimits struct {
	ModelCalls   int64         `json:"model_calls" yaml:"model_calls"`
	ToolCalls    int64         `json:"tool_calls" yaml:"tool_calls"`
	InputTokens  int64         `json:"input_tokens" yaml:"input_tokens"`
	OutputTokens int64         `json:"output_tokens" yaml:"output_tokens"`
	CostMicros   int64         `json:"cost_micros" yaml:"cost_micros"`
	Duration     time.Duration `json:"duration" yaml:"duration"`
}

// TaskGoalCharge 是一次调用的保守预留；失联/缺失 usage 不退还，以免突破预算。
type TaskGoalCharge struct {
	ModelCalls   int64 `json:"model_calls"`
	ToolCalls    int64 `json:"tool_calls"`
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	CostMicros   int64 `json:"cost_micros"`
	// Quoted 表示宿主确认 tokens/费用为上界，不是模型自报或粗略估计。
	Quoted bool `json:"-"`
}

// TaskGoalBinding 在可信上下文及等待记录传递，AI 创建 context 文档不能重置它。
type TaskGoalBinding struct {
	ID       string    `json:"id" bson:"id"`
	Scope    string    `json:"scope" bson:"scope"`
	Deadline time.Time `json:"deadline" bson:"deadline"`
}
type taskGoalKey struct{}

// TaskGoalView 返回目标额度与保守预留，不能作为新的执行授权。
type TaskGoalView struct {
	Binding  TaskGoalBinding `json:"goal"`
	Limits   TaskGoalLimits  `json:"limits"`
	Reserved TaskGoalCharge  `json:"reserved"`
}

func (r *TaskRuntime) newGoal(ctx context.Context, scope, seed string) (context.Context, error) {
	if old, ok := ctx.Value(taskGoalKey{}).(TaskGoalBinding); ok && old.Scope == r.scopeControlKey(scope) {
		if !old.Deadline.After(time.Now()) {
			return nil, &TaskBudgetError{Kind: "goal_deadline"}
		}
		return ctx, nil
	}
	duration := r.cfg.GoalLimits.Duration
	if duration == 0 {
		duration = 24 * time.Hour
	}
	b := TaskGoalBinding{ID: seed, Scope: r.scopeControlKey(scope), Deadline: time.Now().UTC().Add(duration).Truncate(time.Millisecond)}
	if b.ID == "" {
		b.ID = rand.Text()
	}
	query, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	_, err := r.editControl(query, "goal/"+taskHash([]string{r.cfg.ControlNamespace, b.Scope, b.ID}), func(row *TaskControlState) error {
		v := TaskGoalView{Binding: b, Limits: r.cfg.GoalLimits}
		if len(row.Payload) > 0 {
			if json.Unmarshal(row.Payload, &v) != nil || v.Binding.ID != b.ID || v.Binding.Scope != b.Scope {
				return ErrTaskConflict
			}
			b = v.Binding
		}
		if !b.Deadline.After(time.Now()) {
			return &TaskBudgetError{Kind: "goal_deadline"}
		}
		row.Payload, _ = json.Marshal(v)
		row.ExpiresAt = b.Deadline.Add(r.cfg.LedgerRetention)
		return nil
	})
	return context.WithValue(ctx, taskGoalKey{}, b), err
}
func goalBinding(ctx context.Context) TaskGoalBinding {
	b, _ := ctx.Value(taskGoalKey{}).(TaskGoalBinding)
	return b
}
func (r *TaskRuntime) editGoal(ctx context.Context, change func(*TaskGoalView) error) (TaskGoalView, error) {
	b := goalBinding(ctx)
	if b.ID == "" || b.Scope == "" {
		return TaskGoalView{}, ErrTaskNotFound
	}
	if !b.Deadline.After(time.Now()) {
		return TaskGoalView{}, &TaskBudgetError{Kind: "goal_deadline"}
	}
	ctx, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	var view TaskGoalView
	_, err := r.editControl(ctx, "goal/"+taskHash([]string{r.cfg.ControlNamespace, b.Scope, b.ID}), func(row *TaskControlState) error {
		// 已绑定账户缺失不能补成新额度。
		if row.Revision == 0 || len(row.Payload) == 0 {
			return ErrTaskNotFound
		}
		view = TaskGoalView{Binding: b, Limits: r.cfg.GoalLimits}
		if len(row.Payload) > 0 {
			if json.Unmarshal(row.Payload, &view) != nil {
				return ErrTaskConflict
			}
		}
		if view.Binding.ID != b.ID || view.Binding.Scope != b.Scope || !view.Binding.Deadline.Equal(b.Deadline) {
			return ErrTaskConflict
		}
		if err := change(&view); err != nil {
			return err
		}
		row.Payload, _ = json.Marshal(view)
		row.ExpiresAt = b.Deadline.Add(r.cfg.LedgerRetention)
		return nil
	})
	return view, err
}

// GoalBudget 查询同一可信目标的累计保守预留，恢复不会产生新的额度。
func (r *TaskRuntime) GoalBudget(ctx context.Context) (TaskGoalView, error) {
	return r.editGoal(ctx, func(*TaskGoalView) error { return nil })
}

// ChargeGoal 在实际调用前原子扣减；存储失败或无可靠报价时拒绝受约束的调用。
// 预留不自动退款：超时、供应商内部重试和进程退出可能已经产生费用。
func (r *TaskRuntime) ChargeGoal(ctx context.Context, amount TaskGoalCharge) error {
	if amount.ModelCalls < 0 || amount.ToolCalls < 0 || amount.InputTokens < 0 || amount.OutputTokens < 0 || amount.CostMicros < 0 {
		return ErrTaskConflict
	}
	if epoch, ok := ctx.Value(taskEpochKey{}).(taskEpochContext); ok {
		query, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		if _, err := r.editControl(query, epoch.Key, func(row *TaskControlState) error {
			if row.Epoch != epoch.Epoch {
				return ErrTaskStopped
			}
			return nil
		}); err != nil {
			return err
		}
	}
	_, err := r.editGoal(ctx, func(v *TaskGoalView) error {
		limits := []int64{v.Limits.ModelCalls, v.Limits.ToolCalls, v.Limits.InputTokens, v.Limits.OutputTokens, v.Limits.CostMicros}
		used := []*int64{&v.Reserved.ModelCalls, &v.Reserved.ToolCalls, &v.Reserved.InputTokens, &v.Reserved.OutputTokens, &v.Reserved.CostMicros}
		add := []int64{amount.ModelCalls, amount.ToolCalls, amount.InputTokens, amount.OutputTokens, amount.CostMicros}
		if amount.ModelCalls > 0 && !amount.Quoted && (limits[2] > 0 || limits[3] > 0 || limits[4] > 0) {
			return &TaskBudgetError{Kind: "goal_quote_required"}
		}
		for i, n := range add {
			if n > 1<<60 || *used[i] > 1<<60-n || limits[i] > 0 && n > limits[i]-*used[i] {
				return &TaskBudgetError{Kind: "goal_total"}
			}
		}
		for i, n := range add {
			*used[i] += n
		}
		return nil
	})
	return err
}

// ValidateTaskGoalLimits 拒绝负额度及无界保留配置，生产与内存宿主使用同一约束。
func ValidateTaskGoalLimits(l TaskGoalLimits) error {
	for _, n := range []int64{l.ModelCalls, l.ToolCalls, l.InputTokens, l.OutputTokens, l.CostMicros} {
		if n < 0 || n > 1<<60 {
			return errors.New("agent: invalid goal limits")
		}
	}
	if l.Duration < 0 || l.Duration > 30*24*time.Hour {
		return errors.New("agent: invalid goal duration")
	}
	return nil
}

// ContinueGoal 让显式批准/总结继承原目标账户，同时保留当前消息的停止代次与追踪。
// source 必须是宿主保存的可信上下文；不能接受模型传入预算 ID。
func (r *TaskRuntime) ContinueGoal(ctx context.Context, scope string, source context.Context) (context.Context, error) {
	if source == nil {
		return ctx, nil
	}
	b := goalBinding(source)
	if b.ID == "" {
		return ctx, nil
	} // 升级前的提案没有目标绑定，仍走当前可信接纳。
	if b.Scope != r.scopeControlKey(scope) || r.cfg.Scope != "" && r.cfg.Scope != scope {
		return nil, ErrTaskNotFound
	}
	bound := context.WithValue(ctx, taskGoalKey{}, b)
	if _, err := r.GoalBudget(bound); err != nil {
		return nil, err
	}
	return bound, nil
}

// UseTurnGoal 持久绑定可信 InvocationScope，原生 checkpoint 恢复及宿主重试仍使用原账户。
// 新的用户请求须由宿主提供新的 InvocationScope；AI 不能通过工具调用指定它。
func (r *TaskRuntime) UseTurnGoal(ctx context.Context, scope, turn string) (context.Context, error) {
	if turn == "" || len(turn) > 1024 {
		return nil, ErrTaskConflict
	}
	bound, err := r.UseContext(ctx, scope)
	if err != nil {
		return nil, err
	}
	b := goalBinding(bound)
	query, stop := context.WithTimeout(bound, 5*time.Second)
	defer stop()
	_, err = r.editControl(query, "goal-turn/"+taskHash([]string{r.cfg.ControlNamespace, scope, turn}), func(row *TaskControlState) error {
		if len(row.Payload) > 0 {
			if json.Unmarshal(row.Payload, &b) != nil || b.Scope != r.scopeControlKey(scope) {
				return ErrTaskConflict
			}
		}
		if !b.Deadline.After(time.Now()) {
			return &TaskBudgetError{Kind: "goal_deadline"}
		}
		row.Payload, _ = json.Marshal(b)
		row.ExpiresAt = b.Deadline.Add(r.cfg.LedgerRetention)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return context.WithValue(bound, taskGoalKey{}, b), nil
}
