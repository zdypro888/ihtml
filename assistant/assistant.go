// Package assistant bridges ihtml's chat protocol to a host-owned agent.Engine.
// It does not authenticate users or persist transcripts. Optional signed manual
// confirmations still require a host-owned live execution guard.
package assistant

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/agent"
	"github.com/zdypro888/ihtml/confirmation"
)

// ToolResultView 是宿主显式脱敏后的显示投影；原始 Handler 返回值默认永不发送到浏览器。
type ToolResultView struct {
	Status  string // 仅允许 success/accepted/error/unknown，描述已完成接口内的业务结果。
	Title   string
	Summary string
	Result  json.RawMessage
}

// Config contains no provider credentials. Hooks must respect cancellation and
// be safe across connections. Engine.RunTurn must stop when its context ends.
type Config struct {
	Engine agent.Engine
	// Tasks 启用本会话的通用后台工具调用；不会改变原工具的授权和业务完成语义。
	// OnComplete/Completed 提供完成通知，回调不得关闭自身会话。
	Tasks *agent.TaskConfig
	// TasksProvider 为当前可信会话提供独立的持久存储/Scope，不接受客户端数据库或命名空间。
	TasksProvider func(context.Context, *ihtml.ChatSessionContext) (agent.TaskConfig, error)
	TaskWakeGuard func(context.Context, *ihtml.ChatSessionContext) error
	// TaskWakeAcknowledged 核对宿主与对话保存同一事务的已完成恢复标记，避免崩溃窗口重复模型轮次。
	TaskWakeAcknowledged func(context.Context, *ihtml.ChatSessionContext, string) (bool, error)
	// PrepareTurn receives the newest user message or a trusted TaskWakeID(ctx) event, not the browser's
	// transcript. The host owns identity, permissions, model selection, History,
	// SessionID, EngineSession and InvocationScope. Return a fresh request.
	PrepareTurn func(context.Context, *ihtml.ChatSessionContext, *ihtml.ChatClientEvent) (*agent.TurnRequest, error)
	// TurnLifetime 返回宿主已领取轮次的生命周期（例如持久会话租约）。框架将其与请求取消合并；
	// 结束只取消当前推理，不取消宿主已受理的独立业务任务。nil hook 保持原行为。
	TurnLifetime func(context.Context, *ihtml.ChatSessionContext, *agent.TurnRequest) (context.Context, error)
	// Exactly one explicit model source is required; no readiness is inferred.
	Models         *ihtml.ChatModels
	ModelsProvider func(context.Context) (ihtml.ChatModels, error)
	// CompleteTurn receives partial results and failures for host persistence.
	// An error suppresses assistant_done. No client transcript is ever persisted
	// by this package. Hooks and Emit must return; Close waits for real unwind.
	CompleteTurn func(context.Context, *ihtml.ChatSessionContext, *agent.TurnRequest, *agent.TurnResult, error) error
	// ToolExecutionGuard runs immediately before each host Handler. Required for
	// ApprovalRequired tools: it must verify host-owned approval and live access,
	// never a client claim. Already-governed Handlers still own live revocation.
	ToolExecutionGuard func(context.Context, *ihtml.ChatSessionContext, agent.Tool, json.RawMessage) error
	Confirmation       *ConfirmationConfig
	// NativeConfirmation and manual Confirmation are mutually exclusive.
	NativeConfirmation *NativeConfirmationConfig
	// PresentToolResult 仅在接口执行成功时提供公开回执；可标记部分失败或未知，不扩大批准范围。
	PresentToolResult func(context.Context, *ihtml.ChatSessionContext, agent.Tool, json.RawMessage, string) ToolResultView
	// CompleteTool persists manual execution evidence; it does not resume the model trajectory.
	CompleteTool func(context.Context, *ihtml.ChatSessionContext, *ConfirmedToolResult) error
	// PersistToolHistory 在公开操作事件交付前保存历史。错误阻止交付并停止当前会话，
	// 不允许已签发确认卡只有浏览器副本；回调必须有界，且不得重入当前会话。
	PersistToolHistory func(context.Context, *ihtml.ChatSessionContext, ToolHistoryRecord) error
	// ContinueAfterTool 可选地提交一个新的受管轮次；只在确认执行及回执保存成功后调用。
	// 新轮次仍走 PrepareTurn、权限和签名确认，不继承上一张卡的写权限。
	ContinueAfterTool func(context.Context, *ihtml.ChatSessionContext, *ConfirmedToolResult) (*ihtml.ChatClientEvent, error)
	// PublicTurnError 只返回宿主固定的公开错误文案，绝不能透传模型服务或凭据错误原文。
	PublicTurnError func(error) string
	MaxEventBytes   int    // ordinary runner frame limit; default 2 MiB; maximum 16 MiB
	MaxPendingTurns int    // default 16; per connection, excluding the active turn
	ContentFormat   string // default text; HTML remains untrusted model output
}

type Backend struct{ cfg Config }

var _ ihtml.ChatBackend = (*Backend)(nil)
var _ ihtml.ModelLister = (*Backend)(nil)

// NewChecked validates dependencies without invoking the Engine or host hooks.
func NewChecked(cfg Config) (*Backend, error) {
	if isNil(cfg.Engine) {
		return nil, errors.New("assistant: Engine is required")
	}
	if cfg.PrepareTurn == nil {
		return nil, errors.New("assistant: PrepareTurn is required")
	}
	if (cfg.Models == nil) == (cfg.ModelsProvider == nil) {
		return nil, errors.New("assistant: specify exactly one of Models or ModelsProvider")
	}
	if cfg.MaxEventBytes < 0 || cfg.MaxEventBytes > 16<<20 {
		return nil, errors.New("assistant: MaxEventBytes must be between 0 and 16 MiB")
	}
	if cfg.MaxEventBytes == 0 {
		cfg.MaxEventBytes = 2 << 20
	}
	if cfg.MaxPendingTurns == 0 {
		cfg.MaxPendingTurns = 16
	}
	if cfg.MaxPendingTurns < 1 || cfg.MaxPendingTurns > 256 {
		return nil, errors.New("assistant: MaxPendingTurns must be between 1 and 256")
	}
	if cfg.ContentFormat == "" {
		cfg.ContentFormat = ihtml.ChatContentFormatText
	}
	switch cfg.ContentFormat {
	case ihtml.ChatContentFormatText, ihtml.ChatContentFormatMarkdown, ihtml.ChatContentFormatHTML:
	default:
		return nil, errors.New("assistant: unsupported ContentFormat")
	}
	if cfg.Tasks != nil {
		copied := *cfg.Tasks
		cfg.Tasks = &copied
	}
	if cfg.Models != nil {
		models := cloneModels(*cfg.Models)
		cfg.Models = &models
	}
	if cfg.Confirmation != nil {
		c := *cfg.Confirmation
		if !c.Signer.Available() || c.Scope == nil || c.Preview == nil || cfg.ToolExecutionGuard == nil {
			return nil, errors.New("assistant: confirmation requires Signer, Scope, Preview and ToolExecutionGuard")
		}
		if c.MaxPendingTools == 0 {
			c.MaxPendingTools = 16
		}
		if c.ToolTimeout == 0 {
			c.ToolTimeout = 30 * time.Second
		}
		if c.MaxPendingTools < 1 || c.MaxPendingTools > 256 || c.ToolTimeout < time.Millisecond || c.ToolTimeout > 10*time.Minute {
			return nil, errors.New("assistant: invalid confirmation limits")
		}
		if c.ResultReply != nil {
			reply := *c.ResultReply
			if reply.Timeout == 0 {
				reply.Timeout = 30 * time.Second
			}
			if reply.MaxOutputTokens == 0 {
				reply.MaxOutputTokens = 1600
			}
			if reply.MaxOutputTokens < 256 || reply.MaxOutputTokens > 8192 {
				return nil, errors.New("assistant: invalid result reply output limit")
			}
			if reply.Timeout < time.Millisecond || reply.Timeout > 2*time.Minute {
				return nil, errors.New("assistant: invalid result reply timeout")
			}
			c.ResultReply = &reply
		}
		cfg.Confirmation = &c
	}
	if cfg.NativeConfirmation != nil {
		c := *cfg.NativeConfirmation
		native, ok := cfg.Engine.(agent.NativeApprovalEngine)
		if !ok || !native.SupportsNativeApproval() {
			return nil, errors.New("assistant: Engine does not support durable native approval")
		}
		if cfg.Confirmation != nil || c.Pause == nil || c.PrepareResume == nil || c.Pending == nil || cfg.ToolExecutionGuard == nil || cfg.CompleteTurn == nil {
			return nil, errors.New("assistant: native confirmation requires durable hooks, CompleteTurn and ToolExecutionGuard; manual confirmation must be disabled")
		}
		cfg.NativeConfirmation = &c
	}
	return &Backend{cfg: cfg}, nil
}

func isNil(v any) bool {
	if v == nil {
		return true
	}
	r := reflect.ValueOf(v)
	switch r.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return r.IsNil()
	}
	return false
}

func cloneModels(m ihtml.ChatModels) ihtml.ChatModels {
	m.ProtocolVersion = ihtml.ChatProtocolVersion
	m.Models = append([]string{}, m.Models...)
	m.AgentModes = append([]string{}, m.AgentModes...)
	// Capability metadata comes from checked bridge configuration, never from a
	// host's unverified model listing.
	m.DeepApprovalResume = ""
	m.BatchConfirmations = false
	if m.PreviewOnly {
		m.ModelReady = false
	}
	return m
}

func (b *Backend) Models(ctx context.Context) (models ihtml.ChatModels, err error) {
	defer func() {
		if recover() != nil {
			models, err = ihtml.ChatModels{}, errors.New("assistant: ModelsProvider panicked")
		}
	}()
	if b.cfg.Models != nil {
		models = cloneModels(*b.cfg.Models)
	} else {
		models, err = b.cfg.ModelsProvider(ctx)
		models = cloneModels(models)
	}
	if b.cfg.Confirmation != nil {
		models.DeepApprovalResume = "manual_tool_only"
		models.BatchConfirmations = true
		// 续办复用普通受管轮次；只声明能继续核对，不把本卡批准扩大到后续写操作。
		if b.cfg.ContinueAfterTool != nil {
			models.DeepApprovalResume = "managed_turn"
		}
	}
	if b.cfg.NativeConfirmation != nil {
		models.DeepApprovalResume = "checkpoint"
	}
	return models, err
}

func (b *Backend) NewSession(ctx context.Context, sctx *ihtml.ChatSessionContext) (ihtml.ChatSession, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, errors.New("assistant: live session context is required")
	}
	if sctx == nil || strings.TrimSpace(sctx.User) == "" || sctx.Emit == nil {
		return nil, errors.New("assistant: authenticated User and Emit are required")
	}
	if sctx.Access.ChatDisabled {
		return nil, ihtml.ErrAccessDenied
	}
	copyContext := *sctx
	copyContext.APIs = append([]ihtml.APISpec(nil), sctx.APIs...)
	ctx, cancel := context.WithCancel(ctx)
	s := &session{backend: b, sctx: copyContext, ctx: ctx, cancel: cancel,
		wake: make(chan struct{}, 1), done: make(chan struct{}), pendingTools: make(map[string]*pendingTool)}
	if b.cfg.Tasks != nil || b.cfg.TasksProvider != nil {
		var err error
		taskConfig := agent.TaskConfig{}
		if b.cfg.Tasks != nil {
			taskConfig = *b.cfg.Tasks
		}
		if b.cfg.TasksProvider != nil {
			host := s.sctx
			taskConfig, err = b.cfg.TasksProvider(ctx, &host)
			if err != nil {
				cancel()
				return nil, err
			}
		}
		onComplete := taskConfig.OnComplete
		taskConfig.OnComplete = func(info agent.TaskInfo) {
			// 聊天通知仅用于显式后台调用；同步记账不能在推理中产生后台未读/推送。
			// 通用完成钩子仍接收所有账本终态，通知只发送句柄与状态。
			if info.Background {
				_ = s.emit(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamTaskComplete, TaskID: info.ID, TaskState: info.State, ToolName: info.Tool})
			}
			if onComplete != nil {
				onComplete(info)
			}
		}
		s.tasks, err = agent.NewTaskRuntime(ctx, taskConfig)
		if err != nil {
			cancel()
			return nil, err
		}
	}
	// The ordinary host bridge uses the same public lifecycle runner as
	// custom product backends. Confirmation schedulers retain their extra
	// rejection and asynchronous cancellation lanes below.
	if b.cfg.Confirmation == nil && b.cfg.NativeConfirmation == nil {
		runner, err := NewSessionRunner(ctx, SessionRunnerConfig{
			MaxPending: b.cfg.MaxPendingTurns, MaxEventBytes: b.cfg.MaxEventBytes,
			Run: func(ctx context.Context, event *ihtml.ChatClientEvent) {
				ctx, cancel := context.WithCancel(ctx)
				defer cancel()
				defer func() { s.mu.Lock(); s.activeOutput = nil; s.mu.Unlock() }()
				copied := cleanClonedEvent(*event)
				s.run(ctx, cancel, &copied)
			},
			OnQueueFull: func(context.Context) { s.queueFull() },
			OnPanic: func(context.Context, any) {
				_ = s.emit(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamTurnError, Error: "Assistant session failed."})
				s.cancel()
			},
		})
		if err != nil {
			cancel()
			if s.tasks != nil {
				s.tasks.Close()
			}
			return nil, err
		}
		s.runner = runner
		s.startTaskWake()
		return s, nil
	}
	if b.cfg.Confirmation != nil {
		s.cancelTools = make(chan pendingTurn, b.cfg.Confirmation.MaxPendingTools)
		s.cancelDone = make(chan struct{})
		go s.cancelToolLoop()
	}
	s.startTaskWake()
	go s.loop()
	return s, nil
}

type pendingTurn struct {
	parent            context.Context
	event             ihtml.ChatClientEvent
	reject            bool
	issuedApproval    bool
	continuation      bool
	batchIntermediate bool
}

type session struct {
	shutdownOnce       sync.Once
	shutdownDone       chan struct{}
	backend            *Backend
	tasks              *agent.TaskRuntime
	asyncConfirmations int
	confirmationTasks  sync.WaitGroup
	taskWakeDone       chan struct{}
	taskWakeStopped    bool
	taskWakes          map[string]taskWakeState
	sctx               ihtml.ChatSessionContext
	ctx                context.Context
	cancel             context.CancelFunc
	runner             *SessionRunner
	wake               chan struct{}
	done               chan struct{}
	mu                 sync.Mutex
	emitMu             sync.Mutex
	queue              []pendingTurn
	rejections         []pendingTurn
	pendingMu          sync.Mutex
	pendingTools       map[string]*pendingTool
	pendingReserved    int
	cancelTools        chan pendingTurn
	cancelDone         chan struct{}
	active             context.CancelFunc
	activeOutput       *projection
	queueRejected      bool
	closed             bool
}

// Close cancels active and queued work and waits for all host work to unwind.
// It must not be called reentrantly from Engine callbacks, host hooks or Emit.
func (s *session) Close() {
	if s.runner != nil {
		s.cancel()
		s.runner.Close()
		if s.tasks != nil {
			s.tasks.Close()
			if s.taskWakeDone != nil {
				<-s.taskWakeDone
			}
		}
		return
	}
	s.mu.Lock()
	s.closed = true
	s.queue = nil
	s.cancel()
	s.mu.Unlock()
	<-s.done
	if s.cancelDone != nil {
		<-s.cancelDone
	}
}

func (s *session) HandleEvent(ctx context.Context, ev *ihtml.ChatClientEvent) {
	if ev == nil || ctx == nil || ctx.Err() != nil {
		return
	}
	if ev.Type == ihtml.ChatClientUserMessage {
		s.restartTaskWakes(ctx)
	}
	eventScope := ""
	if s.tasks != nil {
		eventScope = s.tasks.Scope()
		if eventScope == "" && s.backend.cfg.Confirmation != nil {
			// 动态宿主的确认范围来自已签发提案，不从浏览器卡片字段推导权限。
			calls := ev.ToolCalls
			if ev.ToolCall != nil {
				calls = []ihtml.ProposedToolCall{*ev.ToolCall}
			}
			s.pendingMu.Lock()
			for _, call := range calls {
				if item := s.pendingTools[call.ConfirmationNonce]; item != nil {
					if eventScope != "" && eventScope != item.taskScope {
						eventScope = ""
						break
					}
					eventScope = item.taskScope
				}
			}
			s.pendingMu.Unlock()
		}
	}
	if eventScope != "" && (ev.Type == ihtml.ChatClientUserMessage || ev.Type == ihtml.ChatClientConfirmTool || ev.Type == ihtml.ChatClientConfirmTools || ev.Type == ihtml.ChatClientCancelTool) {
		// 明确消息在接纳时冻结代次；远程停止后，旧队列不能在出队时取得新代次。
		bound, err := s.tasks.BindContext(ctx, eventScope)
		if err != nil {
			_ = s.emit(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamTurnError, Error: "当前指令未获持久接纳，请稍后重试。"})
			return
		}
		ctx = bound
	}
	if s.runner != nil {
		if ev.Type == ihtml.ChatClientCancel {
			s.runner.Stop()
			s.stopTaskWakes()
			s.mu.Lock()
			s.queueRejected = false
			s.mu.Unlock()
		} else if ev.Type == ihtml.ChatClientUserMessage {
			err := s.runner.Submit(ctx, ev)
			switch {
			case err == nil, errors.Is(err, ErrSessionQueueFull), errors.Is(err, ErrSessionRunnerClosed), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			case errors.Is(err, ErrSessionEventTooLarge):
				_ = s.runner.RunInline(ctx, func(context.Context) {
					_ = s.emit(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamTurnError, Error: "Message is too large. Please send a shorter message."})
				})
			default:
				_ = s.runner.RunInline(ctx, func(context.Context) {
					_ = s.emit(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamTurnError, Error: "Message could not be read. Please resend it."})
				})
			}
		}
		return
	}
	if ev.Type == ihtml.ChatClientCancel {
		s.mu.Lock()
		if s.active != nil {
			s.active()
		}
		s.mu.Unlock()
		s.stopTaskWakes()
		s.mu.Lock()
		s.releaseDroppedConfirmationsLocked()
		s.queue = nil
		s.rejections = nil
		s.queueRejected = false
		if s.active != nil {
			s.active()
		}
		s.mu.Unlock()
		return
	}
	if s.backend.cfg.Confirmation != nil && ev.Type == ihtml.ChatClientConfirmTools {
		s.handleToolBatchEvent(ctx, ev)
		return
	}
	if s.backend.cfg.Confirmation != nil && (ev.Type == ihtml.ChatClientConfirmTool || ev.Type == ihtml.ChatClientCancelTool) {
		s.handleToolEvent(ctx, ev)
		return
	}
	if ev.Type != ihtml.ChatClientUserMessage {
		if s.backend.cfg.NativeConfirmation == nil || (ev.Type != ihtml.ChatClientConfirmTool && ev.Type != ihtml.ChatClientCancelTool) || ev.ToolCall == nil {
			return
		}
	}
	var copyEvent ihtml.ChatClientEvent
	var err error
	if ev.Type == ihtml.ChatClientUserMessage {
		copyEvent, err = cleanEvent(ev)
	} else {
		var call ihtml.ProposedToolCall
		call, err = cloneToolCall(*ev.ToolCall)
		copyEvent = ihtml.ChatClientEvent{Type: ev.Type, ToolCall: &call}
	}
	if err != nil {
		return
	}
	s.mu.Lock()
	if s.closed || s.ctx.Err() != nil {
		s.mu.Unlock()
		return
	}
	if s.queuedOrdinaryTurnsLocked() >= s.backend.cfg.MaxPendingTurns {
		output := s.activeOutput
		if output == nil {
			s.queueRejected = true
		}
		s.mu.Unlock()
		if output != nil {
			output.queueFull()
		}
		return
	}
	s.queue = append(s.queue, pendingTurn{parent: ctx, event: copyEvent})
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// 普通消息预算不含已经签发并占用确认额度的卡片；调用方持有 mu。
func (s *session) queuedOrdinaryTurnsLocked() int {
	count := 0
	for _, next := range s.queue {
		if !next.issuedApproval {
			count++
		}
	}
	return count
}

func cleanEvent(ev *ihtml.ChatClientEvent) (ihtml.ChatClientEvent, error) {
	out, err := cloneSessionEvent(ev, 0)
	if err != nil {
		return out, err
	}
	return cleanClonedEvent(out), nil
}

func cleanClonedEvent(out ihtml.ChatClientEvent) ihtml.ChatClientEvent {
	messages := out.Messages
	out.Messages = nil
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			out.Messages = []ihtml.ChatMessage{messages[i]}
			break
		}
	}
	out.ToolCall, out.Inspect = nil, nil
	out.ToolCalls = nil
	out.InspectID = ""
	return out
}

func (s *session) queueFull() {
	s.mu.Lock()
	output := s.activeOutput
	if output == nil {
		s.queueRejected = true
	}
	s.mu.Unlock()
	if output != nil {
		output.queueFull()
	}
}

func (s *session) loop() {
	defer close(s.done)
	if s.tasks != nil {
		defer func() {
			if s.taskWakeDone != nil {
				<-s.taskWakeDone
			}
			s.tasks.Close()
			s.confirmationTasks.Wait()
		}()
	}
	defer s.cleanupPendingTools()
	s.restoreApprovals()
	for {
		s.mu.Lock()
		if s.closed || s.ctx.Err() != nil {
			s.queue = nil
			s.mu.Unlock()
			return
		}
		if len(s.queue) == 0 && len(s.rejections) == 0 {
			s.mu.Unlock()
			select {
			case <-s.ctx.Done():
			case <-s.wake:
			}
			continue
		}
		// 保留原批量确认的收尾屏障；异步确认未保存回执前，不提前续办同一轮请求。
		if len(s.rejections) == 0 && len(s.queue) > 0 && s.queue[0].continuation && s.asyncConfirmations > 0 {
			s.mu.Unlock()
			select {
			case <-s.ctx.Done():
				return
			case <-s.wake:
			}
			continue
		}
		var next pendingTurn
		if len(s.rejections) > 0 {
			next, s.rejections = s.rejections[0], s.rejections[1:]
		} else {
			next, s.queue = s.queue[0], s.queue[1:]
		}
		ctx, cancelCause, finish := sessionExecution(s.ctx, next.parent)
		ctx = context.WithValue(ctx, batchIntermediateContextKey{}, next.batchIntermediate)
		cancel := func() { cancelCause(context.Canceled) }
		s.active = func() { cancelCause(ErrTurnStopped) }
		s.mu.Unlock()
		if s.backend.cfg.NativeConfirmation != nil {
			s.run(ctx, cancel, &next.event)
		} else if next.event.Type == ihtml.ChatClientCancelTool {
			s.cancelToolAck(*next.event.ToolCall, false)
		} else if next.event.Type == ihtml.ChatClientConfirmTool {
			if !s.scheduleConfirmedTask(ctx, &next.event, next.reject) {
				s.runConfirmedTool(ctx, cancel, &next.event, next.reject)
			}
		} else {
			s.run(ctx, cancel, &next.event)
		}
		finish()
		s.mu.Lock()
		s.active = nil
		s.activeOutput = nil
		s.mu.Unlock()
	}
}

func (s *session) emit(ev ihtml.ChatStreamEvent) (err error) {
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	defer func() {
		if recover() != nil {
			err = errors.New("assistant: Emit panicked")
		}
		if err != nil {
			if s.runner != nil {
				s.runner.Abort(err)
			}
			s.cancel()
		}
	}()
	if err := s.persistToolHistory(ev); err != nil {
		return err
	}
	if s.ctx.Err() != nil {
		return s.ctx.Err()
	}
	return s.sctx.Emit(ev)
}

// 终态历史不依赖浏览器仍连接；取消后只给有界保存时间，不借此恢复模型或执行工具。
func (s *session) persistToolHistory(ev ihtml.ChatStreamEvent) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("assistant: PersistToolHistory panicked")
		}
	}()
	if s.backend != nil && s.backend.cfg.PersistToolHistory != nil {
		if record, ok := ToolHistoryEvent(ev); ok {
			host := s.sctx
			ctx, stop := context.WithTimeout(context.WithoutCancel(s.ctx), 5*time.Second)
			defer stop()
			if err := s.backend.cfg.PersistToolHistory(ctx, &host, record); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *session) run(ctx context.Context, cancel context.CancelFunc, ev *ihtml.ChatClientEvent) {
	wake, waking := ctx.Value(taskWakeQueueKey{}).(taskWakeState)
	watchedScope := ""
	if s.tasks != nil && s.tasks.Scope() != "" {
		// 固定作用域在模型元数据与宿主准备前即验证，关闭工具也不解除整轮停止约束。
		var err error
		if waking {
			ctx, err = s.tasks.BindWaitContext(ctx, s.tasks.Scope(), wake.record.ID)
		} else {
			ctx, err = s.tasks.UseContext(ctx, s.tasks.Scope())
		}
		if err != nil {
			if waking {
				s.finishTaskWake(ev, false)
			}
			return
		}
		var release func()
		ctx, release, err = s.tasks.WatchContext(ctx, s.tasks.Scope())
		if err != nil {
			if waking {
				s.finishTaskWake(ev, false)
			}
			return
		}
		defer release()
		watchedScope = s.tasks.Scope()
	}
	if waking {
		s.mu.Lock()
		stopped := s.taskWakeStopped
		s.mu.Unlock()
		if stopped {
			return
		}
		var stopWake context.CancelFunc
		ctx, stopWake = context.WithCancel(ctx)
		defer stopWake()
		s.mu.Lock()
		current, exists := s.taskWakes[ev.RequestID]
		if exists && current.consumer == wake.consumer {
			current.cancel = stopWake
			s.taskWakes[ev.RequestID] = current
		}
		s.mu.Unlock()
		ctx = agent.WithTaskWake(ctx, wake.record.ID, wake.record.Depth)
	}
	p := newProjection(s, ctx, cancel)
	s.mu.Lock()
	s.activeOutput = p
	rejected := s.queueRejected
	s.queueRejected = false
	s.mu.Unlock()
	var req *agent.TurnRequest
	var result *agent.TurnResult
	var runErr error
	defer func() {
		if recover() != nil {
			runErr = errors.New("assistant: turn panicked")
		}
		p.mu.Lock()
		p.finished = true
		if p.err != nil {
			runErr = p.err
		} else if ctx.Err() != nil {
			runErr = ctx.Err()
		}
		p.mu.Unlock()
		if runErr == nil && (result == nil || result.FinishReason == "agent_error") {
			runErr = errors.New("assistant: engine did not complete successfully")
		}
		pauseDurable := false
		if runErr == nil && result != nil && result.PendingApproval != nil {
			if s.backend.cfg.NativeConfirmation == nil {
				runErr = errors.New("assistant: native interruption is not configured")
			} else {
				host := s.sctx
				cards, err := safeNativePause(s.backend.cfg.NativeConfirmation.Pause, ctx, &host, req, result)
				if err != nil {
					runErr = err
				} else {
					pauseDurable = true
					p.mu.Lock()
					runErr = p.approvalCards(cards)
					p.mu.Unlock()
				}
			}
		}
		if complete := s.backend.cfg.CompleteTurn; !pauseDurable && complete != nil && req != nil {
			if err := safeComplete(complete, ctx, &s.sctx, req, result, runErr); err != nil {
				runErr = err
			}
		}
		if runErr == nil && ctx.Err() != nil {
			runErr = ctx.Err()
		}
		if waking {
			s.finishTaskWake(ev, runErr == nil)
		}
		// 内部事件尚未获得轮次准入时只保留等待，不制造空的 AI 回复或忙碌错误通知。
		if waking && !p.started {
			return
		}
		p.end(result, runErr)
	}()
	models, err := s.backend.Models(ctx)
	p.mu.Lock()
	p.modelReady = models.ModelReady
	if !waking {
		p.started = true
		p.send(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamTurnStart})
		if rejected || p.pendingNotice {
			p.sendQueueFull()
		}
	}
	p.mu.Unlock()
	if err != nil {
		runErr = errors.New("assistant: model metadata unavailable")
		return
	}
	if ctx.Err() != nil {
		return
	}
	// A hook cannot mutate the connection's binding or replace its Emit callback.
	hostContext := s.sctx
	if ev.Type == ihtml.ChatClientConfirmTool || ev.Type == ihtml.ChatClientCancelTool {
		req, runErr = s.backend.cfg.NativeConfirmation.PrepareResume(ctx, &hostContext, *ev.ToolCall, ev.Type == ihtml.ChatClientConfirmTool)
		if runErr == nil && (req == nil || req.Resume == nil || req.Resume.ToolCallID == "" || !req.RequireApproval) {
			runErr = errors.New("assistant: missing native resume binding")
		}
		if runErr == nil && ev.Type == ihtml.ChatClientCancelTool {
			p.mu.Lock()
			p.send(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamCancelToolResult, ToolID: ev.ToolCall.ID, ConfirmationNonce: ev.ToolCall.ConfirmationNonce, ToolStatus: ihtml.ChatToolCancelled})
			p.mu.Unlock()
		}
		if runErr == nil {
			card, err := cloneToolCall(*ev.ToolCall)
			if err != nil {
				runErr = err
			} else {
				p.resumedCard = &card
				p.resumedToolCallID = req.Resume.ToolCallID
			}
		}
	} else {
		req, runErr = s.backend.cfg.PrepareTurn(ctx, &hostContext, ev)
	}
	if runErr != nil {
		return
	}
	if req == nil {
		runErr = errors.New("assistant: PrepareTurn returned nil")
		return
	}
	copyRequest := *req
	req = &copyRequest
	req.DisableTools = req.DisableTools || ev.ToolsEnabled != nil && !*ev.ToolsEnabled
	if req.DisableTools {
		req.Tasks = nil
		req.AllowTaskSuspend = false
	}
	if waking {
		req.WakeID = wake.record.ID
		if req.InvocationScope == "" {
			req.InvocationScope = "wake/" + wake.record.ID
		}
	}
	executionCtx := ctx
	if lifetime := s.backend.cfg.TurnLifetime; lifetime != nil {
		host := s.sctx
		var owner context.Context
		owner, runErr = lifetime(ctx, &host, req)
		if runErr != nil {
			return
		}
		if owner == nil {
			runErr = errors.New("assistant: missing turn lifetime")
			return
		}
		// 生命周期只增加取消条件，不能让宿主上下文脱离用户停止或连接关闭。
		combined, release := context.WithCancel(ctx)
		stopOwner := context.AfterFunc(owner, release)
		defer release()
		defer stopOwner()
		if owner.Err() != nil {
			release()
		}
		executionCtx = combined
	}
	if s.backend.cfg.NativeConfirmation != nil {
		req.RequireApproval = true
	}
	req.StreamReasoning = false
	req.Tools, runErr = s.governTools(req.Tools, ev, p, req)
	if runErr != nil {
		return
	}
	if s.tasks != nil && watchedScope == "" {
		// 动态作用域由可信宿主准备后确定；同样覆盖没有业务工具的受管模型轮次。
		scope := agent.TaskScope(req)
		if waking {
			executionCtx, runErr = s.tasks.BindWaitContext(executionCtx, scope, wake.record.ID)
		} else {
			executionCtx, runErr = s.tasks.UseContext(executionCtx, scope)
		}
		if runErr != nil {
			return
		}
		var release func()
		executionCtx, release, runErr = s.tasks.WatchContext(executionCtx, scope)
		if runErr != nil {
			return
		}
		defer release()
	}
	if s.tasks != nil && !req.DisableTools {
		req.Tasks = s.tasks
		s.mu.Lock()
		req.AllowTaskSuspend = s.tasks.Scope() != "" && !s.taskWakeStopped
		s.mu.Unlock()
		if req.InvocationScope == "" {
			req.InvocationScope = p.id
		}
		defer func() {
			req.Tasks.EndTurn(agent.TaskScope(req), req.InvocationScope, runErr == nil && executionCtx.Err() == nil)
		}()
	}
	if req.DisableTools {
		req.Skills = nil
	}
	onDelta, onEvent := req.OnDelta, req.OnEvent
	req.OnDelta = func(text string) { p.delta(text, onDelta) }
	req.OnEvent = func(step agent.Step) { p.step(step, onEvent) }
	if waking {
		s.mu.Lock()
		stopped := s.taskWakeStopped
		s.mu.Unlock()
		if stopped {
			runErr = context.Canceled
			return
		}
		if runErr = s.tasks.UpdateWake(executionCtx, s.tasks.Scope(), wake.record.ID, wake.consumer, "admit"); runErr != nil {
			if errors.Is(runErr, agent.ErrTaskWakeBudget) {
				p.mu.Lock()
				p.started = true
				p.send(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamTurnStart})
				p.mu.Unlock()
			}
			return
		}
		req.WakeAdmitted = true
		p.mu.Lock()
		p.started = true
		p.send(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamTurnStart})
		p.mu.Unlock()
	}
	if ctx.Err() == nil {
		result, runErr = s.backend.cfg.Engine.RunTurn(executionCtx, req)
		if runErr == nil && executionCtx.Err() != nil {
			runErr = executionCtx.Err()
		}
	}
}

func safeComplete(fn func(context.Context, *ihtml.ChatSessionContext, *agent.TurnRequest, *agent.TurnResult, error) error,
	ctx context.Context, sctx *ihtml.ChatSessionContext, req *agent.TurnRequest, result *agent.TurnResult, turnErr error) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("assistant: CompleteTurn panicked")
		}
	}()
	copyContext := *sctx
	return fn(ctx, &copyContext, req, result, turnErr)
}

func (s *session) governTools(tools []agent.Tool, ev *ihtml.ChatClientEvent, p *projection, req *agent.TurnRequest) ([]agent.Tool, error) {
	if req.DisableTools || ev.ToolsEnabled != nil && !*ev.ToolsEnabled {
		return nil, nil
	}
	guard := s.backend.cfg.ToolExecutionGuard
	out := make([]agent.Tool, 0, len(tools))
	for _, tool := range tools {
		if s.sctx.Access.ReadOnly && tool.Effect != agent.ToolEffectRead {
			continue
		}
		if tool.ApprovalRequired && guard == nil {
			continue
		}
		if tool.Handler == nil || strings.TrimSpace(tool.Name) == "" {
			return nil, errors.New("assistant: invalid host tool")
		}
		handler, original := tool.Handler, tool
		p.tools[tool.Name] = original
		if original.ApprovalRequired && s.backend.cfg.Confirmation != nil {
			// The native receipt represents pending, not domain execution. Domain
			// recovery/persistence callbacks must not run on that placeholder.
			tool.RecoverResult, tool.ResultPersisted = nil, nil
			p.mu.Lock()
			p.approvalTools[tool.Name] = true
			p.mu.Unlock()
		}
		tool.Handler = func(ctx context.Context, args json.RawMessage) (result string, err error) {
			defer func() {
				if recover() != nil {
					result, err = "", errors.New("assistant: tool panicked")
				}
			}()
			if ctx.Err() != nil || !agent.IsBackgroundTask(ctx) && p.ctx.Err() != nil {
				return "", context.Canceled
			}
			if original.ApprovalRequired && s.backend.cfg.Confirmation != nil {
				return s.prepareTool(ctx, original, args, p, req)
			}
			if guard != nil {
				hostContext := s.sctx
				if err := guard(ctx, &hostContext, original, args); err != nil {
					if errors.Is(err, ErrInvalidToolArguments) {
						return invalidToolArgumentsResult, nil
					}
					return "", ihtml.ErrAccessDenied
				}
			}
			// 工具通过上下文报告等待进度，复用当前投影，不能依赖宿主 PrepareTurn 返回指针。
			ctx = agent.WithStatusReporter(ctx, func(text string) {
				if req.OnEvent != nil {
					req.OnEvent(agent.Step{Kind: agent.StepStatus, Result: text})
				}
			})
			return handler(ctx, args)
		}
		out = append(out, tool)
	}
	return out, nil
}

type projection struct {
	continuationPending bool
	mu                  sync.Mutex
	session             *session
	ctx                 context.Context
	cancel              context.CancelFunc
	id                  string
	seq                 int64
	snapshot            string
	finished            bool
	err                 error
	emitFailed          bool
	modelReady          bool
	toolSeq             int64
	started             bool
	pendingNotice       bool
	pendingSteps        map[string]int
	approvalTools       map[string]bool
	tools               map[string]agent.Tool
	resumedCard         *ihtml.ProposedToolCall
	resumedToolCallID   string
}

func newProjection(s *session, ctx context.Context, cancel context.CancelFunc) *projection {
	var id [16]byte
	// crypto/rand.Read either fills the buffer or panics on modern Go.
	_, _ = rand.Read(id[:])
	return &projection{session: s, ctx: ctx, cancel: cancel, id: hex.EncodeToString(id[:]), pendingSteps: make(map[string]int), approvalTools: make(map[string]bool), tools: make(map[string]agent.Tool)}
}

// All send calls hold mu across sequence assignment and synchronous emission.
func (p *projection) send(ev ihtml.ChatStreamEvent) {
	if p.emitFailed {
		// 网络失败后仍保留后续实际终态；不能因已停止发帧而把历史永久留在执行中。
		if err := p.session.persistToolHistory(ev); err != nil {
			p.err = errors.Join(p.err, err)
		}
		return
	}
	p.seq++
	ev.RunID, ev.TurnID, ev.Seq = p.id, p.id+"_turn", p.seq
	ev.ModelReady = p.modelReady
	if err := p.session.emit(ev); err != nil {
		p.err = err
		p.emitFailed = true
		p.cancel()
	}
}

func (p *projection) callback(fn func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finished || p.ctx.Err() != nil || p.err != nil {
		return
	}
	defer func() {
		if recover() != nil {
			p.err = errors.New("assistant: callback panicked")
			p.cancel()
		}
	}()
	fn()
}

func (p *projection) delta(text string, host func(string)) {
	p.callback(func() {
		if host != nil {
			host(text)
		}
		if text == p.snapshot {
			return
		}
		delta := text
		if strings.HasPrefix(text, p.snapshot) {
			delta = strings.TrimPrefix(text, p.snapshot)
		} else {
			p.send(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamAssistantReset})
		}
		p.snapshot = text
		if delta != "" {
			p.send(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamAssistantDelta, Delta: delta, ContentFormat: p.session.backend.cfg.ContentFormat})
		}
	})
}

func (p *projection) queueFull() {
	p.callback(func() {
		if !p.started {
			p.pendingNotice = true
			return
		}
		p.sendQueueFull()
	})
}

func (p *projection) sendQueueFull() {
	p.pendingNotice = false
	p.send(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamAssistantStatus,
		Status: "Pending message rejected: the turn queue is full."})
}

func (p *projection) step(step agent.Step, host func(agent.Step)) {
	p.callback(func() {
		if host != nil {
			host(step)
		}
		if step.Kind == agent.StepStatus {
			if text := strings.TrimSpace(step.Result); text != "" && len(text) <= 1024 {
				p.send(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamAssistantStatus, Status: text})
			}
			return
		}
		if step.Kind != agent.StepToolCall || step.ApprovalPending {
			return
		}
		key := step.ToolName + "\x00" + string(confirmation.CanonicalBody(step.Args))
		if p.pendingSteps[key] > 0 {
			p.pendingSteps[key]--
			return
		}
		previewRefresh := false
		if p.approvalTools[step.ToolName] && step.Err == "" {
			var receipt struct {
				Status string `json:"status"`
				ID     string `json:"id"`
			}
			if json.Unmarshal([]byte(step.Result), &receipt) == nil {
				if receipt.Status == "pending" && receipt.ID != "" {
					return
				}
				previewRefresh = receipt.Status == "preview_refresh_required"
			}
		}
		status, summary := ihtml.ChatToolSuccess, "Invocation returned; no public result details were provided."
		var admission struct {
			Status         string `json:"status"`
			ExecutionState string `json:"execution_state"`
		}
		_ = json.Unmarshal([]byte(step.Result), &admission)
		switch {
		case step.Err != "":
			status, summary = ihtml.ChatToolError, "Tool failed."
		case admission.Status == "not_admitted" && admission.ExecutionState == "not_started":
			// 通用接纳协议优先于工具异步声明，不能把共享预算拒绝展示成已受理。
			status, summary = ihtml.ChatToolCancelled, "调用未受理，原工具未执行。"
		case admission.Status == "operation_failed" && admission.ExecutionState == "unknown":
			status, summary = ihtml.ChatToolUnknown, "调用结果未知，请核对原回执。"
		case admission.Status == "operation_failed":
			status, summary = ihtml.ChatToolError, "调用未完成，请核对执行状态与回执。"
		case previewRefresh:
			status, summary = ihtml.ChatToolCancelled, "Preview needs refreshing; nothing executed."
		case step.ApprovalRejected:
			status, summary = ihtml.ChatToolCancelled, "Tool rejected; not executed."
		case step.Replayed:
			status, summary = ihtml.ChatToolCancelled, "Duplicate invocation suppressed."
		case step.Completion == agent.ToolCompletionAsynchronous:
			status = ihtml.ChatToolAccepted
			summary = "Request accepted; completion is not confirmed."
		}
		p.toolSeq++
		event := ihtml.ChatStreamEvent{Type: ihtml.ChatStreamToolResult, ToolID: fmt.Sprintf("%s_tool_%d", p.id, p.toolSeq), ToolName: step.ToolName, ToolStatus: status, ToolSummary: summary}
		if status == ihtml.ChatToolSuccess || status == ihtml.ChatToolAccepted || previewRefresh {
			view := p.session.publicToolResult(p.ctx, p.tools[step.ToolName], step.Args, step.Result)
			event.ToolTitle, event.ToolResult = view.Title, view.Result
			if view.Status != "" && !(status == ihtml.ChatToolAccepted && view.Status == ihtml.ChatToolSuccess) {
				event.ToolStatus = view.Status
			}
			if view.Summary != "" {
				event.ToolSummary = view.Summary
			}
		}
		if p.resumedCard != nil && p.resumedToolCallID != "" && p.resumedToolCallID == step.ToolCallID && p.resumedCard.Name == step.ToolName {
			event.ToolID = p.resumedCard.ID
			event.ToolCall = p.resumedCard
			p.resumedCard = nil
		}
		p.send(event)
	})
}

func (p *projection) end(result *agent.TurnResult, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.finished = true
	if err != nil || result == nil || result.FinishReason == "agent_error" {
		message := "Assistant turn failed."
		if errors.Is(err, context.Canceled) {
			message = "Assistant turn cancelled."
		}
		if format := p.session.backend.cfg.PublicTurnError; format != nil {
			func() {
				defer func() { _ = recover() }()
				if text := format(err); strings.TrimSpace(text) != "" && len(text) <= 4096 {
					message = text
				}
			}()
		}
		p.send(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamTurnError, Error: message})
		return
	}
	if result.PendingApproval != nil {
		p.send(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamTurnPaused, Status: "Awaiting confirmation."})
		return
	}
	p.send(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamAssistantDone, Message: result.Text,
		ContentFormat: p.session.backend.cfg.ContentFormat, FinishReason: result.FinishReason, ContinuationPending: p.continuationPending,
		Usage: &ihtml.ChatUsage{InputTokens: result.Usage.InputTokens, OutputTokens: result.Usage.OutputTokens,
			TotalTokens: result.Usage.InputTokens + result.Usage.OutputTokens}})
}

// String deliberately exposes no engine/provider configuration.
func (b *Backend) String() string {
	return fmt.Sprintf("assistant.Backend(queue=%d)", b.cfg.MaxPendingTurns)
}

// 展示钩子异常只丢弃展示内容；不能把已执行操作伪装成失败并诱导重试。
func (s *session) publicToolResult(ctx context.Context, tool agent.Tool, args json.RawMessage, result string) (view ToolResultView) {
	defer func() {
		if recover() != nil {
			view = ToolResultView{}
		}
	}()
	if fn := s.backend.cfg.PresentToolResult; fn != nil {
		host := s.sctx
		view = fn(ctx, &host, tool, append(json.RawMessage(nil), args...), result)
	}
	if len(view.Title) > 1024 || len(view.Summary) > 32768 {
		return ToolResultView{}
	}
	if view.Status != "" && view.Status != ihtml.ChatToolSuccess && view.Status != ihtml.ChatToolAccepted && view.Status != ihtml.ChatToolError && view.Status != ihtml.ChatToolUnknown {
		view.Status = ""
	}
	raw, err := json.Marshal(view.Result)
	if err != nil || len(raw) > 32768 {
		view.Result = nil
	}
	return view
}

// Shutdown 在取消后有期限地等待宿主与工具退出；超时不伪称物理执行者已停止。
func (s *session) Shutdown(ctx context.Context) error {
	s.shutdownOnce.Do(func() {
		s.shutdownDone = make(chan struct{})
		s.cancel()
		go func() { s.Close(); close(s.shutdownDone) }()
	})
	select {
	case <-s.shutdownDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
