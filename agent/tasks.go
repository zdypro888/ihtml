package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

// TaskConfig 约束资源与记录保留，不决定业务步骤。Store 必须由宿主绑定可信命名空间。
type TaskConfig struct {
	MaxConcurrent, MaxTasks, MaxResultBytes, MaxWakeAttempts, MaxWakeDepth int
	Timeout, LeaseDuration, ResultRetention, LedgerRetention, PollInterval time.Duration
	Store                                                                  TaskStore
	Scope                                                                  string
	OnComplete                                                             func(TaskInfo)
}

// TaskInfo 的 completed 表示 Handler 已返回，不能代替原异步业务的完成状态。
type TaskInfo struct {
	ID         string         `json:"id" bson:"id"`
	ParentID   string         `json:"parent_id,omitempty" bson:"parentID,omitempty"`
	Tool       string         `json:"tool" bson:"tool"`
	State      string         `json:"state" bson:"state"`
	Completion ToolCompletion `json:"completion,omitempty" bson:"completion,omitempty"`
	// Background 是调用方选择的执行方式，与原业务是否异步完成无关。
	Background    bool            `json:"background" bson:"background"`
	Result        string          `json:"result,omitempty" bson:"result,omitempty"`
	Error         string          `json:"error,omitempty" bson:"error,omitempty"`
	Input         json.RawMessage `json:"arguments,omitempty" bson:"-"`
	ResultExpired bool            `json:"result_expired,omitempty" bson:"resultExpired,omitempty"`
}

type toolTask struct {
	scope, turn, id string
	cancel          context.CancelFunc
	unlink          func() bool
	done            chan struct{}
	background      bool
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
}

// NewTaskRuntime 默认使用内存账本；宿主接入持久 TaskStore 后具备跨实例/重启记录恢复能力。
func NewTaskRuntime(ctx context.Context, cfg TaskConfig) (*TaskRuntime, error) {
	if ctx == nil || ctx.Err() != nil || cfg.MaxConcurrent < 0 || cfg.MaxConcurrent > 256 || cfg.MaxTasks < 0 || cfg.MaxTasks > 4096 || cfg.MaxResultBytes < 0 || cfg.MaxResultBytes > 1<<20 || cfg.Timeout < 0 || cfg.Timeout > 24*time.Hour || cfg.LeaseDuration < 0 || cfg.ResultRetention < 0 || cfg.LedgerRetention < 0 || cfg.PollInterval < 0 {
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
	if cfg.MaxConcurrent == 0 {
		cfg.MaxConcurrent = 8
	}
	if cfg.MaxTasks == 0 {
		cfg.MaxTasks = 128
	}
	if cfg.MaxResultBytes == 0 {
		cfg.MaxResultBytes = 64 << 10
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
	ctx, cancel := context.WithCancel(ctx)
	r := &TaskRuntime{cfg: cfg, ctx: ctx, cancel: cancel, owner: rand.Text(), tasks: map[string]*toolTask{}, seenScopes: map[string]bool{}, slots: make(chan struct{}, cfg.MaxConcurrent), changed: make(chan struct{}), completed: make(chan TaskInfo, cfg.MaxTasks), maintenanceDone: make(chan struct{})}
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
	if ctx == nil || ctx.Err() != nil || scope == "" || r.cfg.Scope != "" && r.cfg.Scope != scope || turn == "" || key == "" || tool.Name == "" || tool.Handler == nil || len(args) > 64<<10 || !json.Valid(args) {
		return TaskInfo{}, errors.New("agent: invalid task invocation")
	}
	r.mu.Lock()
	r.seenScopes[scope] = true
	r.mu.Unlock()
	args = append([]byte(nil), args...)
	compound := taskHash([]string{scope, turn, key})
	digest := taskHash([]any{tool.Name, json.RawMessage(args)})
	op, stop := r.operation()
	defer stop()
	if old, err := r.cfg.Store.FindTask(op, scope, compound); err == nil {
		if old.Digest != digest {
			return TaskInfo{}, errors.New("agent: task invocation changed")
		}
		old, err = r.refresh(op, old)
		return r.visible(old), err
	} else if !errors.Is(err, ErrTaskNotFound) {
		return TaskInfo{}, err
	}
	r.mu.Lock()
	if r.closed || r.ctx.Err() != nil {
		r.mu.Unlock()
		return TaskInfo{}, errors.New("agent: task runtime closed")
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
	deadline := now.Add(r.cfg.Timeout)
	synchronous, _ := ctx.Value(synchronousInvocationKey{}).(bool)
	record := TaskRecord{ID: id, Scope: scope, Key: compound, Digest: digest, Turn: turn, Args: args, Invocation: ToolInvocationKey(ctx), Effect: tool.Effect, Info: TaskInfo{ID: id, ParentID: TaskID(ctx), Tool: tool.Name, State: "queued", Completion: tool.Completion, Background: !synchronous}, Owner: r.owner, Revision: 1, CreatedAt: now, UpdatedAt: now, Deadline: deadline, LeaseUntil: minTaskTime(now.Add(r.cfg.LeaseDuration), deadline)}
	saved, created, err := r.cfg.Store.CreateTask(op, record)
	if err != nil {
		return TaskInfo{}, err
	}
	if !created {
		if saved.Digest != digest {
			return TaskInfo{}, errors.New("agent: task invocation changed")
		}
		return r.visible(saved), nil
	}
	taskCtx, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
	unlinkRoot := context.AfterFunc(r.ctx, cancel)
	t := &toolTask{scope: scope, turn: turn, id: id, cancel: cancel, done: make(chan struct{}), background: !synchronous}
	t.unlink = context.AfterFunc(ctx, cancel)
	r.mu.Lock()
	if r.closed {
		cancel()
	}
	r.tasks[id] = t
	r.mu.Unlock()
	launched = true
	go r.run(taskCtx, t, tool, args, unlinkRoot)
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
	if !rec.ResultExpiresAt.IsZero() && !rec.ResultExpiresAt.After(time.Now()) {
		info.Result = ""
		info.ResultExpired = true
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
func (r *TaskRuntime) run(ctx context.Context, t *toolTask, tool Tool, args json.RawMessage, unlinkRoot func() bool) {
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
		if ctx.Err() != nil {
			callErr = ctx.Err()
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
		if rec.CancelRequested {
			callErr = context.Canceled
			return
		}
		started = true
		r.signal()
		result, callErr = tool.Handler(context.WithValue(ctx, backgroundInvocationKey{}, taskInvocation{ID: t.id, Background: t.background}), args)
	}()
	close(renewStop)
	<-renewDone
	op, stop := r.operation()
	rec, saveErr := r.ownedUpdate(op, t, func(rec *TaskRecord) {
		rec.Info.State = "completed"
		if callErr != nil {
			rec.Info.State = "failed"
			if ctx.Err() != nil {
				rec.Info.State = "cancelled"
			}
			if started && tool.Effect != ToolEffectRead {
				rec.Info.State = "unknown"
			}
		}
		if len(result) > r.cfg.MaxResultBytes {
			result = ""
			rec.Info.State = "failed"
			if started && tool.Effect != ToolEffectRead {
				rec.Info.State = "unknown"
			}
			callErr = errors.New("result exceeds limit")
		}
		rec.Info.Result = result
		if callErr != nil {
			rec.Info.Error = "工具调用未取得完整结果；请查询原业务记录，不自动重放。"
		}
		rec.ResultExpiresAt = time.Now().Add(r.cfg.ResultRetention)
		rec.ExpiresAt = time.Now().Add(r.cfg.LedgerRetention)
		rec.LeaseUntil = time.Time{}
	})
	stop()
	if saveErr != nil {
		op, stop = r.operation()
		rec, _ = r.cfg.Store.GetTask(op, t.scope, t.id)
		rec, _ = r.refresh(op, rec)
		stop()
	}
	t.unlink()
	r.mu.Lock()
	delete(r.tasks, t.id)
	close(t.done)
	r.signalLocked()
	r.mu.Unlock()
	if saveErr == nil {
		r.notify(rec.Info)
	}
}
func (r *TaskRuntime) notify(info TaskInfo) {
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
	if !closed && r.cfg.OnComplete != nil {
		func() { defer func() { _ = recover() }(); r.cfg.OnComplete(info) }()
	}
}

func (r *TaskRuntime) signal()       { r.mu.Lock(); r.signalLocked(); r.mu.Unlock() }
func (r *TaskRuntime) signalLocked() { close(r.changed); r.changed = make(chan struct{}) }

// refresh 只恢复过期租约的记录，不执行 Handler；结果未知的写调用必须查询原回执。
func (r *TaskRuntime) refresh(ctx context.Context, rec TaskRecord) (TaskRecord, error) {
	if rec.ID == "" {
		return rec, ErrTaskNotFound
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
	r.notify(rec.Info)
	return rec, nil
}

// Collect 先将失联调用转成可核对的终态，再执行宿主存储的分层保留策略。
func (r *TaskRuntime) Collect(ctx context.Context) error {
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
	return r.cfg.Store.Collect(ctx, time.Now().UTC())
}
func (r *TaskRuntime) maintain() {
	defer close(r.maintenanceDone)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			ctx, stop := r.operation()
			_ = r.Collect(ctx)
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
func (r *TaskRuntime) Close() {
	r.mu.Lock()
	r.closed = true
	r.cancel()
	r.mu.Unlock()
	r.wg.Wait()
	<-r.maintenanceDone
	r.closeOnce.Do(func() { close(r.completed) })
}

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
		return "", err
	}
	for {
		rows, err := r.Wait(ctx, scope, []string{info.ID}, map[string]bool{tool.Name: true}, 30*time.Second, true)
		if err != nil {
			return "", err
		}
		info = rows[0]
		if !taskTerminal(info.State) {
			continue
		}
		if info.State == "completed" {
			if info.ResultExpired {
				raw, _ := json.Marshal(map[string]any{"status": "result_expired", "execution": "handler_returned", "task_id": info.ID, "message": "原调用已返回，正文已按保留策略回收；请查询原业务回执，不要重放。"})
				return string(raw), nil
			}
			return info.Result, nil
		}
		return "", errors.New("agent: original invocation is " + info.State + "; inspect its receipt, do not replay")
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
