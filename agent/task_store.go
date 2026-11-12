package agent

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

var ErrTaskNotFound = errors.New("agent: task record unavailable")
var ErrTaskConflict = errors.New("agent: task record changed")

// ErrTaskWakeBudget 表示当前目标的自动恢复预算已用完，须公开报告而非继续注册循环。
var ErrTaskWakeBudget = errors.New("agent: task wake budget exhausted")

// TaskRecord 是持久调用账本；参数只存于宿主受控存储，不向浏览器或任务列表透传。
// Revision 与 Owner/LeaseUntil 形成 fencing，失去租约的执行器不能覆盖恢复后的状态。
type TaskRecord struct {
	StorageVersion  int       `bson:"storageVersion,omitempty" json:"storage_version,omitempty"`
	Epoch           string    `bson:"epoch,omitempty"`
	ID              string    `bson:"_id"`
	Scope           string    `bson:"scope"`
	Key             string    `bson:"key"`
	Digest          string    `bson:"digest"`
	Turn            string    `bson:"turn"`
	Args            []byte    `bson:"args,omitempty"`
	Invocation      string    `bson:"invocation,omitempty"`
	Effect          string    `bson:"effect"`
	Info            TaskInfo  `bson:"info"`
	Revision        int64     `bson:"revision"`
	Owner           string    `bson:"owner"`
	Started         bool      `bson:"started"`
	CancelRequested bool      `bson:"cancelRequested"`
	CreatedAt       time.Time `bson:"createdAt"`
	UpdatedAt       time.Time `bson:"updatedAt"`
	Deadline        time.Time `bson:"deadline"`
	LeaseUntil      time.Time `bson:"leaseUntil"`
	ResultExpiresAt time.Time `bson:"resultExpiresAt,omitempty"`
	ExpiresAt       time.Time `bson:"expiresAt,omitempty"`
}

// TaskWaitSpec 只保存 AI 选择的等待条件和工作上下文，不保存接口专用后续步骤。
type TaskWaitSpec struct {
	Contexts   []TaskContextVersion `json:"contexts,omitempty" bson:"contexts,omitempty"`
	Events     []TaskEventCondition `json:"events,omitempty" bson:"events,omitempty"`
	Timeout    time.Duration        `json:"-" bson:"timeout"`
	IDs        []string             `json:"ids" bson:"ids"`
	All        bool                 `json:"all" bson:"all"`
	Deadline   time.Time            `json:"deadline" bson:"deadline"`
	Checkpoint string               `json:"checkpoint" bson:"checkpoint"`
}

// TaskWaitRecord 保存条件、交付租约与处理状态；delivered 不表示原业务全部完成。
type TaskWaitRecord struct {
	// TraceParent/TraceState 只保存 W3C 追踪头，不保存 baggage 或业务上下文。
	TraceParent    string             `bson:"traceparent,omitempty" json:"-"`
	TraceState     string             `bson:"tracestate,omitempty" json:"-"`
	StorageVersion int                `bson:"storageVersion,omitempty" json:"storage_version,omitempty"`
	Goal           TaskGoalBinding    `bson:"goal,omitempty" json:"-"`
	ContextMatches []TaskContextMatch `bson:"contextMatches,omitempty" json:"context_matches,omitempty"`
	TraceID        string             `bson:"traceID,omitempty" json:"trace_id,omitempty"`
	EventMatches   []TaskEventMatch   `bson:"eventMatches,omitempty" json:"event_matches,omitempty"`
	Epoch          string             `bson:"epoch,omitempty" json:"-"`
	Attempts       int                `bson:"attempts" json:"-"`
	Depth          int                `bson:"depth" json:"-"`
	ID             string             `bson:"_id" json:"id"`
	Scope          string             `bson:"scope" json:"-"`
	Key            string             `bson:"key" json:"-"`
	Digest         string             `bson:"digest" json:"-"`
	Turn           string             `bson:"turn" json:"-"`
	Spec           TaskWaitSpec       `bson:"spec" json:"condition"`
	State          string             `bson:"state" json:"state"`
	Reason         string             `bson:"reason,omitempty" json:"reason,omitempty"`
	Revision       int64              `bson:"revision" json:"-"`
	Owner          string             `bson:"owner" json:"-"`
	LeaseUntil     time.Time          `bson:"leaseUntil" json:"-"`
	RetryAt        time.Time          `bson:"retryAt" json:"-"`
	CreatedAt      time.Time          `bson:"createdAt" json:"-"`
	ExpiresAt      time.Time          `bson:"expiresAt" json:"-"`
}

// TaskScan 提供稳定分页和活跃筛选，维护不扫描已经结束的全部历史。
type TaskScan struct {
	After      string
	Limit      int
	ActiveOnly bool
}

// TaskStore 的 Create 必须原子去重(scope,key)，Replace 必须 CAS；List 按 ID 排序分页。
// 空 scope 仅用于宿主绑定命名空间内的维护，不允许模型直接指定。
// Collect 只清理终态：先清结果/参数保留幂等记录，账本保留期之后才允许删除。
type TaskStore interface {
	CreateTask(context.Context, TaskRecord) (TaskRecord, bool, error)
	GetTask(context.Context, string, string) (TaskRecord, error)
	FindTask(context.Context, string, string) (TaskRecord, error)
	ReplaceTask(context.Context, TaskRecord, int64) (bool, error)
	ListTasks(context.Context, string, TaskScan) ([]TaskRecord, error)
	GetWait(context.Context, string, string) (TaskWaitRecord, error)
	FindWait(context.Context, string, string) (TaskWaitRecord, error)
	CreateWait(context.Context, TaskWaitRecord) (TaskWaitRecord, bool, error)
	ReplaceWait(context.Context, TaskWaitRecord, int64) (bool, error)
	ListWaits(context.Context, string, TaskScan) ([]TaskWaitRecord, error)
	Collect(context.Context, time.Time) error
}

// MemoryTaskStore 供不需要重启恢复的宿主和测试使用；生产宿主可接入同一 CAS 存储协议。
type MemoryTaskStore struct {
	observations *MemoryTaskObservationStore
	events       *MemoryTaskEventStore
	control      *MemoryTaskControlStore
	results      *MemoryTaskResultStore
	contexts     *MemoryTaskContextStore
	mu           sync.Mutex
	tasks        map[string]TaskRecord
	waits        map[string]TaskWaitRecord
}

// NewMemoryTaskStore 创建独立内存账本，不宣称支持进程重启恢复。
func NewMemoryTaskStore() *MemoryTaskStore {
	return &MemoryTaskStore{observations: NewMemoryTaskObservationStore(), control: NewMemoryTaskControlStore(), results: NewMemoryTaskResultStore(), contexts: NewMemoryTaskContextStore(), tasks: map[string]TaskRecord{}, waits: map[string]TaskWaitRecord{}}
}
func cloneTaskRecord(r TaskRecord) TaskRecord {
	r.Info.Progress = cloneTaskProgress(r.Info.Progress)
	if r.Info.ResolutionRef != nil {
		ref := *r.Info.ResolutionRef
		r.Info.ResolutionRef = &ref
	}
	r.Info.Failure = cloneTaskFailure(r.Info.Failure)
	r.Args = append([]byte(nil), r.Args...)
	r.Info.Input = append([]byte(nil), r.Info.Input...)
	if r.Info.ResultRef != nil {
		ref := *r.Info.ResultRef
		r.Info.ResultRef = &ref
	}
	return r
}
func cloneWaitRecord(r TaskWaitRecord) TaskWaitRecord {
	r.ContextMatches = append([]TaskContextMatch(nil), r.ContextMatches...)
	r.Spec.Contexts = append([]TaskContextVersion(nil), r.Spec.Contexts...)
	r.Spec.IDs = append([]string(nil), r.Spec.IDs...)
	r.Spec.Events = append([]TaskEventCondition(nil), r.Spec.Events...)
	for i := range r.Spec.Events {
		r.Spec.Events[i].Types = append([]string(nil), r.Spec.Events[i].Types...)
	}
	r.EventMatches = append([]TaskEventMatch(nil), r.EventMatches...)
	return r
}

// CreateTask 原子绑定调用键与参数指纹，重复受理不创建第二个执行者。
func (s *MemoryTaskStore) CreateTask(ctx context.Context, r TaskRecord) (TaskRecord, bool, error) {
	if err := TaskProtocolVersion(r.StorageVersion); err != nil {
		return TaskRecord{}, false, err
	}
	r.StorageVersion = TaskStorageVersion
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return TaskRecord{}, false, err
	}
	for _, old := range s.tasks {
		if old.Scope == r.Scope && old.Key == r.Key {
			return cloneTaskRecord(old), false, nil
		}
	}
	if _, ok := s.tasks[r.ID]; ok {
		return TaskRecord{}, false, ErrTaskConflict
	}
	s.tasks[r.ID] = cloneTaskRecord(r)
	return cloneTaskRecord(r), true, nil
}

// GetTask 核对作用域并返回副本，避免调用方绕过 CAS 修改记录。
func (s *MemoryTaskStore) GetTask(ctx context.Context, scope, id string) (TaskRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return TaskRecord{}, err
	}
	r, ok := s.tasks[id]
	if !ok || r.Scope != scope {
		return TaskRecord{}, ErrTaskNotFound
	}
	return cloneTaskRecord(r), nil
}

// FindTask 按稳定调用键查询原回执，不以重放代替恢复。
func (s *MemoryTaskStore) FindTask(ctx context.Context, scope, key string) (TaskRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return TaskRecord{}, err
	}
	for _, r := range s.tasks {
		if r.Scope == scope && r.Key == key {
			return cloneTaskRecord(r), nil
		}
	}
	return TaskRecord{}, ErrTaskNotFound
}

// ReplaceTask 同时核对版本与调用身份，拒绝过期执行者覆盖恢复后的状态。
func (s *MemoryTaskStore) ReplaceTask(ctx context.Context, r TaskRecord, revision int64) (bool, error) {
	if err := TaskProtocolVersion(r.StorageVersion); err != nil {
		return false, err
	}
	r.StorageVersion = TaskStorageVersion
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	old, ok := s.tasks[r.ID]
	if !ok || old.Scope != r.Scope || old.Revision != revision {
		return false, nil
	}
	if r.Key != old.Key || r.Digest != old.Digest || r.Revision != revision+1 {
		return false, ErrTaskConflict
	}
	s.tasks[r.ID] = cloneTaskRecord(r)
	return true, nil
}

// ListTasks 有序分页只返回元数据；活跃扫描不受历史正文大小影响。
func (s *MemoryTaskStore) ListTasks(ctx context.Context, scope string, query TaskScan) ([]TaskRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := []TaskRecord{}
	for _, r := range s.tasks {
		if (scope == "" || r.Scope == scope) && r.ID > query.After && (!query.ActiveOnly || !taskTerminal(r.Info.State)) {
			rec := cloneTaskRecord(r)
			rec.Args = nil
			rec.Info.Result = ""
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if query.Limit > 0 && len(out) > query.Limit {
		out = out[:query.Limit]
	}
	return out, nil
}

// CreateWait 原子保存等待条件，相同登记只能取回原记录。
func (s *MemoryTaskStore) CreateWait(ctx context.Context, r TaskWaitRecord) (TaskWaitRecord, bool, error) {
	if err := TaskProtocolVersion(r.StorageVersion); err != nil {
		return TaskWaitRecord{}, false, err
	}
	r.StorageVersion = TaskStorageVersion
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return TaskWaitRecord{}, false, err
	}
	for _, old := range s.waits {
		if old.Scope == r.Scope && old.Key == r.Key {
			return cloneWaitRecord(old), false, nil
		}
	}
	if _, ok := s.waits[r.ID]; ok {
		return TaskWaitRecord{}, false, ErrTaskConflict
	}
	s.waits[r.ID] = cloneWaitRecord(r)
	return cloneWaitRecord(r), true, nil
}

// ReplaceWait 用 CAS 串行化交付租约、确认与取消，不依赖进程内锁。
func (s *MemoryTaskStore) ReplaceWait(ctx context.Context, r TaskWaitRecord, revision int64) (bool, error) {
	if err := TaskProtocolVersion(r.StorageVersion); err != nil {
		return false, err
	}
	r.StorageVersion = TaskStorageVersion
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	old, ok := s.waits[r.ID]
	if !ok || old.Scope != r.Scope || old.Revision != revision {
		return false, nil
	}
	if old.Key != r.Key || old.Digest != r.Digest || r.Revision != revision+1 {
		return false, ErrTaskConflict
	}
	s.waits[r.ID] = cloneWaitRecord(r)
	return true, nil
}

// ListWaits 按稳定游标查询条件，活跃扫描跳过已交付的历史。
func (s *MemoryTaskStore) ListWaits(ctx context.Context, scope string, query TaskScan) ([]TaskWaitRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := []TaskWaitRecord{}
	for _, r := range s.waits {
		if (scope == "" || r.Scope == scope) && r.ID > query.After && (!query.ActiveOnly || r.State == "waiting" || r.State == "delivering") {
			out = append(out, cloneWaitRecord(r))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if query.Limit > 0 && len(out) > query.Limit {
		out = out[:query.Limit]
	}
	return out, nil
}

// Collect 分层回收正文与幂等记录，不删除仍在执行的调用。
func (s *MemoryTaskStore) Collect(ctx context.Context, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	for id, r := range s.tasks {
		if !taskTerminal(r.Info.State) {
			continue
		}
		if !r.ExpiresAt.IsZero() && !r.ExpiresAt.After(now) {
			delete(s.tasks, id)
			continue
		}
		if !r.ResultExpiresAt.IsZero() && !r.ResultExpiresAt.After(now) && (!r.Info.ResultExpired || r.Info.Result != "" || r.Info.State != "unknown" && len(r.Args) > 0) {
			r.Info.Result = ""
			r.Info.ResultExpired = true
			if r.Info.State != "unknown" {
				r.Args = nil
			}
			r.Revision++
			s.tasks[id] = r
		}
	}
	for id, r := range s.waits {
		if !r.ExpiresAt.After(now) {
			delete(s.waits, id)
		}
	}
	return nil
}

// GetWait 返回原等待条件的副本，交付控制仍须校验版本。
func (s *MemoryTaskStore) GetWait(ctx context.Context, scope, id string) (TaskWaitRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return TaskWaitRecord{}, err
	}
	w, ok := s.waits[id]
	if !ok || w.Scope != scope {
		return TaskWaitRecord{}, ErrTaskNotFound
	}
	return cloneWaitRecord(w), nil
}

// FindWait 在资源预留前查询同一逻辑登记；重复调用不重复消耗等待额度。
func (s *MemoryTaskStore) FindWait(ctx context.Context, scope, key string) (TaskWaitRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return TaskWaitRecord{}, err
	}
	for _, row := range s.waits {
		if row.Scope == scope && row.Key == key {
			return cloneWaitRecord(row), nil
		}
	}
	return TaskWaitRecord{}, ErrTaskNotFound
}
