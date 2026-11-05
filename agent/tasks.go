package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.opentelemetry.io/otel/attribute"
)

// TaskConfig 约束资源与记录保留，不决定业务步骤。Store 必须由宿主绑定可信命名空间。
type TaskConfig struct {
	// StorageMode 明确声明持久能力；durable 禁止任何权威组件回退内存。
	StorageMode                                                            string
	MaxConcurrent, MaxTasks, MaxResultBytes, MaxWakeAttempts, MaxWakeDepth int
	Timeout, LeaseDuration, ResultRetention, LedgerRetention, PollInterval time.Duration
	// Results 保存完整回执；MaxResultBytes 只限制向模型内联的正文，不决定执行成败。
	Results                                  TaskResultStore
	MaxStoredResultBytes                     int
	ResultSaveTimeout                        time.Duration
	Store                                    TaskStore
	Scope                                    string
	OnComplete                               func(TaskInfo)
	ControlStore                             TaskControlStore
	ControlNamespace                         string
	BudgetPool                               string
	Budgets                                  []TaskBudgetLimit
	Observations                             TaskObservationStore
	ObservationRetention, ObservationTimeout time.Duration
	// Contexts 保存AI可见的长期工作事实，独立于有损聊天历史和业务授权。
	Contexts                                          TaskContextStore
	ContextRetention                                  time.Duration
	MaxContexts, MaxContextBytes, MaxContextMutations int
	Events                                            TaskEventStore
	MaxEventRecords                                   int
	Outbox                                            TaskOutboxStore
	GoalLimits                                        TaskGoalLimits
	QoS                                               TaskQoSConfig
	BudgetPolicyVersion                               int64
}

// TaskInfo 的 completed 表示 Handler 已返回，不能代替原异步业务的完成状态。
type TaskInfo struct {
	ID              string         `json:"id" bson:"id"`
	ContractVersion string         `json:"contract_version,omitempty" bson:"contractVersion,omitempty"`
	ParentID        string         `json:"parent_id,omitempty" bson:"parentID,omitempty"`
	Tool            string         `json:"tool" bson:"tool"`
	State           string         `json:"state" bson:"state"`
	Completion      ToolCompletion `json:"completion,omitempty" bson:"completion,omitempty"`
	TraceID         string         `json:"trace_id,omitempty" bson:"traceID,omitempty"`
	// Background 是调用方选择的执行方式，与原业务是否异步完成无关。
	Background    bool            `json:"background" bson:"background"`
	Result        string          `json:"result,omitempty" bson:"result,omitempty"`
	Error         string          `json:"error,omitempty" bson:"error,omitempty"`
	Input         json.RawMessage `json:"arguments,omitempty" bson:"-"`
	ResultExpired bool            `json:"result_expired,omitempty" bson:"resultExpired,omitempty"`
	ResolutionRef *TaskResultRef  `json:"resolution_ref,omitempty" bson:"resolutionRef,omitempty"`
	ResultRef     *TaskResultRef  `json:"result_ref,omitempty" bson:"resultRef,omitempty"`
	Failure       *TaskFailure    `json:"failure,omitempty" bson:"failure,omitempty"`
}

type toolTask struct {
	scope, turn, id string
	cancel          context.CancelFunc
	unlink          func() bool
	done            chan struct{}
	background      bool
	epoch           string
}

// TaskRuntime 只保留在途 goroutine；已完成调用由账本查询，不再占用执行容量。
// 进程失去的 goroutine 不会重建：过期租约经 CAS 转为取消/未知，禁止盲目重放。
type TaskRuntime struct {
	cfg             TaskConfig
	ctx             context.Context
	cancel          context.CancelFunc
	owner           string
	mu              sync.Mutex
	tasks           map[string]*toolTask
	seenScopes      map[string]bool
	reserved        int
	slots           chan struct{}
	changed         chan struct{}
	completed       chan TaskInfo
	wg              sync.WaitGroup
	maintenanceDone chan struct{}
	closed          bool
	closeOnce       sync.Once
	drainOnce       sync.Once
	shutdownOnce    sync.Once
	workersDone     chan struct{}
	shutdownDone    chan struct{}
	callbackSlot    chan struct{}
	callbackDropped atomic.Uint64
	observer        *taskObservationRuntime
}

// NewTaskRuntime 默认使用内存账本；宿主接入持久 TaskStore 后具备跨实例/重启记录恢复能力。
func NewTaskRuntime(ctx context.Context, cfg TaskConfig) (*TaskRuntime, error) {
	if _, err := validateTaskStorage(cfg); err != nil {
		return nil, err
	}
	if ctx == nil || ctx.Err() != nil || cfg.MaxConcurrent < 0 || cfg.MaxConcurrent > 256 || cfg.MaxTasks < 0 || cfg.MaxTasks > 4096 || cfg.MaxResultBytes < 0 || cfg.MaxResultBytes > 1<<20 || cfg.Timeout < 0 || cfg.Timeout > 24*time.Hour || cfg.LeaseDuration < 0 || cfg.ResultRetention < 0 || cfg.LedgerRetention < 0 || cfg.PollInterval < 0 || len(cfg.Scope) > 256 {
		return nil, errors.New("agent: invalid task configuration")
	}
	if cfg.MaxWakeAttempts == 0 {
		cfg.MaxWakeAttempts = 3
	}
	if cfg.MaxWakeDepth == 0 {
		cfg.MaxWakeDepth = 32
	}
	if cfg.MaxWakeAttempts < 1 || cfg.MaxWakeAttempts > 32 || cfg.MaxWakeDepth < 1 || cfg.MaxWakeDepth > 1024 {
		return nil, errors.New("agent: invalid wake budget")
	}
	if err := ValidateTaskGoalLimits(cfg.GoalLimits); err != nil {
		return nil, err
	}
	if cfg.MaxConcurrent == 0 {
		cfg.MaxConcurrent = 8
	}
	if cfg.MaxTasks == 0 {
		cfg.MaxTasks = 128
	}
	if cfg.MaxResultBytes == 0 {
		cfg.MaxResultBytes = 64 << 10
	}
	if cfg.MaxStoredResultBytes == 0 {
		cfg.MaxStoredResultBytes = 16 << 20
	}
	if cfg.ResultSaveTimeout == 0 {
		cfg.ResultSaveTimeout = 30 * time.Second
	}
	if cfg.MaxStoredResultBytes < cfg.MaxResultBytes || cfg.MaxStoredResultBytes > 256<<20 || cfg.ResultSaveTimeout < time.Millisecond || cfg.ResultSaveTimeout > time.Hour {
		return nil, errors.New("agent: invalid result storage configuration")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Minute
	}
	if cfg.LeaseDuration == 0 {
		cfg.LeaseDuration = 30 * time.Second
	}
	if cfg.ResultRetention == 0 {
		cfg.ResultRetention = 24 * time.Hour
	}
	if cfg.LedgerRetention == 0 {
		cfg.LedgerRetention = 30 * 24 * time.Hour
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = time.Second
	}
	if cfg.LeaseDuration < 30*time.Millisecond || cfg.LeaseDuration > 5*time.Minute || cfg.PollInterval < time.Millisecond || cfg.PollInterval > time.Minute || cfg.LedgerRetention < cfg.ResultRetention {
		return nil, errors.New("agent: invalid task retention or lease")
	}
	if cfg.Store == nil {
		cfg.Store = NewMemoryTaskStore()
	}
	if cfg.ControlStore == nil {
		if memory, ok := cfg.Store.(*MemoryTaskStore); ok {
			cfg.ControlStore = memory.control
		} else {
			cfg.ControlStore = NewMemoryTaskControlStore()
		}
	}
	if len(cfg.Budgets) > 8 || len(cfg.BudgetPool) > 256 || len(cfg.ControlNamespace) > 1024 {
		return nil, errors.New("agent: invalid shared task budgets")
	}
	seenBudgets := map[string]bool{}
	for _, limit := range cfg.Budgets {
		if limit.Key == "" || len(limit.Key) > 256 || seenBudgets[limit.Key] || limit.MaxActive < 1 || limit.MaxActive > 4096 || limit.MaxConcurrent < 1 || limit.MaxConcurrent > limit.MaxActive || limit.MaxWaiting < 1 || limit.MaxWaiting > 4096 {
			return nil, errors.New("agent: invalid shared task budget limit")
		}
		seenBudgets[limit.Key] = true
	}
	if cfg.BudgetPolicyVersion == 0 {
		cfg.BudgetPolicyVersion = 1
	}
	if cfg.BudgetPolicyVersion < 1 {
		return nil, ErrTaskPolicyConflict
	}
	if err := ValidateTaskQoSConfig(cfg.QoS); err != nil {
		return nil, err
	}
	cfg.QoS.Policies = append([]TaskResourcePolicy(nil), cfg.QoS.Policies...)
	cfg.Budgets = append([]TaskBudgetLimit(nil), cfg.Budgets...)
	if cfg.Results == nil {
		// 显式持久宿主应同时提供结果存储；内存默认仅适用于会话内使用。
		if memory, ok := cfg.Store.(*MemoryTaskStore); ok {
			cfg.Results = memory.results
		} else if results, ok := cfg.Store.(TaskResultStore); ok {
			cfg.Results = results
		} else {
			cfg.Results = NewMemoryTaskResultStore()
		}
	}
	if cfg.ContextRetention == 0 {
		cfg.ContextRetention = 30 * 24 * time.Hour
	}
	if cfg.MaxContexts == 0 {
		cfg.MaxContexts = 128
	}
	if cfg.MaxContextBytes == 0 {
		cfg.MaxContextBytes = 32 << 10
	}
	if cfg.MaxContextMutations == 0 {
		cfg.MaxContextMutations = 4096
	}
	if cfg.ContextRetention < time.Millisecond || cfg.ContextRetention > 365*24*time.Hour || cfg.MaxContexts < 1 || cfg.MaxContexts > 128 || cfg.MaxContextBytes < 1024 || cfg.MaxContextBytes > 64<<10 || cfg.MaxContextMutations < 1 || cfg.MaxContextMutations > 4096 {
		return nil, errors.New("agent: invalid task context configuration")
	}
	if cfg.Contexts == nil {
		if memory, ok := cfg.Store.(*MemoryTaskStore); ok {
			cfg.Contexts = memory.contexts
		} else {
			cfg.Contexts = NewMemoryTaskContextStore()
		}
	}
	if cfg.MaxEventRecords == 0 {
		cfg.MaxEventRecords = 256
	}
	if cfg.MaxEventRecords < 1 || cfg.MaxEventRecords > 256 {
		return nil, errors.New("agent: invalid external event capacity")
	}
	if cfg.Events == nil {
		if memory, ok := cfg.Store.(*MemoryTaskStore); ok {
			memory.mu.Lock()
			if memory.events == nil {
				memory.events = NewMemoryTaskEventStore()
			}
			cfg.Events = memory.events
			memory.mu.Unlock()
		} else {
			cfg.Events = NewMemoryTaskEventStore()
		}
	}
	if cfg.ObservationRetention == 0 {
		cfg.ObservationRetention = 30 * 24 * time.Hour
	}
	if cfg.ObservationTimeout == 0 {
		cfg.ObservationTimeout = 100 * time.Millisecond
	}
	if cfg.ObservationRetention < time.Second || cfg.ObservationRetention > 365*24*time.Hour || cfg.ObservationTimeout < time.Millisecond || cfg.ObservationTimeout > time.Second {
		return nil, errors.New("agent: invalid observation configuration")
	}
	if cfg.Observations == nil {
		if memory, ok := cfg.Store.(*MemoryTaskStore); ok {
			cfg.Observations = memory.observations
		} else {
			cfg.Observations = NewMemoryTaskObservationStore()
		}
	}
	if cfg.StorageMode == "" {
		cfg.StorageMode = TaskStorageMemory
	}
	ctx, cancel := context.WithCancel(ctx)
	r := &TaskRuntime{cfg: cfg, ctx: ctx, cancel: cancel, owner: rand.Text(), tasks: map[string]*toolTask{}, seenScopes: map[string]bool{}, slots: make(chan struct{}, cfg.MaxConcurrent), changed: make(chan struct{}), completed: make(chan TaskInfo, cfg.MaxTasks), workersDone: make(chan struct{}), shutdownDone: make(chan struct{}), callbackSlot: make(chan struct{}, 1), maintenanceDone: make(chan struct{})}
	if cfg.StorageMode == TaskStorageDurable {
		if _, err := r.Readiness(ctx); err != nil {
			cancel()
			return nil, err
		}
	}
	r.observer = newTaskObserver(cfg.Observations, cfg.ObservationTimeout)
	go r.maintain()
	return r, nil
}
func taskHash(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (r *TaskRuntime) operation() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

// Store 返回宿主绑定的账本，供受控维护与恢复使用。
func (r *TaskRuntime) Store() TaskStore { return r.cfg.Store }

// Scope 返回固定的权限作用域，模型不能覆盖它。
func (r *TaskRuntime) Scope() string { return r.cfg.Scope }

// PollInterval 仅控制状态检查，不产生模型轮询调用。
func (r *TaskRuntime) PollInterval() time.Duration { return r.cfg.PollInterval }

// Start 在创建 goroutine 前原子保存调用键与冻结参数；重复调用只读原记录。
func (r *TaskRuntime) Start(ctx context.Context, scope, turn, key string, tool Tool, args json.RawMessage) (TaskInfo, error) {
	if ctx == nil || ctx.Err() != nil || scope == "" || len(scope) > 256 || r.cfg.Scope != "" && r.cfg.Scope != scope || turn == "" || key == "" || tool.Name == "" || tool.Handler == nil || len(args) > 64<<10 || !json.Valid(args) {
		return TaskInfo{}, errors.New("agent: invalid task invocation")
	}
	r.mu.Lock()
	r.seenScopes[scope] = true
	r.mu.Unlock()
	ctx = r.TraceContext(ctx, scope, turn)
	outputSchema, contractErr := CompileToolContract(tool)
	if contractErr != nil {
		return TaskInfo{}, contractErr
	}
	args = append([]byte(nil), args...)
	compound := taskHash([]string{scope, turn, key})
	digest := taskToolDigest(tool, args)
	op, stop := r.operation()
	defer stop()
	epoch, epochErr := r.scopeEpoch(ctx, scope)
	if epochErr != nil {
		return TaskInfo{}, epochErr
	}
	if epochErr = r.checkEpoch(op, scope, epoch, false); epochErr != nil {
		return TaskInfo{}, epochErr
	}
	if old, err := r.cfg.Store.FindTask(op, scope, compound); err == nil {
		if old.Digest != digest {
			return TaskInfo{}, fmt.Errorf("%w: task invocation changed", ErrTaskConflict)
		}
		old, err = r.refresh(op, old)
		return r.visible(old), err
	} else if !errors.Is(err, ErrTaskNotFound) {
		return TaskInfo{}, WrapTaskStorage(err)
	}
	var goalErr error
	ctx, goalErr = r.newGoal(ctx, scope, taskHash([]string{scope, turn}))
	if goalErr != nil {
		return TaskInfo{}, goalErr
	}

	r.mu.Lock()
	if r.closed || r.ctx.Err() != nil {
		r.mu.Unlock()
		return TaskInfo{}, ErrTaskDraining
	}
	if len(r.tasks)+r.reserved >= r.cfg.MaxTasks {
		r.mu.Unlock()
		return TaskInfo{}, errors.New("agent: active task capacity reached")
	}
	r.reserved++
	// 接纳的存储请求也必须被 Close 等待；计数在锁内取得，随后转交给执行 goroutine。
	// 否则 CreateTask 尚未返回时 Wait 已结束，关闭后仍可能新增工作。
	r.wg.Add(1)
	r.mu.Unlock()
	launched := false
	defer func() {
		r.mu.Lock()
		r.reserved--
		r.mu.Unlock()
		if !launched {
			r.wg.Done()
		}
	}()
	now := time.Now().UTC()
	id := rand.Text()
	var claims []TaskResourceRequest
	if tool.ResolveResources != nil {
		var err error
		claims, err = tool.ResolveResources(ctx, args)
		if err != nil {
			return TaskInfo{}, err
		}
		if len(claims) > 32 {
			return TaskInfo{}, ErrTaskConflict
		}
		seen := map[string]bool{}
		for _, claim := range claims {
			if claim.Key == "" || len(claim.Key) > 256 || len(claim.IfVersion) > 256 || seen[claim.Key] {
				return TaskInfo{}, ErrTaskConflict
			}
			seen[claim.Key] = true
		}
	}
	deadline := minTaskTime(now.Add(r.cfg.Timeout), goalBinding(ctx).Deadline)
	synchronous, _ := ctx.Value(synchronousInvocationKey{}).(bool)
	if err := r.acquireBudget(op, scope, epoch, id, "task", minTaskTime(now.Add(r.cfg.LeaseDuration), deadline), claims...); err != nil {
		f := TaskFailureFrom(err, "not_started", false, "", nil)
		_ = r.RecordObservation(ctx, scope, TaskObservation{Kind: "admission_rejected", State: "not_started", Code: f.Code, TurnID: turn, TraceID: TaskTraceID(ctx)})
		return TaskInfo{}, err
	}
	defer func() {
		if !launched {
			r.releaseBudget(id)
		}
	}()
	record := TaskRecord{StorageVersion: TaskStorageVersion, ID: id, Scope: scope, Epoch: epoch, Key: compound, Digest: digest, Turn: turn, Args: args, Invocation: ToolInvocationKey(ctx), Effect: tool.Effect, Info: TaskInfo{ID: id, ContractVersion: tool.Contract.Version, ParentID: TaskID(ctx), Tool: tool.Name, State: "queued", Completion: tool.Completion, Background: !synchronous, TraceID: TaskTraceID(ctx)}, Owner: r.owner, Revision: 1, CreatedAt: now, UpdatedAt: now, Deadline: deadline, LeaseUntil: minTaskTime(now.Add(r.cfg.LeaseDuration), deadline)}
	saved, created, err := r.cfg.Store.CreateTask(op, record)
	if err != nil {
		return TaskInfo{}, TaskStorageCommitFailure(err, "not_started", id, nil)
	}
	if !created {
		if saved.Digest != digest {
			return TaskInfo{}, fmt.Errorf("%w: task invocation changed", ErrTaskConflict)
		}
		return r.visible(saved), nil
	}
	ctx = context.WithValue(ctx, taskEpochKey{}, taskEpochContext{r.scopeControlKey(scope), epoch})
	taskCtx, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
	unlinkRoot := context.AfterFunc(r.ctx, cancel)
	t := &toolTask{scope: scope, turn: turn, id: id, epoch: epoch, cancel: cancel, done: make(chan struct{}), background: !synchronous}
	t.unlink = context.AfterFunc(ctx, cancel)
	r.mu.Lock()
	if r.ctx.Err() != nil {
		cancel()
	}
	r.tasks[id] = t
	r.mu.Unlock()
	launched = true
	r.observeRecord(ctx, record, "task_admitted")
	go r.run(taskCtx, t, tool, args, unlinkRoot, outputSchema)
	return record.Info, nil
}
func minTaskTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func (r *TaskRuntime) visible(rec TaskRecord) TaskInfo {
	info := rec.Info
	if info.ResolutionRef != nil {
		ref := *info.ResolutionRef
		info.ResolutionRef = &ref
	}
	info.Failure = failureForRecord(rec)
	if info.ResultRef != nil {
		ref := *info.ResultRef
		info.ResultRef = &ref
	}
	if !rec.ResultExpiresAt.IsZero() && !rec.ResultExpiresAt.After(time.Now()) {
		info.Result = ""
		info.ResultExpired = true
		if info.ResultRef != nil {
			info.ResultRef.Availability = "expired"
		}
	}
	if info.Failure != nil && info.ResultRef != nil {
		ref := *info.ResultRef
		info.Failure.ResultRef = &ref
	}
	return info
}

// ownedUpdate 每次核对当前租约及 fencing 版本，取消申请不会使旧执行器覆盖他人恢复的终态。
func (r *TaskRuntime) ownedUpdate(ctx context.Context, t *toolTask, change func(*TaskRecord)) (TaskRecord, error) {
	for range 8 {
		rec, err := r.cfg.Store.GetTask(ctx, t.scope, t.id)
		if err != nil {
			return rec, err
		}
		if rec.Owner != r.owner || taskTerminal(rec.Info.State) || !rec.LeaseUntil.After(time.Now()) {
			return rec, ErrTaskConflict
		}
		rev := rec.Revision
		change(&rec)
		rec.Revision++
		rec.UpdatedAt = time.Now().UTC()
		ok, err := r.cfg.Store.ReplaceTask(ctx, rec, rev)
		if err != nil {
			return rec, err
		}
		if ok {
			return rec, nil
		}
	}
	return TaskRecord{}, ErrTaskConflict
}
func (r *TaskRuntime) run(ctx context.Context, t *toolTask, tool Tool, args json.RawMessage, unlinkRoot func() bool, outputSchema *jsonschema.Schema) {
	ctx, span := RuntimeSpan(ctx, "agent.tool", attribute.String("tool.name", tool.Name), attribute.String("task.id", t.id))
	defer span.End()
	defer r.wg.Done()
	defer t.cancel()
	defer unlinkRoot()
	renewStop := make(chan struct{})
	renewDone := make(chan struct{})
	go func() {
		defer close(renewDone)
		ticker := time.NewTicker(r.cfg.LeaseDuration / 3)
		defer ticker.Stop()
		for {
			select {
			case <-renewStop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				op, stop := r.operation()
				if r.checkEpoch(op, t.scope, t.epoch, false) != nil || r.updateBudget(op, t.id, "renew", minTaskTime(time.Now().Add(r.cfg.LeaseDuration), deadlineFromContext(ctx))) != nil {
					stop()
					t.cancel()
					return
				}
				rec, err := r.ownedUpdate(op, t, func(rec *TaskRecord) { rec.LeaseUntil = minTaskTime(time.Now().Add(r.cfg.LeaseDuration), rec.Deadline) })
				stop()
				if err != nil || rec.CancelRequested {
					t.cancel()
					return
				}
			}
		}
	}()
	result := ""
	var callErr error
	started := false
	func() {
		defer func() {
			if recover() != nil {
				callErr = errors.New("handler panic")
			}
		}()
		select {
		case r.slots <- struct{}{}:
			defer func() { <-r.slots }()
		case <-ctx.Done():
			callErr = ctx.Err()
			return
		}
		if ctx.Err() != nil || r.ctx.Err() != nil {
			t.cancel()
			callErr = context.Canceled
			return
		}
		// 共享并发不足时由受管队列等待，不重复调用模型，也不把同步接口改成业务异步接口。
		for {
			err := r.updateBudget(ctx, t.id, "run", minTaskTime(time.Now().Add(r.cfg.LeaseDuration), deadlineFromContext(ctx)))
			if err == nil {
				break
			}
			var budgetErr *TaskBudgetError
			if !errors.As(err, &budgetErr) {
				callErr = err
				return
			}
			select {
			case <-ctx.Done():
				callErr = ctx.Err()
				return
			case <-time.After(r.cfg.PollInterval):
			}
		}
		// 此 CAS 是停止屏障的执行准入点；受理后才落库的旧代次不能越过它。
		if err := r.checkEpoch(ctx, t.scope, t.epoch, true); err != nil {
			t.cancel()
			callErr = err
			return
		}
		if err := r.ChargeGoal(ctx, TaskGoalCharge{ToolCalls: 1}); err != nil {
			callErr = err
			return
		}
		op, stop := r.operation()
		rec, err := r.ownedUpdate(op, t, func(rec *TaskRecord) {
			if !rec.CancelRequested {
				rec.Started = true
				rec.Info.State = "running"
			}
		})
		stop()
		if err != nil {
			callErr = err
			return
		}
		// 运行标记可能已落库、应答却晚于关闭；不能只依赖AfterFunc异步取消传播。
		// 保留已持久的开始标记做保守核对，但关闭后不再进入Handler。
		started = rec.Started
		if rec.CancelRequested || ctx.Err() != nil || r.ctx.Err() != nil {
			t.cancel()
			callErr = context.Canceled
			return
		}
		r.observeRecord(ctx, rec, "task_started")
		r.signal()
		callCtx := context.WithValue(ctx, taskTraceContextKey{}, rec.Info.TraceID)
		if len(r.cfg.Budgets) > 0 || tool.ResolveResources != nil || len(r.cfg.QoS.Policies) > 0 {
			callCtx, err = r.resourceContext(callCtx, t.id)
			if err != nil {
				callErr = err
				return
			}
		}
		if !tool.Contract.ResultInputs {
			callCtx = context.WithValue(callCtx, taskResultReaderKey{}, TaskResultInputReader(nil))
		}
		result, callErr = tool.Handler(context.WithValue(callCtx, backgroundInvocationKey{}, taskInvocation{ID: t.id, Background: t.background}), args)
	}()
	// 输出不合约不能否认 Handler 已返回；保存原文和独立验证失败，不再执行 Handler。
	validationErr := validateToolOutput(outputSchema, result)
	// Handler 只调用一次；后续阶段只重试不可变回执提交和账本补写。
	outcome := r.makeResult(t, tool, result, callErr, started, ctx.Err() != nil || r.ctx.Err() != nil, taskToolDigest(tool, args))
	if callErr == nil && validationErr != nil {
		outcome.Failure = TaskFailureFrom(validationErr, "handler_returned", true, t.id, resultRef(outcome))
	}
	rec, saveErr := r.persistTaskResult(t, outcome)
	close(renewStop)
	<-renewDone
	if saveErr != nil {
		op, stop := r.operation()
		rec, _ = r.cfg.Store.GetTask(op, t.scope, t.id)
		rec, _ = r.refresh(op, rec)
		stop()
	}
	// 名额释放先于本地Done，顺序调用不会因前项已完成却尚占共享额度而被误拒。
	r.releaseBudget(t.id)
	t.unlink()
	retire := func() { r.mu.Lock(); delete(r.tasks, t.id); close(t.done); r.signalLocked(); r.mu.Unlock() }
	if saveErr == nil {
		r.observeRecord(ctx, rec, "task_finished")
		// 完成channel先入队再关闭Done，回调仍在Done之后，避免Close漏通知且不改变回调等待约束。
		r.notify(rec.Info, retire)
	} else {
		retire()
	}
}
func (r *TaskRuntime) notify(info TaskInfo, afterQueued ...func()) {
	info.Result = ""
	info.Error = ""
	info.Input = nil
	r.mu.Lock()
	closed := r.closed
	if !closed {
		select {
		case r.completed <- info:
		default:
		}
	}
	r.mu.Unlock()
	for _, after := range afterQueued {
		after()
	}
	if !closed {
		r.enqueueCallback(info)
	}
}

func (r *TaskRuntime) signal()       { r.mu.Lock(); r.signalLocked(); r.mu.Unlock() }
func (r *TaskRuntime) signalLocked() { close(r.changed); r.changed = make(chan struct{}) }

// refresh 只恢复过期租约的记录，不执行 Handler；结果未知的写调用必须查询原回执。
func (r *TaskRuntime) refresh(ctx context.Context, rec TaskRecord) (TaskRecord, error) {
	if err := TaskProtocolVersion(rec.StorageVersion); err != nil {
		return rec, err
	}
	if rec.ID == "" {
		return rec, ErrTaskNotFound
	}
	// 已持久的不可变回执优先于租约推断；覆盖“正文已提交，账本尚未补写”的崩溃窗口。
	if rec.Info.ResultRef == nil {
		if outcome, err := r.cfg.Results.GetResult(ctx, rec.Scope, rec.ID); err == nil {
			return r.commitTaskResult(ctx, rec.Scope, rec.ID, outcome)
		} else if !errors.Is(err, ErrTaskNotFound) {
			return rec, err
		}
	}
	if taskTerminal(rec.Info.State) || rec.LeaseUntil.After(time.Now()) {
		return rec, nil
	}
	current, err := r.cfg.Store.GetTask(ctx, rec.Scope, rec.ID)
	if err != nil {
		return rec, err
	}
	rec = current
	if taskTerminal(rec.Info.State) || rec.LeaseUntil.After(time.Now()) {
		return rec, nil
	}
	rev := rec.Revision
	rec.Info.State = "cancelled"
	if rec.Started {
		rec.Info.State = "failed"
		if rec.Effect != ToolEffectRead {
			rec.Info.State = "unknown"
		}
	}
	rec.Info.Error = "执行租约已失效；请核对原业务回执，不自动重放。"
	execution := "not_started"
	if rec.Started {
		execution = "unknown" // 失联只读同样不能证明 Handler 已返回。
	}
	if rec.Info.State == "unknown" {
		execution = "unknown"
	}
	rec.Info.Failure = TaskFailureFrom(context.DeadlineExceeded, execution, true, rec.ID, rec.Info.ResultRef)
	rec.Revision++
	rec.UpdatedAt = time.Now()
	rec.ResultExpiresAt = time.Now().Add(r.cfg.ResultRetention)
	rec.ExpiresAt = time.Now().Add(r.cfg.LedgerRetention)
	rec.LeaseUntil = time.Time{}
	ok, err := r.cfg.Store.ReplaceTask(ctx, rec, rev)
	if err != nil {
		return rec, err
	}
	if !ok {
		return r.cfg.Store.GetTask(ctx, rec.Scope, rec.ID)
	}
	r.signal()
	r.observeRecord(ctx, rec, "task_lease_expired")
	r.notify(rec.Info)
	return rec, nil
}

// Collect 先将失联调用转成可核对的终态，再执行宿主存储的分层保留策略。
func (r *TaskRuntime) Collect(ctx context.Context) error {
	if collector, ok := r.cfg.ControlStore.(interface {
		CollectControls(context.Context, time.Time) error
	}); ok {
		if err := collector.CollectControls(ctx, time.Now().UTC()); err != nil {
			return err
		}
	}
	if err := r.cfg.Contexts.CollectContexts(ctx, time.Now().UTC()); err != nil {
		return err
	}
	if err := r.reconcileBudget(ctx); err != nil {
		return err
	}
	after := ""
	for {
		rows, err := r.cfg.Store.ListTasks(ctx, "", TaskScan{After: after, Limit: 128, ActiveOnly: true})
		if err != nil {
			return err
		}
		for _, rec := range rows {
			if _, err = r.refresh(ctx, rec); err != nil {
				return err
			}
		}
		if len(rows) < 128 {
			break
		}
		after = rows[len(rows)-1].ID
	}
	now := time.Now().UTC()
	if err := r.cfg.Events.CollectEvents(ctx, now); err != nil {
		return err
	}
	if err := r.cfg.Results.CollectResults(ctx, now); err != nil {
		return err
	}
	if err := r.cfg.Store.Collect(ctx, now); err != nil {
		return err
	}
	if r.cfg.Outbox != nil {
		if _, err := DeliverTaskOutbox(ctx, r.cfg.Outbox, r.EventPublisher(), r.owner); err != nil {
			return err
		}
	}
	return nil
}
func (r *TaskRuntime) maintain() {
	defer close(r.maintenanceDone)
	interval := time.Minute
	if r.cfg.Outbox != nil {
		interval = r.cfg.PollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	lastCollect := time.Now()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			ctx, stop := r.operation()
			if r.cfg.Outbox != nil {
				_, err := DeliverTaskOutbox(ctx, r.cfg.Outbox, r.EventPublisher(), r.owner)
				if err != nil && r.cfg.Scope != "" {
					_ = r.RecordObservation(ctx, r.cfg.Scope, TaskObservation{Kind: "outbox_delivery", State: "pending", Code: TaskFailureFrom(err, "not_started", false, "", nil).Code})
				}
			}
			if time.Since(lastCollect) >= time.Minute {
				_ = r.Collect(ctx)
				lastCollect = time.Now()
			}
			stop()
		}
	}
}

// EndTurn 正常结束仅解除轮次绑定；错误/停止取消原轮在途调用，不删除账本。
func (r *TaskRuntime) EndTurn(scope, turn string, keep bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range r.tasks {
		if t.scope == scope && t.turn == turn {
			t.unlink()
			if !keep {
				t.cancel()
			}
		}
	}
}
func taskTerminal(state string) bool {
	return state != "queued" && state != "running" && state != "cancelling" && state != "waiting" && state != "delivering"
}

// Read 从持久账本读取，调用方 scope 和当前工具权限必须同时匹配。
func (r *TaskRuntime) Read(scope string, ids []string, allowed map[string]bool) ([]TaskInfo, error) {
	if scope == "" || r.cfg.Scope != "" && r.cfg.Scope != scope {
		return nil, ErrTaskNotFound
	}
	op, stop := r.operation()
	defer stop()
	out := []TaskInfo{}
	if len(ids) == 0 {
		after := ""
		for {
			rows, err := r.cfg.Store.ListTasks(op, scope, TaskScan{After: after, Limit: 128})
			if err != nil {
				return nil, err
			}
			for _, rec := range rows {
				if !allowed[rec.Info.Tool] {
					continue
				}
				rec, err = r.refresh(op, rec)
				if err != nil {
					return nil, err
				}
				info := r.visible(rec)
				info.Result = ""
				info.Error = ""
				out = append(out, info)
			}
			if len(rows) < 128 {
				break
			}
			after = rows[len(rows)-1].ID

		}
		return out, nil
	}
	if len(ids) > 8 {
		return nil, errors.New("agent: at most eight task handles per read")
	}
	for _, id := range ids {
		rec, err := r.cfg.Store.GetTask(op, scope, id)
		if errors.Is(err, ErrTaskNotFound) && allowed["task_suspend"] {
			w, e := r.cfg.Store.GetWait(op, scope, id)
			if e != nil {
				return nil, e
			}
			out = append(out, waitTaskInfo(w))
			continue
		}
		if err != nil {
			return nil, err
		}
		if !allowed[rec.Info.Tool] {
			return nil, ErrTaskNotFound
		}
		rec, err = r.refresh(op, rec)
		if err != nil {
			return nil, err
		}
		info := r.visible(rec)
		info.Input = append(json.RawMessage(nil), rec.Args...)
		out = append(out, info)
	}
	return out, nil
}

// Wait 通过本地事件与跨实例查询唤醒；等待超时不取消原调用。
func (r *TaskRuntime) Wait(ctx context.Context, scope string, ids []string, allowed map[string]bool, timeout time.Duration, all bool) ([]TaskInfo, error) {
	if len(ids) == 0 || timeout < 0 || timeout > 30*time.Second {
		return nil, errors.New("agent: invalid task wait")
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(r.cfg.PollInterval)
	defer ticker.Stop()
	for {
		r.mu.Lock()
		changed := r.changed
		r.mu.Unlock()
		rows, err := r.Read(scope, ids, allowed)
		if err != nil {
			return nil, err
		}
		finished := 0
		for _, row := range rows {
			if taskTerminal(row.State) {
				finished++
			}
		}
		if finished == len(rows) || !all && finished > 0 {
			// 账本终态先落地，本地执行名额随后释放；同步等待同时等待本地真实收尾。
			for _, row := range rows {
				if !taskTerminal(row.State) {
					continue
				}
				r.mu.Lock()
				local := r.tasks[row.ID]
				r.mu.Unlock()
				if local != nil {
					select {
					case <-local.done:
					case <-ctx.Done():
						return nil, ctx.Err()
					case <-timer.C:
						// 回执已可见而原执行器仍在补写收尾时，短等期限仍须生效。
						return rows, nil
					}
				}
			}
			return rows, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-r.ctx.Done():
			return nil, r.ctx.Err()
		case <-timer.C:
			return r.Read(scope, ids, allowed)
		case <-changed:
		case <-ticker.C:
		}
	}
}

// Cancel 保存取消意图；远程执行器经租约心跳响应，不承诺业务回滚。
func (r *TaskRuntime) Cancel(scope, id string, allowed map[string]bool) (TaskInfo, error) {
	if scope == "" || r.cfg.Scope != "" && r.cfg.Scope != scope {
		return TaskInfo{}, ErrTaskNotFound
	}
	op, stop := r.operation()
	defer stop()
	return r.cancelInvocation(op, scope, id, allowed, false)
}

// cancelInvocation 统一保存取消意图；host 仅由已绑定作用域的宿主停止入口使用，不接受模型授权。
func (r *TaskRuntime) cancelInvocation(ctx context.Context, scope, id string, allowed map[string]bool, host bool) (TaskInfo, error) {
	for range 8 {
		rec, err := r.cfg.Store.GetTask(ctx, scope, id)
		if errors.Is(err, ErrTaskNotFound) && !host && allowed["task_suspend"] {
			return r.cancelWait(ctx, scope, id)
		}
		if err != nil {
			return TaskInfo{}, err
		}
		if !host && !allowed[rec.Info.Tool] {
			return TaskInfo{}, ErrTaskNotFound
		}
		if taskTerminal(rec.Info.State) {
			return r.visible(rec), nil
		}
		rev := rec.Revision
		rec.CancelRequested = true
		rec.Info.State = "cancelling"
		rec.Revision++
		rec.UpdatedAt = time.Now()
		ok, err := r.cfg.Store.ReplaceTask(ctx, rec, rev)
		if err != nil {
			return TaskInfo{}, err
		}
		if !ok {
			continue
		}
		r.mu.Lock()
		if t := r.tasks[id]; t != nil {
			t.cancel()
		}
		r.signalLocked()
		r.mu.Unlock()
		r.observeRecord(ctx, rec, "task_cancel_requested")
		return rec.Info, nil
	}
	return TaskInfo{}, ErrTaskConflict
}

// CancelAll 先立即停止本地工作，再持久取消本作用域的活跃调用和等待；远程执行器经心跳响应。
func (r *TaskRuntime) CancelAll() error {
	r.mu.Lock()
	for _, t := range r.tasks {
		t.cancel()
	}
	scopes := []string{}
	if r.cfg.Scope != "" {
		scopes = append(scopes, r.cfg.Scope)
	} else {
		for scope := range r.seenScopes {
			scopes = append(scopes, scope)
		}
	}
	r.signalLocked()
	r.mu.Unlock()
	ctx, stop := r.operation()
	defer stop()
	for _, scope := range scopes {
		// 先持久推进代次，再扫描旧账本；晚到的旧受理即使不在本次扫描中也无法开始执行。
		if err := r.stopScope(ctx, scope); err != nil {
			return err
		}
		_ = r.RecordObservation(ctx, scope, TaskObservation{Kind: "scope_stopped", State: "stopped"})
		after := ""
		for {
			rows, err := r.cfg.Store.ListTasks(ctx, scope, TaskScan{After: after, Limit: 128, ActiveOnly: true})
			if err != nil {
				return err
			}
			for _, rec := range rows {
				if _, err = r.cancelInvocation(ctx, scope, rec.ID, nil, true); err != nil {
					return err
				}
			}
			if len(rows) < 128 {
				break
			}
			after = rows[len(rows)-1].ID
		}
		if err := r.CancelWaits(ctx, scope); err != nil {
			return err
		}
	}
	return nil
}

// Done 本地句柄在执行器退出后关闭，远程句柄在观察到账本终态后关闭，不重建执行工作。
// 本地退出不保证最终回执已持久成功；需要可核对终态/观察错误时使用带 context 的 Wait。
func (r *TaskRuntime) Done(scope, id string, allowed map[string]bool) (<-chan struct{}, error) {
	rows, err := r.Read(scope, []string{id}, allowed)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	if t := r.tasks[id]; t != nil {
		done := t.done
		r.mu.Unlock()
		return done, nil
	}
	r.mu.Unlock()
	done := make(chan struct{})
	if taskTerminal(rows[0].State) {
		close(done)
		return done, nil
	}
	go func() {
		for {
			rows, err := r.Wait(r.ctx, scope, []string{id}, allowed, 30*time.Second, true)
			if err != nil {
				return
			}
			if taskTerminal(rows[0].State) {
				close(done)
				return
			}
		}
	}()
	return done, nil
}

// Completed 是有界本地通知提示；可靠状态/唤醒以持久账本为准，慢消费者不会阻塞工作收尾。
func (r *TaskRuntime) Completed() <-chan TaskInfo { return r.completed }

// Close 取消并等待真实退出，账本和待恢复订阅独立于运行时保留。
func (r *TaskRuntime) Close() { _ = r.Shutdown(context.Background()) }

// TaskScope 将可信会话与权限快照绑定为稳定的账本作用域。
func TaskScope(req *TurnRequest) string {
	return taskHash([]string{req.SessionID, req.SessionCapability})
}

type backgroundInvocationKey struct{}

// IsBackgroundTask 区分显式后台方式和同步记账，不因内部协程改变模型选择。
func IsBackgroundTask(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(backgroundInvocationKey{}).(taskInvocation)
	return v.Background
}

// TaskID 读取运行时附加的调用句柄，业务 JSON 不能冒充该标记。
func TaskID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(backgroundInvocationKey{}).(taskInvocation)
	return value.ID
}

// Call 让同步与后台调用共用账本。已完成/未知的同一调用只返回原事实，绝不再调 Handler。
func (r *TaskRuntime) Call(ctx context.Context, scope, turn, key string, tool Tool, args json.RawMessage) (string, error) {
	info, err := r.Start(context.WithValue(ctx, synchronousInvocationKey{}, true), scope, turn, key, tool, args)
	if err != nil {
		return "", TaskFailureFrom(err, "not_started", false, "", nil)
	}
	for {
		rows, err := r.Wait(ctx, scope, []string{info.ID}, map[string]bool{tool.Name: true}, 30*time.Second, true)
		if err != nil {
			return "", TaskStorageFailure(err, "unknown", true, info.ID, info.ResultRef)
		}
		info = rows[0]
		if !taskTerminal(info.State) {
			continue
		}
		if info.State == "completed" {
			if info.Failure != nil {
				return "", info.Failure
			}
			if info.ResultExpired {
				raw, _ := json.Marshal(map[string]any{"status": "result_expired", "execution": "handler_returned", "task_id": info.ID, "message": "原调用已返回，正文已按保留策略回收；请查询原业务回执，不要重放。"})
				return string(raw), nil
			}
			if info.ResultRef != nil && (info.ResultRef.Size > int64(r.cfg.MaxResultBytes) || info.ResultRef.Availability != "available" || info.ResultRef.Encoding != "utf-8") {
				return resultResponse(info), nil
			}
			return info.Result, nil
		}
		if info.Failure != nil {
			return "", info.Failure
		}
		return "", TaskFailureFrom(errors.New("original invocation failed"), "unknown", true, info.ID, info.ResultRef)
	}
}

type synchronousInvocationKey struct{}
type taskInvocation struct {
	ID         string
	Background bool
}

// TaskPage 显式提供游标，避免账本增长后把部分元数据当成完整清单。
type TaskPage struct {
	Tasks    []TaskInfo       `json:"tasks"`
	Next     string           `json:"next,omitempty"`
	Waits    []TaskWaitRecord `json:"waits,omitempty"`
	WaitNext string           `json:"wait_next,omitempty"`
}

func (r *TaskRuntime) List(ctx context.Context, scope, after string, limit int, allowed map[string]bool) (TaskPage, error) {
	if scope == "" || r.cfg.Scope != "" && r.cfg.Scope != scope {
		return TaskPage{}, ErrTaskNotFound
	}
	if limit < 1 || limit > 128 {
		return TaskPage{}, errors.New("agent: invalid task page")
	}
	records, err := r.cfg.Store.ListTasks(ctx, scope, TaskScan{After: after, Limit: limit})
	if err != nil {
		return TaskPage{}, err
	}
	page := TaskPage{Tasks: []TaskInfo{}}
	for _, rec := range records {
		if !allowed[rec.Info.Tool] {
			continue
		}
		rec, err = r.refresh(ctx, rec)
		if err != nil {
			return TaskPage{}, err
		}
		info := r.visible(rec)
		info.Result = ""
		info.Error = ""
		page.Tasks = append(page.Tasks, info)
	}
	if len(records) == limit {
		page.Next = records[len(records)-1].ID
	}
	return page, nil
}

func waitTaskInfo(w TaskWaitRecord) TaskInfo {
	raw, _ := json.Marshal(w.Spec)
	return TaskInfo{ID: w.ID, Tool: "task_suspend", State: w.State, Completion: ToolCompletionAsynchronous, Input: raw, Result: w.Reason}
}
func (r *TaskRuntime) cancelWait(ctx context.Context, scope, id string) (TaskInfo, error) {
	for range 8 {
		w, err := r.cfg.Store.GetWait(ctx, scope, id)
		if err != nil {
			return TaskInfo{}, err
		}
		if w.State != "waiting" && w.State != "delivering" {
			return waitTaskInfo(w), nil
		}
		rev := w.Revision
		w.State = "cancelled"
		w.LeaseUntil = time.Time{}
		w.Revision++
		ok, err := r.cfg.Store.ReplaceWait(ctx, w, rev)
		if err != nil {
			return TaskInfo{}, err
		}
		if ok {
			r.signal()
			r.releaseBudget(w.ID)
			r.observeWait(ctx, w, "wait_cancelled", "")
			return waitTaskInfo(w), nil
		}
	}
	return TaskInfo{}, ErrTaskConflict
}

// ListWaitPage 只返回当前作用域的活跃等待，独立游标不混淆调用记录与事件登记。
func (r *TaskRuntime) ListWaitPage(ctx context.Context, scope, after string, limit int) ([]TaskWaitRecord, string, error) {
	if scope == "" || r.cfg.Scope != "" && r.cfg.Scope != scope || limit < 1 || limit > 128 {
		return nil, "", ErrTaskNotFound
	}
	rows, err := r.cfg.Store.ListWaits(ctx, scope, TaskScan{After: after, Limit: limit, ActiveOnly: true})
	next := ""
	if len(rows) == limit {
		next = rows[len(rows)-1].ID
	}
	return rows, next, err
}
