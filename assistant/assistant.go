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
	// PrepareTurn receives only the newest client user message, not the browser's
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
			return nil, err
		}
		s.runner = runner
		return s, nil
	}
	if b.cfg.Confirmation != nil {
		s.cancelTools = make(chan pendingTurn, b.cfg.Confirmation.MaxPendingTools)
		s.cancelDone = make(chan struct{})
		go s.cancelToolLoop()
	}
	go s.loop()
	return s, nil
}

type pendingTurn struct {
	parent context.Context
	event  ihtml.ChatClientEvent
	reject bool
}

type session struct {
	backend         *Backend
	sctx            ihtml.ChatSessionContext
	ctx             context.Context
	cancel          context.CancelFunc
	runner          *SessionRunner
	wake            chan struct{}
	done            chan struct{}
	mu              sync.Mutex
	emitMu          sync.Mutex
	queue           []pendingTurn
	rejections      []pendingTurn
	pendingMu       sync.Mutex
	pendingTools    map[string]*pendingTool
	pendingReserved int
	cancelTools     chan pendingTurn
	cancelDone      chan struct{}
	active          context.CancelFunc
	activeOutput    *projection
	queueRejected   bool
	closed          bool
}

// Close cancels active and queued work and waits for all host work to unwind.
// It must not be called reentrantly from Engine callbacks, host hooks or Emit.
func (s *session) Close() {
	if s.runner != nil {
		s.cancel()
		s.runner.Close()
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
	if s.runner != nil {
		if ev.Type == ihtml.ChatClientCancel {
			s.runner.Stop()
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
	if len(s.queue) >= s.backend.cfg.MaxPendingTurns {
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
		var next pendingTurn
		if len(s.rejections) > 0 {
			next, s.rejections = s.rejections[0], s.rejections[1:]
		} else {
			next, s.queue = s.queue[0], s.queue[1:]
		}
		ctx, cancelCause, finish := sessionExecution(s.ctx, next.parent)
		cancel := func() { cancelCause(context.Canceled) }
		s.active = func() { cancelCause(ErrTurnStopped) }
		s.mu.Unlock()
		if s.backend.cfg.NativeConfirmation != nil {
			s.run(ctx, cancel, &next.event)
		} else if next.event.Type == ihtml.ChatClientCancelTool {
			s.cancelToolAck(*next.event.ToolCall, false)
		} else if next.event.Type == ihtml.ChatClientConfirmTool {
			s.runConfirmedTool(ctx, cancel, &next.event, next.reject)
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
	if s.ctx.Err() != nil {
		return s.ctx.Err()
	}
	return s.sctx.Emit(ev)
}

func (s *session) run(ctx context.Context, cancel context.CancelFunc, ev *ihtml.ChatClientEvent) {
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
		p.end(result, runErr)
	}()
	models, err := s.backend.Models(ctx)
	p.mu.Lock()
	p.modelReady = models.ModelReady
	p.started = true
	p.send(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamTurnStart})
	if rejected || p.pendingNotice {
		p.sendQueueFull()
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
	if ev.ToolsEnabled != nil && !*ev.ToolsEnabled {
		req.Skills = nil
	}
	onDelta, onEvent := req.OnDelta, req.OnEvent
	req.OnDelta = func(text string) { p.delta(text, onDelta) }
	req.OnEvent = func(step agent.Step) { p.step(step, onEvent) }
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
	if ev.ToolsEnabled != nil && !*ev.ToolsEnabled {
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
			if ctx.Err() != nil || p.ctx.Err() != nil {
				return "", context.Canceled
			}
			if original.ApprovalRequired && s.backend.cfg.Confirmation != nil {
				return s.prepareTool(ctx, original, args, p, req)
			}
			if guard != nil {
				hostContext := s.sctx
				if err := guard(ctx, &hostContext, original, args); err != nil {
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
	mu                sync.Mutex
	session           *session
	ctx               context.Context
	cancel            context.CancelFunc
	id                string
	seq               int64
	snapshot          string
	finished          bool
	err               error
	emitFailed        bool
	modelReady        bool
	toolSeq           int64
	started           bool
	pendingNotice     bool
	pendingSteps      map[string]int
	approvalTools     map[string]bool
	tools             map[string]agent.Tool
	resumedCard       *ihtml.ProposedToolCall
	resumedToolCallID string
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
		switch {
		case step.Err != "":
			status, summary = ihtml.ChatToolError, "Tool failed."
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
		ContentFormat: p.session.backend.cfg.ContentFormat, FinishReason: result.FinishReason,
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
