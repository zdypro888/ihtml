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

// TaskConfig 只约束执行资源，不决定业务调用顺序。完成事件不携带参数或结果。
type TaskConfig struct {
	MaxConcurrent  int
	MaxTasks       int
	MaxResultBytes int
	Timeout        time.Duration
	OnComplete     func(TaskInfo)
}

// TaskInfo 描述一次工具调用；completed 只表示 Handler 返回，并非其异步业务已经完成。
type TaskInfo struct {
	ID         string         `json:"id"`
	ParentID   string         `json:"parent_id,omitempty"`
	Tool       string         `json:"tool"`
	State      string         `json:"state"`
	Completion ToolCompletion `json:"completion,omitempty"`
	Result     string         `json:"result,omitempty"`
	Error      string         `json:"error,omitempty"`
}

type toolTask struct {
	info                TaskInfo
	scope, turn, digest string
	cancel              context.CancelFunc
	unlink              func() bool
	done                chan struct{}
	started             bool
}

// TaskRuntime 为一个可信会话提供有界 goroutine、结果缓存和完成通道，不执行模型或重试。
// 宿主负责身份/权限绑定；进程退出不持久化此缓存，业务回执仍由原业务服务保存。
type TaskRuntime struct {
	cfg       TaskConfig
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	tasks     map[string]*toolTask
	keys      map[string]string
	slots     chan struct{}
	changed   chan struct{}
	completed chan TaskInfo
	wg        sync.WaitGroup
	closed    bool
	closeOnce sync.Once
}

// NewTaskRuntime 创建归属于宿主可信生命周期的有界执行器；零配置使用保守的资源上限。
func NewTaskRuntime(ctx context.Context, cfg TaskConfig) (*TaskRuntime, error) {
	if ctx == nil || ctx.Err() != nil || cfg.MaxConcurrent < 0 || cfg.MaxConcurrent > 256 || cfg.MaxTasks < 0 || cfg.MaxTasks > 4096 || cfg.MaxResultBytes < 0 || cfg.MaxResultBytes > 1<<20 || cfg.Timeout < 0 || cfg.Timeout > 24*time.Hour {
		return nil, errors.New("agent: invalid task configuration")
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
	ctx, cancel := context.WithCancel(ctx)
	return &TaskRuntime{cfg: cfg, ctx: ctx, cancel: cancel, tasks: map[string]*toolTask{}, keys: map[string]string{}, slots: make(chan struct{}, cfg.MaxConcurrent), changed: make(chan struct{}), completed: make(chan TaskInfo, cfg.MaxTasks)}, nil
}

// Start 冻结原调用并立即返回句柄；invoke 应是已通过宿主治理的原 Handler。
// key 由可信运行时产生，同键不同调用拒绝执行；原上下文取消在 EndTurn 前仍会传递。
func (r *TaskRuntime) Start(ctx context.Context, scope, turn, key string, tool Tool, args json.RawMessage) (TaskInfo, error) {
	if ctx == nil || ctx.Err() != nil || scope == "" || turn == "" || key == "" || tool.Name == "" || tool.Handler == nil || len(args) > 64<<10 || !json.Valid(args) {
		return TaskInfo{}, errors.New("agent: invalid task invocation")
	}
	args = append(json.RawMessage(nil), args...)
	digestBytes := sha256.Sum256(append([]byte(tool.Name+"\x00"), args...))
	digest := hex.EncodeToString(digestBytes[:])
	compound := scope + "\x00" + turn + "\x00" + key
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.ctx.Err() != nil {
		return TaskInfo{}, errors.New("agent: task runtime closed")
	}
	if id := r.keys[compound]; id != "" {
		task := r.tasks[id]
		if task.digest != digest {
			return TaskInfo{}, errors.New("agent: task invocation changed")
		}
		return task.info, nil
	}
	if len(r.tasks) >= r.cfg.MaxTasks {
		return TaskInfo{}, errors.New("agent: task capacity reached")
	}
	var id [16]byte
	_, _ = rand.Read(id[:])
	// 保留宿主值和调用身份，生命周期由会话、执行期限和当前轮次共同约束。
	taskCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.cfg.Timeout)
	unlinkRoot := context.AfterFunc(r.ctx, cancel)
	task := &toolTask{info: TaskInfo{ID: hex.EncodeToString(id[:]), Tool: tool.Name, ParentID: TaskID(ctx), State: "queued", Completion: tool.Completion}, scope: scope, turn: turn, digest: digest, cancel: cancel, done: make(chan struct{})}
	task.unlink = context.AfterFunc(ctx, cancel)
	r.tasks[task.info.ID] = task
	r.keys[compound] = task.info.ID
	r.wg.Add(1)
	go r.run(taskCtx, task, tool, args, unlinkRoot)
	return task.info, nil
}

func (r *TaskRuntime) run(ctx context.Context, t *toolTask, tool Tool, args json.RawMessage, unlinkRoot func() bool) {
	defer r.wg.Done()
	defer t.cancel()
	defer unlinkRoot()
	result, callErr := "", error(nil)
	func() {
		defer func() {
			if recover() != nil {
				callErr = errors.New("agent: background handler panicked")
			}
		}()
		select {
		case r.slots <- struct{}{}:
			defer func() { <-r.slots }()
		case <-ctx.Done():
			callErr = ctx.Err()
			return
		}
		r.mu.Lock()
		if ctx.Err() != nil {
			r.mu.Unlock()
			callErr = ctx.Err()
			return
		}
		t.started = true
		t.info.State = "running"
		r.signalLocked()
		r.mu.Unlock()
		result, callErr = tool.Handler(context.WithValue(ctx, backgroundInvocationKey{}, t.info.ID), args)
	}()
	r.mu.Lock()
	state := "completed"
	if callErr != nil {
		state = "failed"
		if ctx.Err() != nil {
			state = "cancelled"
		}
		if t.started && tool.Effect != ToolEffectRead {
			state = "unknown"
		}
	}
	if len(result) > r.cfg.MaxResultBytes {
		result = ""
		state = "failed"
		if t.started && tool.Effect != ToolEffectRead {
			state = "unknown"
		}
		callErr = errors.New("agent: task result exceeds limit")
	}
	t.info.State = state
	t.info.Result = result
	// 普通 error 可能包含凭据，仅缓存固定错误分类；原结果仍由宿主 Handler 负责脱敏。
	if callErr != nil {
		t.info.Error = "工具调用未取得完整结果；有外部影响时须查询原业务记录，不能自动重放。"
	}
	t.unlink()
	close(t.done)
	r.signalLocked()
	info := t.info
	info.Result = ""
	info.Error = ""
	r.completed <- info
	r.mu.Unlock()
	if r.cfg.OnComplete != nil {
		func() { defer func() { _ = recover() }(); r.cfg.OnComplete(info) }()
	}
}

func (r *TaskRuntime) signalLocked() { close(r.changed); r.changed = make(chan struct{}) }

// EndTurn 在正常返回时解除轮次取消绑定，任务仍受会话和自身期限约束；异常/停止取消原轮任务。
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
	return state != "queued" && state != "running" && state != "cancelling"
}

// Read 不跨 scope，allowed 来自本轮能力目录；未指定 ID 时只列元数据，避免灌入全部结果。
func (r *TaskRuntime) Read(scope string, ids []string, allowed map[string]bool) ([]TaskInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.readLocked(scope, ids, allowed)
}
func (r *TaskRuntime) readLocked(scope string, ids []string, allowed map[string]bool) ([]TaskInfo, error) {
	out := []TaskInfo{}
	if len(ids) == 0 {
		for _, t := range r.tasks {
			if t.scope == scope && allowed[t.info.Tool] {
				info := t.info
				info.Result = ""
				info.Error = ""
				out = append(out, info)
			}
		}
		return out, nil
	}
	if len(ids) > 8 {
		return nil, errors.New("agent: at most eight task handles per read")
	}
	for _, id := range ids {
		t := r.tasks[id]
		if t == nil || t.scope != scope || !allowed[t.info.Tool] {
			return nil, errors.New("agent: task unavailable in this session; query the original business receipt, do not replay effects")
		}
		out = append(out, t.info)
	}
	return out, nil
}

// Wait 使用完成通道唤醒，timeout 只结束本次等待，不取消原任务；all=false 等任意一个结束。
func (r *TaskRuntime) Wait(ctx context.Context, scope string, ids []string, allowed map[string]bool, timeout time.Duration, all bool) ([]TaskInfo, error) {
	if len(ids) == 0 || timeout < 0 || timeout > 30*time.Second {
		return nil, errors.New("agent: invalid task wait")
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		r.mu.Lock()
		rows, err := r.readLocked(scope, ids, allowed)
		changed := r.changed
		r.mu.Unlock()
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
		}
	}
}

// Cancel 只请求协作取消，不宣称业务已回滚；结果须等 Handler 真正返回。
func (r *TaskRuntime) Cancel(scope, id string, allowed map[string]bool) (TaskInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rows, err := r.readLocked(scope, []string{id}, allowed)
	if err != nil {
		return TaskInfo{}, err
	}
	t := r.tasks[id]
	if !taskTerminal(t.info.State) {
		t.info.State = "cancelling"
		t.cancel()
		r.signalLocked()
		return t.info, nil
	}
	return rows[0], nil
}

// CancelAll 请求停止本会话尚未返回的调用，不销毁运行时，后续新轮次仍可启动任务。
func (r *TaskRuntime) CancelAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range r.tasks {
		if !taskTerminal(t.info.State) {
			t.info.State = "cancelling"
			t.cancel()
		}
	}
	r.signalLocked()
}

// Done 返回原调用的完成通道；取消请求不会提前关闭，且不能跨会话取得通道。
func (r *TaskRuntime) Done(scope, id string, allowed map[string]bool) (<-chan struct{}, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.readLocked(scope, []string{id}, allowed); err != nil {
		return nil, err
	}
	return r.tasks[id].done, nil
}

// Completed 每次调用完成产生一条不含参数/结果的通知；通知缓存与 MaxTasks 同界，不丢终态。
func (r *TaskRuntime) Completed() <-chan TaskInfo { return r.completed }

// Close 等待真实退出；Handler 必须响应 context，OnComplete 不得在回调内调用 Close。
func (r *TaskRuntime) Close() {
	r.mu.Lock()
	r.closed = true
	r.cancel()
	r.mu.Unlock()
	r.wg.Wait()
	r.closeOnce.Do(func() { close(r.completed) })
}

// TaskScope 将结果绑定当前可信会话及权限快照；模型不能通过参数选择 scope。
func TaskScope(req *TurnRequest) string {
	raw, _ := json.Marshal([]string{req.SessionID, req.SessionCapability})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

type backgroundInvocationKey struct{}

// IsBackgroundTask 仅识别执行器附加的可信上下文标记，不能由模型参数指定。
// 原工具守卫仍执行，轮次正常结束不再误取消已由会话执行器接纳的排队调用。
func IsBackgroundTask(ctx context.Context) bool {
	return TaskID(ctx) != ""
}

// TaskID 返回可信执行上下文中的调用句柄；嵌套受管调用可保留父子关联。
func TaskID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(backgroundInvocationKey{}).(string)
	return value
}
