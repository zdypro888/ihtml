package agent

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"time"
)

// ErrTaskStopped 表示调用所属代次已停止；重新执行必须来自新的明确轮次。
var ErrTaskStopped = errors.New("agent: task scope stopped")

// MaxTaskBudgetLeases 为共享协调文档设置总容量上限，避免不相交的资源组合无限增长。
const MaxTaskBudgetLeases = 5000

// TaskBudgetLimit 由可信宿主定义共享资源额度，模型只能查询，不能扩大。
type TaskBudgetLimit struct {
	Key                                  string
	MaxActive, MaxConcurrent, MaxWaiting int
}

// TaskBudgetLease 是共享池中的有期限预留；同一池的全部资源在一次 CAS 中接纳。
type TaskBudgetLease struct {
	ID        string    `bson:"id"`
	Scope     string    `bson:"scope"`
	Epoch     string    `bson:"epoch"`
	Owner     string    `bson:"owner"`
	Kind      string    `bson:"kind"`
	Resources []string  `bson:"resources"`
	ExpiresAt time.Time `bson:"expiresAt"`
	Running   bool      `bson:"running"`
}

// TaskControlState 用同一 CAS 协议保存独立的停止代次文档和共享预算池文档。
// 停止代次不可重置为旧值；预算租约到期可以回收，不能把失联执行者重新接纳为原执行者。
type TaskControlState struct {
	Key       string            `bson:"_id"`
	Revision  int64             `bson:"revision"`
	Epoch     string            `bson:"epoch,omitempty"`
	Leases    []TaskBudgetLease `bson:"leases,omitempty"`
	UpdatedAt time.Time         `bson:"updatedAt"`
}

// TaskControlStore 的 expected=0 表示原子创建；其他值必须与当前 Revision 匹配。
// 存储必须跨会话/实例共享，不能为预算池隐式加会话前缀。
type TaskControlStore interface {
	LoadControl(context.Context, string) (TaskControlState, error)
	CompareControl(context.Context, TaskControlState, int64) (bool, error)
}

// MemoryTaskControlStore 为测试及单进程宿主提供共享协调，生产应注入持久适配。
type MemoryTaskControlStore struct {
	mu   sync.Mutex
	rows map[string]TaskControlState
}

func NewMemoryTaskControlStore() *MemoryTaskControlStore {
	return &MemoryTaskControlStore{rows: map[string]TaskControlState{}}
}
func cloneControl(s TaskControlState) TaskControlState {
	s.Leases = append([]TaskBudgetLease(nil), s.Leases...)
	for i := range s.Leases {
		s.Leases[i].Resources = append([]string(nil), s.Leases[i].Resources...)
	}
	return s
}
func (s *MemoryTaskControlStore) LoadControl(ctx context.Context, key string) (TaskControlState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return TaskControlState{}, err
	}
	row, ok := s.rows[key]
	if !ok {
		return row, ErrTaskNotFound
	}
	return cloneControl(row), nil
}
func (s *MemoryTaskControlStore) CompareControl(ctx context.Context, row TaskControlState, expected int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	old, ok := s.rows[row.Key]
	if ok && old.Revision != expected || !ok && expected != 0 {
		return false, nil
	}
	if row.Revision != expected+1 {
		return false, ErrTaskConflict
	}
	s.rows[row.Key] = cloneControl(row)
	return true, nil
}

type taskEpochKey struct{}
type taskEpochContext struct{ Key, Epoch string }

func (r *TaskRuntime) scopeControlKey(scope string) string {
	return "scope/" + taskHash([]string{r.cfg.ControlNamespace, scope})
}
func (r *TaskRuntime) poolControlKey() string { return "budget/" + taskHash(r.cfg.BudgetPool) }
func (r *TaskRuntime) editControl(ctx context.Context, key string, change func(*TaskControlState) error) (TaskControlState, error) {
	for {
		if err := ctx.Err(); err != nil {
			return TaskControlState{}, err
		}
		row, err := r.cfg.ControlStore.LoadControl(ctx, key)
		if errors.Is(err, ErrTaskNotFound) {
			row = TaskControlState{Key: key}
		} else if err != nil {
			return row, err
		}
		revision := row.Revision
		if err = change(&row); err != nil {
			return row, err
		}
		row.Revision++
		row.UpdatedAt = time.Now().UTC()
		ok, err := r.cfg.ControlStore.CompareControl(ctx, row, revision)
		if err != nil {
			return row, err
		}
		if ok {
			return row, nil
		}
		// 冲突只重试协调元数据，不重放任何 Handler。
		select {
		case <-ctx.Done():
			return row, ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
}
func (r *TaskRuntime) scopeEpoch(ctx context.Context, scope string) (string, error) {
	key := r.scopeControlKey(scope)
	if value, ok := ctx.Value(taskEpochKey{}).(taskEpochContext); ok && value.Key == key {
		return value.Epoch, nil
	}
	row, err := r.editControl(ctx, key, func(row *TaskControlState) error {
		if row.Epoch == "" {
			row.Epoch = rand.Text()
		}
		return nil
	})
	return row.Epoch, err
}

// BindContext 在规划开始或新的明确批准时冻结停止代次；后续后台/嵌套调用继承它。
func (r *TaskRuntime) BindContext(ctx context.Context, scope string) (context.Context, error) {
	if scope == "" || len(scope) > 256 || r.cfg.Scope != "" && r.cfg.Scope != scope {
		return nil, ErrTaskNotFound
	}
	r.mu.Lock()
	r.seenScopes[scope] = true
	r.mu.Unlock()
	// 新受管轮次显式取得当前代次，不沿用原提案/旧轮次的代次。
	row, err := r.editControl(ctx, r.scopeControlKey(scope), func(row *TaskControlState) error {
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
func (r *TaskRuntime) checkEpoch(ctx context.Context, scope, epoch string, commit bool) error {
	if epoch == "" {
		return ErrTaskStopped // 无代次历史只能查询或由用户明确重新继续，不能自动取得新执行资格。
	}
	if commit {
		_, err := r.editControl(ctx, r.scopeControlKey(scope), func(row *TaskControlState) error {
			if row.Epoch != epoch {
				return ErrTaskStopped
			}
			return nil
		})
		return err
	}
	row, err := r.cfg.ControlStore.LoadControl(ctx, r.scopeControlKey(scope))
	if err != nil {
		return err
	}
	if row.Epoch != epoch {
		return ErrTaskStopped
	}
	return nil
}
func (r *TaskRuntime) stopScope(ctx context.Context, scope string) error {
	_, err := r.editControl(ctx, r.scopeControlKey(scope), func(row *TaskControlState) error { row.Epoch = rand.Text(); return nil })
	return err
}

// TaskBudgetError 明确表示尚未接纳，不表示工具或原业务失败；AI 可选择等待或调整并发。
type TaskBudgetError struct {
	Resource     string `json:"resource"`
	Kind         string `json:"kind"`
	RetryAfterMS int64  `json:"retry_after_ms"`
}

func (e *TaskBudgetError) Error() string {
	return "agent: shared task budget exhausted (" + e.Kind + ")"
}

// TaskBudgetView 提供当前限制和剩余额度快照；查询不构成资源预留。
type TaskBudgetView struct {
	Resource            string `json:"resource"`
	MaxActive           int    `json:"max_active"`
	ActiveRemaining     int    `json:"active_remaining"`
	MaxConcurrent       int    `json:"max_concurrent"`
	ConcurrentRemaining int    `json:"concurrent_remaining"`
	MaxWaiting          int    `json:"max_waiting"`
	WaitingRemaining    int    `json:"waiting_remaining"`
}

func liveBudget(row *TaskControlState) {
	now := time.Now()
	out := row.Leases[:0]
	for _, lease := range row.Leases {
		if lease.ExpiresAt.After(now) {
			out = append(out, lease)
		}
	}
	row.Leases = out
}
func hasResource(lease TaskBudgetLease, key string) bool {
	for _, resource := range lease.Resources {
		if resource == key {
			return true
		}
	}
	return false
}
func budgetCounts(row TaskControlState, key string) (active, running, waiting int) {
	for _, lease := range row.Leases {
		if !hasResource(lease, key) {
			continue
		}
		if lease.Kind == "wait" {
			waiting++
		} else {
			active++
			if lease.Running {
				running++
			}
		}
	}
	return
}
func (r *TaskRuntime) acquireBudget(ctx context.Context, scope, epoch, id, kind string, until time.Time) error {
	if len(r.cfg.Budgets) == 0 {
		return nil
	}
	_, err := r.editControl(ctx, r.poolControlKey(), func(row *TaskControlState) error {
		liveBudget(row)
		for _, lease := range row.Leases {
			if lease.ID == id {
				if lease.Owner == r.owner && lease.Epoch == epoch {
					return nil
				}
				return ErrTaskConflict
			}
		}
		if len(row.Leases) >= MaxTaskBudgetLeases {
			return &TaskBudgetError{Resource: "pool", Kind: "pool_capacity", RetryAfterMS: r.cfg.PollInterval.Milliseconds()}
		}
		resources := make([]string, 0, len(r.cfg.Budgets))
		for _, limit := range r.cfg.Budgets {
			a, _, w := budgetCounts(*row, limit.Key)
			if kind == "wait" && w >= limit.MaxWaiting || kind != "wait" && a >= limit.MaxActive {
				return &TaskBudgetError{Resource: limit.Key, Kind: kind, RetryAfterMS: r.cfg.PollInterval.Milliseconds()}
			}
			resources = append(resources, limit.Key)
		}
		row.Leases = append(row.Leases, TaskBudgetLease{ID: id, Scope: scope, Epoch: epoch, Owner: r.owner, Kind: kind, Resources: resources, ExpiresAt: until})
		return nil
	})
	return err
}
func (r *TaskRuntime) updateBudget(ctx context.Context, id, action string, until time.Time) error {
	if len(r.cfg.Budgets) == 0 && r.cfg.BudgetPool == "" {
		return nil
	}
	_, err := r.editControl(ctx, r.poolControlKey(), func(row *TaskControlState) error {
		liveBudget(row)
		found := -1
		for i, lease := range row.Leases {
			if lease.ID == id {
				found = i
				break
			}
		}
		if action == "release" {
			if found >= 0 {
				row.Leases = append(row.Leases[:found], row.Leases[found+1:]...)
			}
			return nil
		}
		if found < 0 {
			return ErrTaskConflict
		}
		lease := &row.Leases[found]
		if lease.Owner != r.owner {
			return ErrTaskConflict
		}
		if action == "run" && !lease.Running {
			for _, limit := range r.cfg.Budgets {
				_, count, _ := budgetCounts(*row, limit.Key)
				if count >= limit.MaxConcurrent {
					return &TaskBudgetError{Resource: limit.Key, Kind: "concurrent", RetryAfterMS: r.cfg.PollInterval.Milliseconds()}
				}
			}
			lease.Running = true
		}
		lease.ExpiresAt = until
		return nil
	})
	return err
}
func (r *TaskRuntime) releaseBudget(id string) {
	ctx, stop := r.operation()
	defer stop()
	_ = r.updateBudget(ctx, id, "release", time.Time{})
}

// Budget 查询可信宿主绑定的共享额度，不向模型开放其他租户的资源选择参数。
func (r *TaskRuntime) Budget(ctx context.Context) ([]TaskBudgetView, error) {
	if len(r.cfg.Budgets) == 0 {
		return []TaskBudgetView{}, nil
	}
	row, err := r.cfg.ControlStore.LoadControl(ctx, r.poolControlKey())
	if err != nil && !errors.Is(err, ErrTaskNotFound) {
		return nil, err
	}
	liveBudget(&row)
	out := make([]TaskBudgetView, 0, len(r.cfg.Budgets))
	for _, limit := range r.cfg.Budgets {
		a, c, w := budgetCounts(row, limit.Key)
		out = append(out, TaskBudgetView{limit.Key, limit.MaxActive, max(0, limit.MaxActive-a), limit.MaxConcurrent, max(0, limit.MaxConcurrent-c), limit.MaxWaiting, max(0, limit.MaxWaiting-w)})
	}
	return out, nil
}

func deadlineFromContext(ctx context.Context) time.Time {
	deadline, _ := ctx.Deadline()
	return deadline
}

// BindWaitContext 恢复的是原等待代次，不是新的用户指令；停止后的旧事件不能借新代次重新取得执行资格。
func (r *TaskRuntime) BindWaitContext(ctx context.Context, scope, id string) (context.Context, error) {
	if scope == "" || r.cfg.Scope != "" && r.cfg.Scope != scope {
		return nil, ErrTaskNotFound
	}
	r.mu.Lock()
	r.seenScopes[scope] = true
	r.mu.Unlock()
	wait, err := r.cfg.Store.GetWait(ctx, scope, id)
	if err != nil {
		return nil, err
	}
	epoch := wait.Epoch
	if epoch == "" {
		return nil, ErrTaskStopped
	}
	if err = r.checkEpoch(ctx, scope, epoch, false); err != nil {
		return nil, err
	}
	return context.WithValue(ctx, taskEpochKey{}, taskEpochContext{r.scopeControlKey(scope), epoch}), nil
}

// reconcileBudget 修复“终态已提交、释放共享预留时断线”的窗口；不根据缺失记录释放尚在接纳的预留。
func (r *TaskRuntime) reconcileBudget(ctx context.Context) error {
	if r.cfg.BudgetPool == "" {
		return nil
	}
	row, err := r.cfg.ControlStore.LoadControl(ctx, r.poolControlKey())
	if errors.Is(err, ErrTaskNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	r.mu.Lock()
	scopes := map[string]bool{}
	if r.cfg.Scope != "" {
		scopes[r.cfg.Scope] = true
	} else {
		for scope := range r.seenScopes {
			scopes[scope] = true
		}
	}
	r.mu.Unlock()
	for _, lease := range row.Leases {
		if !scopes[lease.Scope] {
			continue
		}
		terminal := false
		if lease.Kind == "wait" {
			wait, e := r.cfg.Store.GetWait(ctx, lease.Scope, lease.ID)
			if errors.Is(e, ErrTaskNotFound) {
				continue
			}
			if e != nil {
				return e
			}
			terminal = wait.State != "waiting" && wait.State != "delivering"
		} else {
			task, e := r.cfg.Store.GetTask(ctx, lease.Scope, lease.ID)
			if errors.Is(e, ErrTaskNotFound) {
				continue
			}
			if e != nil {
				return e
			}
			terminal = taskTerminal(task.Info.State)
		}
		if terminal {
			if err := r.updateBudget(ctx, lease.ID, "release", time.Time{}); err != nil {
				return err
			}
		}
	}
	return nil
}
