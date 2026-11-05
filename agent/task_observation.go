package agent

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// TaskObservation 只包含关联和状态事实，不接受参数、正文、检查点或事件载荷。
// ID 按时间排序；InvocationID是摘要，不能反推出宿主调用身份或授权内容。
type TaskObservation struct {
	ID             string    `json:"id" bson:"id"`
	Scope          string    `json:"-" bson:"scope"`
	TraceID        string    `json:"trace_id" bson:"traceID"`
	TaskID         string    `json:"task_id,omitempty" bson:"taskID,omitempty"`
	ParentID       string    `json:"parent_id,omitempty" bson:"parentID,omitempty"`
	InvocationID   string    `json:"invocation_id,omitempty" bson:"invocationID,omitempty"`
	TurnID         string    `json:"turn_id,omitempty" bson:"turnID,omitempty"`
	WaitID         string    `json:"wait_id,omitempty" bson:"waitID,omitempty"`
	WakeID         string    `json:"wake_id,omitempty" bson:"wakeID,omitempty"`
	ContextID      string    `json:"context_id,omitempty" bson:"contextID,omitempty"`
	SourceID       string    `json:"source_id,omitempty" bson:"sourceID,omitempty"`
	SubscriptionID string    `json:"subscription_id,omitempty" bson:"subscriptionID,omitempty"`
	EventID        string    `json:"event_id,omitempty" bson:"eventID,omitempty"`
	Tool           string    `json:"tool,omitempty" bson:"tool,omitempty"`
	Kind           string    `json:"kind" bson:"kind"`
	State          string    `json:"state,omitempty" bson:"state,omitempty"`
	Code           string    `json:"code,omitempty" bson:"code,omitempty"`
	Revision       int64     `json:"revision,omitempty" bson:"revision,omitempty"`
	InputTokens    int64     `json:"input_tokens,omitempty" bson:"inputTokens,omitempty"`
	OutputTokens   int64     `json:"output_tokens,omitempty" bson:"outputTokens,omitempty"`
	DurationMS     int64     `json:"duration_ms,omitempty" bson:"durationMS,omitempty"`
	At             time.Time `json:"at" bson:"at"`
	ExpiresAt      time.Time `json:"expires_at" bson:"expiresAt"`
}

// TaskObservationQuery 为快照分页，不能把并发写入期间的一页当作完整实时事件流。
type TaskObservationQuery struct {
	After                                                                       string
	Limit                                                                       int
	TaskID, WaitID, TraceID, Kind, ContextID, SourceID, SubscriptionID, EventID string
	From, To                                                                    time.Time
}

// TaskObservationStore 必须按(scope,ID)原子去重，按ID升序分页并服从ctx取消。
// 收到完整记录才表示持久成功；观测不是调用账本，也不能决定是否重放业务。
type TaskObservationStore interface {
	AppendObservation(context.Context, TaskObservation) (bool, error)
	ListObservations(context.Context, string, TaskObservationQuery) ([]TaskObservation, error)
	CollectObservations(context.Context, time.Time) error
}

// TaskObservationPage 明确给出下一游标和本地观测损失，不隐瞒有界队列背压。
type TaskObservationPage struct {
	Records []TaskObservation `json:"records"`
	Next    string            `json:"next,omitempty"`
	Dropped uint64            `json:"local_dropped"`
	Failed  uint64            `json:"local_failed"`
	Pending int64             `json:"local_pending"`
}

// TaskObservationSummary 仅汇总返回的有界快照；Truncated/Next表示还有数据而非全量统计。
type TaskObservationSummary struct {
	Count     int            `json:"count"`
	Counts    map[string]int `json:"counts"`
	Truncated bool           `json:"truncated"`
	Next      string         `json:"next,omitempty"`
	Dropped   uint64         `json:"local_dropped"`
	Failed    uint64         `json:"local_failed"`
	Pending   int64          `json:"local_pending"`
}

// MemoryTaskObservationStore 提供内存观测目录，重用同实例可验证重建后查询与去重。
type MemoryTaskObservationStore struct {
	mu   sync.Mutex
	rows map[string]TaskObservation
}

// NewMemoryTaskObservationStore 创建独立目录，不宣称进程重启持久。
func NewMemoryTaskObservationStore() *MemoryTaskObservationStore {
	return &MemoryTaskObservationStore{rows: map[string]TaskObservation{}}
}

// AppendObservation 原子去重，同一ID不同事实必须拒绝而不是覆盖旧追踪。
func (s *MemoryTaskObservationStore) AppendObservation(ctx context.Context, row TaskObservation) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := ValidateTaskObservation(row); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := taskHash([]string{row.Scope, row.ID})
	if old, ok := s.rows[key]; ok {
		if TaskObservationIdentity(old) != TaskObservationIdentity(row) {
			return false, ErrTaskConflict
		}
		return false, nil
	}
	s.rows[key] = row
	return true, nil
}

// ListObservations 只读未过期快照并保持游标有序，过滤条件不扩大作用域。
func (s *MemoryTaskObservationStore) ListObservations(ctx context.Context, scope string, q TaskObservationQuery) ([]TaskObservation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []TaskObservation{}
	for _, row := range s.rows {
		if observationMatches(row, scope, q) {
			out = append(out, row)
		}
	}
	sortObservations(out)
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

// CollectObservations 只执行保留策略，观测过期不能改变调用或等待的真实状态。
func (s *MemoryTaskObservationStore) CollectObservations(ctx context.Context, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, row := range s.rows {
		if !row.ExpiresAt.After(now) {
			delete(s.rows, key)
		}
	}
	return nil
}
func observationMatches(row TaskObservation, scope string, q TaskObservationQuery) bool {
	return row.Scope == scope && row.ID > q.After && row.ExpiresAt.After(time.Now()) && (q.TaskID == "" || q.TaskID == row.TaskID) && (q.WaitID == "" || q.WaitID == row.WaitID) && (q.TraceID == "" || q.TraceID == row.TraceID) && (q.Kind == "" || q.Kind == row.Kind) && (q.ContextID == "" || q.ContextID == row.ContextID) && (q.SourceID == "" || q.SourceID == row.SourceID) && (q.SubscriptionID == "" || q.SubscriptionID == row.SubscriptionID) && (q.EventID == "" || q.EventID == row.EventID) && (q.From.IsZero() || !row.At.Before(q.From)) && (q.To.IsZero() || row.At.Before(q.To))
}
func sortObservations(rows []TaskObservation) {
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
}
func safeObservationValue(value string, limit int) bool {
	if len(value) > limit {
		return false
	}
	for _, ch := range value {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-' || ch == '.' || ch == '/' || ch == ':') {
			return false
		}
	}
	return true
}

// ValidateTaskObservation 仅允许有界关联字段和枚举式状态，原始业务文本不能进入观测目录。
func ValidateTaskObservation(row TaskObservation) error {
	if row.Scope == "" || len(row.Scope) > 256 || row.ID == "" || !safeObservationValue(row.ID, 128) || !safeObservationValue(row.TraceID, 128) || row.Kind == "" || !safeObservationValue(row.Kind, 64) || !safeObservationValue(row.State, 64) || !safeObservationValue(row.Code, 64) || row.Revision < 0 || row.DurationMS < 0 || row.InputTokens < 0 || row.OutputTokens < 0 || row.At.IsZero() || !row.ExpiresAt.After(row.At) {
		return ErrTaskConflict
	}
	for _, value := range []string{row.TaskID, row.ParentID, row.InvocationID, row.TurnID, row.WaitID, row.WakeID, row.ContextID, row.SourceID, row.SubscriptionID, row.EventID, row.Tool} {
		if !safeObservationValue(value, 256) {
			return ErrTaskConflict
		}
	}
	return nil
}

type taskObservationRuntime struct {
	mu      sync.Mutex
	store   TaskObservationStore
	queue   chan TaskObservation
	stop    chan struct{}
	done    chan struct{}
	timeout time.Duration
	closed  atomic.Bool
	dropped atomic.Uint64
	failed  atomic.Uint64
	pending atomic.Int64
}

func newTaskObserver(store TaskObservationStore, timeout time.Duration) *taskObservationRuntime {
	o := &taskObservationRuntime{store: store, queue: make(chan TaskObservation, 128), stop: make(chan struct{}), done: make(chan struct{}), timeout: timeout}
	go o.loop()
	return o
}
func (o *taskObservationRuntime) write(row TaskObservation) {
	defer o.pending.Add(-1)
	ctx, stop := context.WithTimeout(context.Background(), o.timeout)
	defer stop()
	func() {
		defer func() {
			if recover() != nil {
				o.failed.Add(1)
			}
		}()
		if _, err := o.store.AppendObservation(ctx, row); err != nil {
			o.failed.Add(1)
		}
	}()
}
func (o *taskObservationRuntime) loop() {
	defer close(o.done)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case row := <-o.queue:
			o.write(row)
		case <-ticker.C:
			func() {
				defer func() {
					if recover() != nil {
						o.failed.Add(1)
					}
				}()
				ctx, stop := context.WithTimeout(context.Background(), o.timeout)
				defer stop()
				if o.store.CollectObservations(ctx, time.Now().UTC()) != nil {
					o.failed.Add(1)
				}
			}()
		case <-o.stop:
			deadline := time.Now().Add(2 * o.timeout)
			for time.Now().Before(deadline) {
				select {
				case row := <-o.queue:
					o.write(row)
				default:
					return
				}
			}
			for {
				select {
				case <-o.queue:
					o.pending.Add(-1)
					o.dropped.Add(1)
				default:
					return
				}
			}
		}
	}
}
func (o *taskObservationRuntime) close() {
	if o == nil {
		return
	}
	o.mu.Lock()
	if o.closed.CompareAndSwap(false, true) {
		close(o.stop)
	}
	o.mu.Unlock()
	select {
	case <-o.done:
	case <-time.After(3*o.timeout + time.Millisecond):
	}
}

// RecordObservation 接受白名单元数据并有界异步提交；旧代次收尾也应记录，因此不执行新的权限/业务准入。
func (r *TaskRuntime) RecordObservation(ctx context.Context, scope string, row TaskObservation) error {
	if scope == "" || r.cfg.Scope != "" && r.cfg.Scope != scope || r.observer == nil {
		return ErrTaskNotFound
	}
	if row.TraceID == "" {
		row.TraceID = TaskTraceID(ctx)
	}
	var err error
	row, err = PrepareTaskObservation(scope, row, r.cfg.ObservationRetention)
	if err != nil {
		r.observer.dropped.Add(1)
		return err
	}
	observeTelemetry(ctx, row)
	o := r.observer
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed.Load() {
		o.dropped.Add(1)
		return context.Canceled
	}
	o.pending.Add(1)
	select {
	case o.queue <- row:
		return nil
	default:
		o.pending.Add(-1)
		o.dropped.Add(1)
		return errors.New("agent: observation queue full")
	}
}

// Observations 查询当前可信作用域的持久追踪快照，不暴露其他租户查询参数。
func (r *TaskRuntime) Observations(ctx context.Context, scope string, q TaskObservationQuery) (TaskObservationPage, error) {
	if scope == "" || r.cfg.Scope != "" && r.cfg.Scope != scope {
		return TaskObservationPage{}, ErrTaskNotFound
	}
	if q.Limit == 0 {
		q.Limit = 64
	}
	if q.Limit < 1 || q.Limit > 1024 || len(q.After) > 128 || !q.To.IsZero() && !q.From.IsZero() && !q.To.After(q.From) {
		return TaskObservationPage{}, ErrTaskConflict
	}
	rows, err := r.cfg.Observations.ListObservations(ctx, scope, q)
	if err != nil {
		return TaskObservationPage{}, WrapTaskStorage(err)
	}
	page := TaskObservationPage{Records: rows}
	if len(rows) == q.Limit {
		page.Next = rows[len(rows)-1].ID
	}
	if r.observer != nil {
		page.Dropped = r.observer.dropped.Load()
		page.Failed = r.observer.failed.Load()
		page.Pending = r.observer.pending.Load()
	}
	return page, nil
}

// ObservationStats 只汇总最多1024条返回记录，明确报告截断和后续游标。
func (r *TaskRuntime) ObservationStats(ctx context.Context, scope string, q TaskObservationQuery) (TaskObservationSummary, error) {
	if q.Limit == 0 {
		q.Limit = 1024
	}
	page, err := r.Observations(ctx, scope, q)
	if err != nil {
		return TaskObservationSummary{}, err
	}
	out := TaskObservationSummary{Count: len(page.Records), Counts: map[string]int{}, Truncated: page.Next != "", Next: page.Next, Dropped: page.Dropped, Failed: page.Failed, Pending: page.Pending}
	for _, row := range page.Records {
		key := row.Kind + "/" + row.State + "/" + row.Code
		out.Counts[key]++
	}
	return out, nil
}
func (r *TaskRuntime) observeRecord(ctx context.Context, rec TaskRecord, kind string) {
	code := ""
	if failure := failureForRecord(rec); failure != nil {
		code = failure.Code
	}
	_ = r.RecordObservation(ctx, rec.Scope, TaskObservation{TaskID: rec.ID, ParentID: rec.Info.ParentID, TraceID: rec.Info.TraceID, InvocationID: rec.Invocation, TurnID: rec.Turn, WakeID: TaskWakeID(ctx), Tool: rec.Info.Tool, Kind: kind, State: rec.Info.State, Code: code, Revision: rec.Revision, At: rec.UpdatedAt, DurationMS: max(0, rec.UpdatedAt.Sub(rec.CreatedAt).Milliseconds())})
}

// TaskObservationIdentity 对白名单元数据求指纹，持久适配器可拒绝同ID不同内容。
func TaskObservationIdentity(row TaskObservation) string {
	row.At = row.At.UTC().Truncate(time.Millisecond)
	row.ExpiresAt = row.ExpiresAt.UTC().Truncate(time.Millisecond)
	return taskHash([]any{row.Scope, row})
}

// PrepareTaskObservation 供可信宿主发布器归一化白名单元数据，不调用存储或解释业务载荷。
func PrepareTaskObservation(scope string, row TaskObservation, retention time.Duration) (TaskObservation, error) {
	if retention == 0 {
		retention = 30 * 24 * time.Hour
	}
	if retention < time.Second || retention > 365*24*time.Hour {
		return TaskObservation{}, ErrTaskConflict
	}
	row.Scope = scope
	if row.At.IsZero() {
		row.At = time.Now().UTC()
	}
	if row.ExpiresAt.IsZero() {
		row.ExpiresAt = row.At.Add(retention)
	}
	if row.TraceID == "" {
		row.TraceID = taskHash([]string{scope, row.TurnID, row.TaskID, row.WaitID, row.ContextID, row.SourceID})
	}
	// 不将宿主原调用键或逻辑轮次明文放入可读目录。
	if row.InvocationID != "" {
		row.InvocationID = taskHash(row.InvocationID)
	}
	if row.TurnID != "" {
		row.TurnID = taskHash(row.TurnID)
	}
	row.At = row.At.UTC().Truncate(time.Millisecond)
	row.ExpiresAt = row.ExpiresAt.UTC().Truncate(time.Millisecond)
	if row.ID == "" {
		nonce := ""
		if row.TaskID == "" && row.WaitID == "" && row.ContextID == "" && row.SourceID == "" {
			nonce = rand.Text()
		}
		row.ID = fmt.Sprintf("%020d_%s", row.At.UnixNano(), taskHash([]any{scope, row.TraceID, row.TaskID, row.WaitID, row.ContextID, row.SourceID, row.SubscriptionID, row.EventID, row.Kind, row.Revision, row.InvocationID, row.TurnID, row.Tool, row.State, row.Code, nonce}))
	}
	if err := ValidateTaskObservation(row); err != nil {
		return TaskObservation{}, err
	}
	return row, nil
}

func (r *TaskRuntime) observeWait(ctx context.Context, w TaskWaitRecord, kind, code string) {
	at := time.Now().UTC()
	if kind == "wait_registered" {
		at = w.CreatedAt
	}
	_ = r.RecordObservation(ctx, w.Scope, TaskObservation{WaitID: w.ID, WakeID: TaskWakeID(ctx), TurnID: w.Turn, TraceID: w.TraceID, Kind: kind, State: w.State, Code: code, Revision: w.Revision, At: at, DurationMS: max(0, at.Sub(w.CreatedAt).Milliseconds())})
	if kind == "wake_claimed" {
		for _, match := range w.EventMatches {
			_ = r.RecordObservation(ctx, w.Scope, TaskObservation{WaitID: w.ID, TraceID: w.TraceID, SourceID: match.SourceID, EventID: fmt.Sprintf("%s:%d", match.SourceID, match.Seq), Kind: "wait_event_matched", State: match.State, Revision: w.Revision, At: at})
		}
	}
	if kind == "wait_registered" {
		for _, reference := range w.Spec.Contexts {
			_ = r.RecordObservation(ctx, w.Scope, TaskObservation{WaitID: w.ID, TraceID: w.TraceID, ContextID: reference.ID, Kind: "wait_context_bound", State: "referenced", Revision: reference.Revision, At: at})
		}
	}
}

type taskTraceContextKey struct{}

// TaskTraceID 读取受管关联身份，不从业务JSON或浏览器请求编号取得授权。
func TaskTraceID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(taskTraceContextKey{}).(string)
	return value
}

// TraceContext 为同一公开规划/续办保留追踪身份；它只关联记录，不赋予执行权限。
func (r *TaskRuntime) TraceContext(ctx context.Context, scope, turn string) context.Context {
	if ctx == nil || TaskTraceID(ctx) != "" {
		return ctx
	}
	return context.WithValue(ctx, taskTraceContextKey{}, taskHash([]string{r.cfg.ControlNamespace, scope, turn}))
}
