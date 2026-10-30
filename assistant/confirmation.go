package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/agent"
	"github.com/zdypro888/ihtml/confirmation"
)

// ConfirmationConfig enables process-local manual approvals, never checkpoint
// resume. Scope and Preview receive trusted server bindings and must cooperate
// with cancellation. Preview is the host's explicit public/redacted card view.
// ErrPreviewRefreshRequired 表示宿主已确认预览不足以签发新卡；没有执行动作，模型应刷新原范围。
// 桥接层仅公开固定结构，不透传普通错误正文，也不把此结果当成用户批准。
var ErrPreviewRefreshRequired = errors.New("assistant: refresh the original preview before confirmation")

// ErrInvalidToolArguments 仅表示执行前的参数校验失败；不适用于权限错误或已有副作用的失败。
// 桥接层返回固定结构让模型修正提案，不透传原始错误或签发无效确认卡。
var ErrInvalidToolArguments = errors.New("assistant: invalid tool arguments; not executed")

const invalidToolArgumentsResult = `{"status":"invalid_arguments","execution":"not_started","next_action":"correct_arguments","message":"Check the tool schema and original resource identifiers, correct the arguments, then propose again. No operation was executed."}`

type ConfirmationConfig struct {
	Signer          *confirmation.Signer
	Scope           func(context.Context, *ihtml.ChatSessionContext, *agent.TurnRequest) (confirmation.Scope, error)
	Preview         func(context.Context, *ihtml.ChatSessionContext, agent.Tool, json.RawMessage) (ihtml.ProposedToolCall, error)
	MaxPendingTools int           // default 16; range 1..256
	ToolTimeout     time.Duration // default 30s; range 1ms..10min
	// ResultReply 在真实回执后生成一次无工具的解释；不会恢复原操作计划。
	ResultReply *ResultReplyConfig
}

// ConfirmedToolResult is host-only execution evidence, not a model turn. Err may
// mean an unknown side-effect state; hosts must not retry blindly. Args and Call
// are detached copies. CompleteTool must not infer success solely from Result.
type ConfirmedToolResult struct {
	Call            ihtml.ProposedToolCall
	Tool            agent.Tool
	Args            json.RawMessage
	Result          string
	Err             error
	InvocationScope string
	// Started distinguishes guard/validation rejection from an execution whose
	// final side-effect state may be unknown. It is set immediately before Handler.
	Started bool
}

type pendingTool struct {
	call            ihtml.ProposedToolCall
	scope           confirmation.Scope
	tool            agent.Tool
	args            json.RawMessage
	invocation      context.Context
	invocationScope string
	userText        string
	model           string
	queued          bool // guarded by pendingMu; one confirm attempt per pending card
}

func (s *session) prepareTool(ctx context.Context, tool agent.Tool, args json.RawMessage, p *projection, req *agent.TurnRequest) (string, error) {
	cfg := s.backend.cfg.Confirmation
	if len(args) > 64<<10 || !json.Valid(args) {
		return "", errors.New("assistant: invalid or oversized tool arguments")
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.ToolTimeout)
	defer cancel()
	args = append(json.RawMessage(nil), args...)
	host := s.sctx
	scope, err := cfg.Scope(ctx, &host, req)
	if err != nil || !scope.Valid() {
		return "", errors.New("assistant: confirmation scope unavailable")
	}
	host = s.sctx
	call, err := cfg.Preview(ctx, &host, tool, append(json.RawMessage(nil), args...))
	if errors.Is(err, ErrInvalidToolArguments) {
		return invalidToolArgumentsResult, nil
	}
	if errors.Is(err, ErrPreviewRefreshRequired) {
		return `{"status":"preview_refresh_required","execution":"not_started","next_action":"refresh_preview"}`, nil
	}
	if err != nil {
		return "", errors.New("assistant: confirmation preview unavailable")
	}
	// IDs and approval semantics are bridge-owned, never model- or preview-owned.
	call.ID = newProjection(s, ctx, func() {}).id
	call.Name, call.AgentMode, call.ApprovalResume = tool.Name, "deep", "manual_tool_only"
	call.RequiresConfirmation = true
	if call.Risk != "low" && call.Risk != "medium" && call.Risk != "high" && call.Risk != "blocked" {
		call.Risk = "high"
	}
	if call.Risk == "blocked" {
		return "", ihtml.ErrAccessDenied
	}
	if len(call.Name) > 256 || len(call.Title) > 1024 || len(call.Method) > 32 || len(call.Path) > 4096 || len(call.Reason) > 4096 {
		return "", errors.New("assistant: oversized confirmation metadata")
	}
	// Preview 是可信宿主回调；其业务期限只作上限，签名和 nonce 始终由桥接层重新生成。
	var deadline time.Time
	if call.ConfirmationExpires != 0 {
		deadline = time.Unix(call.ConfirmationExpires, 0)
	}
	call.ConfirmationToken, call.ConfirmationNonce, call.ConfirmationExpires = "", "", 0
	raw, err := json.Marshal(call)
	if err != nil || len(raw) > (256<<10)-512 {
		return "", errors.New("assistant: invalid or oversized confirmation preview")
	}
	s.pendingMu.Lock()
	s.expirePendingToolsLocked(time.Now())
	if len(s.pendingTools)+s.pendingReserved >= cfg.MaxPendingTools {
		s.pendingMu.Unlock()
		return "", errors.New("assistant: pending confirmation capacity exceeded")
	}
	s.pendingReserved++
	s.pendingMu.Unlock()
	reserved := true
	defer func() {
		if reserved {
			s.pendingMu.Lock()
			s.pendingReserved--
			s.pendingMu.Unlock()
		}
	}()
	if ctx.Err() != nil || s.ctx.Err() != nil {
		return "", context.Canceled
	}
	call, err = cfg.Signer.PrepareUntil(ctx, s.sctx.User, scope, call, deadline)
	if err != nil {
		return "", errors.New("assistant: confirmation issuance failed")
	}
	s.pendingMu.Lock()
	s.pendingTools[call.ConfirmationNonce] = &pendingTool{call: call, scope: scope, tool: tool, args: args,
		invocation: context.WithoutCancel(ctx), invocationScope: req.InvocationScope, userText: req.UserText, model: req.Model}
	s.pendingReserved--
	reserved = false
	s.pendingMu.Unlock()
	view, err := cloneToolCall(call)
	if err != nil {
		return "", err
	}
	emitted := false
	p.callback(func() {
		key := tool.Name + "\x00" + string(confirmation.CanonicalBody(args))
		p.pendingSteps[key]++
		p.send(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamConfirmationRequired, ToolCall: &view,
			ToolID: call.ID, ToolName: tool.Name, ToolStatus: ihtml.ChatToolPending, ToolSummary: "Awaiting manual confirmation; not executed."})
		emitted = !p.emitFailed
	})
	if !emitted {
		s.pendingMu.Lock()
		delete(s.pendingTools, call.ConfirmationNonce)
		s.pendingMu.Unlock()
		cleanupCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), cfg.ToolTimeout)
		defer stop()
		_, _ = cfg.Signer.Revoke(cleanupCtx, s.sctx.User, scope, call)
		return "", errors.New("assistant: confirmation was not delivered")
	}
	result, _ := json.Marshal(struct {
		Status string `json:"status"`
		ID     string `json:"id"`
	}{"pending", call.ID})
	return string(result), nil
}

func cloneToolCall(call ihtml.ProposedToolCall) (ihtml.ProposedToolCall, error) {
	raw, err := json.Marshal(call)
	if err != nil {
		return ihtml.ProposedToolCall{}, err
	}
	var out ihtml.ProposedToolCall
	err = json.Unmarshal(raw, &out)
	return out, err
}

func (s *session) expirePendingToolsLocked(now time.Time) {
	for nonce, item := range s.pendingTools {
		if item.call.ConfirmationExpires <= now.Unix() {
			delete(s.pendingTools, nonce)
		}
	}
}

func (s *session) handleToolEvent(ctx context.Context, ev *ihtml.ChatClientEvent) {
	var source ihtml.ProposedToolCall
	if ev.ToolCall != nil {
		source = *ev.ToolCall
	}
	call, err := cloneToolCall(source)
	if err != nil {
		call = ihtml.ProposedToolCall{ID: boundedReference(source.ID), ConfirmationNonce: boundedReference(source.ConfirmationNonce)}
	}
	raw, marshalErr := json.Marshal(call)
	invalid := err != nil || marshalErr != nil || len(raw) > 256<<10
	if invalid {
		call = ihtml.ProposedToolCall{ID: boundedReference(source.ID), ConfirmationNonce: boundedReference(source.ConfirmationNonce)}
	}
	next := pendingTurn{parent: ctx, event: ihtml.ChatClientEvent{Type: ev.Type, ToolCall: &call}, reject: invalid}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.ctx.Err() != nil {
		return
	}
	if ev.Type == ihtml.ChatClientCancelTool {
		select {
		case s.cancelTools <- next:
		default:
			// Never call Emit under mu, including a host's reentrant HandleEvent.
			if len(s.rejections) < s.backend.cfg.Confirmation.MaxPendingTools {
				next.reject = true
				s.rejections = append(s.rejections, next)
				select {
				case s.wake <- struct{}{}:
				default:
				}
			}
		}
		return
	}
	s.pendingMu.Lock()
	item := s.pendingTools[call.ConfirmationNonce]
	if item != nil && item.queued {
		s.pendingMu.Unlock()
		return
	}
	// Bound rejected confirmations separately. They are drained serially after
	// the active task, not projected as an error onto the active model turn.
	// 已签发确认卡本来就占用 MaxPendingTools 的有界预算，不能再与普通提问共享 16 项排队额度。
	// 同一 nonce 的 queued 标记保证最多入队一次；未知请求仍使用普通队列和拒绝预算。
	full := s.queuedOrdinaryTurnsLocked() >= s.backend.cfg.MaxPendingTurns
	if item != nil {
		full = len(s.queue)-s.queuedOrdinaryTurnsLocked() >= s.backend.cfg.Confirmation.MaxPendingTools
	}
	next.issuedApproval = item != nil
	if full {
		if len(s.rejections) >= s.backend.cfg.Confirmation.MaxPendingTools {
			// Reserve the bounded rejection budget for real issued cards. Unknown
			// or malformed client requests must not starve a legitimate card ACK.
			replaced := false
			if item != nil {
				for i, prior := range s.rejections {
					if prior.event.ToolCall == nil || s.pendingTools[prior.event.ToolCall.ConfirmationNonce] == nil {
						next.reject = true
						s.rejections[i] = next
						replaced = true
						break
					}
				}
			}
			if !replaced {
				s.pendingMu.Unlock()
				return
			}
		} else {
			next.reject = true
			s.rejections = append(s.rejections, next)
		}
	} else {
		s.queue = append(s.queue, next)
	}
	if item != nil {
		item.queued = true
	}
	s.pendingMu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *session) cancelToolAck(call ihtml.ProposedToolCall, ok bool) {
	status, summary, failure := ihtml.ChatToolError, "", "Confirmation cancellation failed; not acknowledged."
	if ok {
		status, summary, failure = ihtml.ChatToolCancelled, "Cancelled; not executed.", ""
	}
	// Connection-scoped protocol: intentionally no RunID or turn sequence.
	_ = s.emit(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamCancelToolResult,
		ToolID: boundedReference(call.ID), ConfirmationNonce: boundedReference(call.ConfirmationNonce),
		ToolStatus: status, ToolSummary: summary, Error: failure})
}

func boundedReference(s string) string {
	if len(s) > 256 {
		return s[:256]
	}
	return s
}

func (s *session) cancelToolLoop() {
	defer close(s.cancelDone)
	for {
		select {
		case <-s.ctx.Done():
			return
		case next := <-s.cancelTools:
			ctx, cancel := context.WithTimeout(s.ctx, s.backend.cfg.Confirmation.ToolTimeout)
			stop := context.AfterFunc(next.parent, cancel)
			if next.parent.Err() != nil {
				cancel()
			}
			call := *next.event.ToolCall
			s.pendingMu.Lock()
			item := s.pendingTools[call.ConfirmationNonce]
			s.pendingMu.Unlock()
			ok := false
			if item != nil {
				ok, _ = s.backend.cfg.Confirmation.Signer.Revoke(ctx, s.sctx.User, item.scope, call)
				if ok {
					s.pendingMu.Lock()
					delete(s.pendingTools, call.ConfirmationNonce)
					s.pendingMu.Unlock()
				}
			}
			stop()
			cancel()
			s.cancelToolAck(call, ok)
		}
	}
}

func (s *session) cleanupPendingTools() {
	if s.backend.cfg.Confirmation == nil {
		return
	}
	s.pendingMu.Lock()
	items := s.pendingTools
	s.pendingTools = make(map[string]*pendingTool)
	s.pendingMu.Unlock()
	// One bounded cleanup deadline for all cards; do not retain session work on
	// a cancelled connection. Stores must honor context, just like runtime hooks.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), s.backend.cfg.Confirmation.ToolTimeout)
	defer cancel()
	for _, item := range items {
		_, _ = s.backend.cfg.Confirmation.Signer.Revoke(ctx, s.sctx.User, item.scope, item.call)
	}
}

func (s *session) runConfirmedTool(ctx context.Context, cancel context.CancelFunc, ev *ihtml.ChatClientEvent, rejected bool) {
	replyParent := ctx
	ctx, timeoutCancel := context.WithTimeout(ctx, s.backend.cfg.Confirmation.ToolTimeout)
	defer timeoutCancel()
	p := newProjection(s, replyParent, cancel)
	s.mu.Lock()
	s.activeOutput = p
	s.mu.Unlock()
	call := *ev.ToolCall
	var evidence *ConfirmedToolResult
	var pending *pendingTool
	var runErr error
	started := false
	recovered := false
	var finishRecovery func()
	defer func() {
		if finishRecovery != nil {
			defer finishRecovery()
		}
		if recover() != nil {
			runErr = errors.New("assistant: confirmed tool panicked; execution state unknown")
		}
		if ctx.Err() != nil && !recovered {
			runErr = ctx.Err()
		}
		if evidence != nil {
			evidence.Started = started
			evidence.Err = runErr
			if fn := s.backend.cfg.CompleteTool; fn != nil {
				if err := safeCompleteTool(fn, ctx, &s.sctx, evidence); err != nil {
					runErr = err
				}
			}
		}
		if ctx.Err() != nil && !recovered {
			runErr = ctx.Err()
		}
		status, summary := ihtml.ChatToolSuccess, "Invocation returned; no public result details were provided. Check the source record to verify the business outcome."
		view := ToolResultView{}
		if evidence != nil {
			view.Title = evidence.Call.Title
			if runErr == nil {
				public := s.publicToolResult(ctx, evidence.Tool, evidence.Args, evidence.Result)
				if public.Title != "" {
					view.Title = public.Title
				}
				view.Summary, view.Result, view.Status = public.Summary, public.Result, public.Status
			}
		}
		if evidence != nil && evidence.Tool.Completion == agent.ToolCompletionAsynchronous {
			status = ihtml.ChatToolAccepted
			summary = "Request accepted; completion is not confirmed."
		}
		if runErr != nil {
			status, summary = ihtml.ChatToolError, "Not executed."
			if started {
				status, summary = ihtml.ChatToolUnknown, "Execution outcome unknown; check the original record before retrying."
			}
		}
		if runErr == nil && view.Status != "" && !(status == ihtml.ChatToolAccepted && view.Status == ihtml.ChatToolSuccess) {
			status = view.Status
		}
		if runErr == nil && view.Summary != "" {
			summary = view.Summary
		}
		if view.Title != "" && view.Summary == "" {
			summary = view.Title + ": " + summary
		}
		// Echo only bounded card identifiers, never unverified client card contents.
		ref := ihtml.ProposedToolCall{ID: boundedReference(call.ID), ConfirmationNonce: boundedReference(call.ConfirmationNonce)}
		p.mu.Lock()
		p.send(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamToolResult, ToolCall: &ref, ToolID: ref.ID,
			ToolName: func() string {
				if evidence != nil {
					return evidence.Tool.Name
				}
				return ""
			}(), ToolTitle: view.Title, ToolResult: view.Result, ToolStatus: status, ToolSummary: summary})
		p.mu.Unlock()
		if runErr != nil {
			p.end(nil, runErr)
		} else {
			// 后续步骤必须重新进入普通轮次；原回执已落地，续跑失败不能改变业务结果或重放动作。
			var next *ihtml.ChatClientEvent
			if fn := s.backend.cfg.ContinueAfterTool; fn != nil && evidence != nil && started && runErr == nil && evidence.Err == nil && status != ihtml.ChatToolUnknown {
				func() {
					defer func() { _ = recover() }()
					host := s.sctx
					candidate, err := fn(replyParent, &host, evidence)
					if err == nil && candidate != nil && candidate.Type == ihtml.ChatClientUserMessage {
						next = candidate
					}
				}()
			}
			reply := &agent.TurnResult{Text: summary, FinishReason: "manual_tool_only"}
			if next == nil && s.backend.cfg.Confirmation.ResultReply != nil && evidence != nil && pending != nil {
				reply = s.explainToolResult(replyParent, p, pending, status, view, summary)
			}
			p.mu.Lock()
			p.continuationPending = next != nil && replyParent.Err() == nil && s.enqueueContinuation(replyParent, next)
			p.mu.Unlock()
			p.end(reply, nil)
		}
	}()
	models, err := s.backend.Models(ctx)
	p.mu.Lock()
	p.modelReady = models.ModelReady
	p.started = true
	p.send(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamTurnStart})
	p.mu.Unlock()
	if err != nil {
		runErr = errors.New("assistant: model metadata unavailable")
		s.releaseQueued(call)
		return
	}
	if rejected {
		runErr = errors.New("assistant: confirmation queue is full")
		s.releaseQueued(call)
		return
	}
	s.pendingMu.Lock()
	item := s.pendingTools[call.ConfirmationNonce]
	s.pendingMu.Unlock()
	if item == nil {
		runErr = errors.New("assistant: confirmation unavailable")
		return
	}
	// Verifying the client card against the trusted stored scope also binds user,
	// session, capability and exact preview. Only stored args/handler can execute.
	ok, err := s.backend.cfg.Confirmation.Signer.Consume(ctx, s.sctx.User, item.scope, call)
	if err != nil || !ok {
		runErr = errors.New("assistant: confirmation rejected")
		s.releaseQueued(call)
		return
	}
	s.pendingMu.Lock()
	delete(s.pendingTools, call.ConfirmationNonce)
	s.pendingMu.Unlock()
	pending = item
	evidence = &ConfirmedToolResult{Call: item.call, Tool: item.tool, Args: append(json.RawMessage(nil), item.args...), InvocationScope: item.invocationScope}
	callCtx, stop := context.WithTimeout(item.invocation, s.backend.cfg.Confirmation.ToolTimeout)
	// 调用继承原提案的可信执行上下文，但进度必须绑定本次确认轮次，不能写回已结束的提案。
	callCtx = agent.WithStatusReporter(callCtx, func(text string) { p.step(agent.Step{Kind: agent.StepStatus, Result: text}, nil) })
	defer stop()
	stopParent := context.AfterFunc(ctx, stop)
	defer stopParent()
	if ctx.Err() != nil {
		stop()
	}
	host := s.sctx
	if err := s.backend.cfg.ToolExecutionGuard(callCtx, &host, item.tool, append(json.RawMessage(nil), item.args...)); err != nil {
		runErr = ihtml.ErrAccessDenied
		return
	}
	if callCtx.Err() != nil {
		runErr = callCtx.Err()
		return
	}
	ref := ihtml.ProposedToolCall{ID: item.call.ID, ConfirmationNonce: item.call.ConfirmationNonce}
	p.mu.Lock()
	p.send(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamToolStart, ToolCall: &ref, ToolID: ref.ID, ToolName: item.tool.Name, ToolStatus: ihtml.ChatToolRunning})
	p.mu.Unlock()
	if ctx.Err() != nil {
		runErr = ctx.Err()
		return
	}
	started = true
	evidence.Result, runErr = item.tool.Handler(callCtx, append(json.RawMessage(nil), item.args...))
	if callCtx.Err() != nil {
		runErr = callCtx.Err()
	}
	// 确认桥接与普通引擎共用工具的只读回执恢复协议；绝不再次调用 Handler。
	// 请求超时后只核对同一 invocation，用户停止/会话关闭则不发起恢复或续办。
	if runErr != nil && item.tool.RecoverResult != nil && replyParent.Err() == nil && s.ctx.Err() == nil {
		reconcile, finish := context.WithTimeout(item.invocation, min(s.backend.cfg.Confirmation.ToolTimeout, 5*time.Second))
		stopReply := context.AfterFunc(replyParent, finish)
		finishRecovery = func() { stopReply(); finish() }
		host := s.sctx
		if err := s.backend.cfg.ToolExecutionGuard(reconcile, &host, item.tool, append(json.RawMessage(nil), item.args...)); err == nil {
			value, ok, err := item.tool.RecoverResult(reconcile, append(json.RawMessage(nil), item.args...))
			if err == nil && ok && value != "" && reconcile.Err() == nil {
				evidence.Result, runErr, recovered = value, nil, true
				// 回执保存/展示也使用独立核对预算，不能继续使用已超时的执行 context。
				ctx = reconcile
			}
		}
	}
}

// 排队与用户停止共用同一把锁；不能借用即将收尾的上一轮 context，也不能在停止后重新排队。
func (s *session) enqueueContinuation(parent context.Context, event *ihtml.ChatClientEvent) bool {
	cleaned, err := cleanEvent(event)
	if err != nil {
		return false
	}
	s.mu.Lock()
	if s.closed || parent.Err() != nil || s.ctx.Err() != nil {
		s.mu.Unlock()
		return false
	}
	// 批量回执的相同续办事件合并为一次重新规划；不同宿主事件保持各自语义。
	// 普通轮次会重新读取权威回执，不需要为同一提示重复调用模型。
	for _, queued := range s.queue {
		if queued.continuation && reflect.DeepEqual(queued.event, cleaned) {
			s.mu.Unlock()
			return true
		}
	}
	if s.queuedOrdinaryTurnsLocked() >= s.backend.cfg.MaxPendingTurns {
		s.queueRejected = true
		s.mu.Unlock()
		return false
	}
	s.queue = append(s.queue, pendingTurn{parent: s.ctx, event: cleaned, continuation: true})
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}

	return true
}

func (s *session) releaseQueued(call ihtml.ProposedToolCall) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	if item := s.pendingTools[call.ConfirmationNonce]; item != nil {
		item.queued = false
	}
}

// Called with mu held; lock order is always mu then pendingMu.
func (s *session) releaseDroppedConfirmationsLocked() {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	for _, queue := range [][]pendingTurn{s.queue, s.rejections} {
		for _, next := range queue {
			if next.event.Type == ihtml.ChatClientConfirmTool && next.event.ToolCall != nil {
				if item := s.pendingTools[next.event.ToolCall.ConfirmationNonce]; item != nil {
					item.queued = false
				}
			}
		}
	}
}

func safeCompleteTool(fn func(context.Context, *ihtml.ChatSessionContext, *ConfirmedToolResult) error, ctx context.Context, sctx *ihtml.ChatSessionContext, evidence *ConfirmedToolResult) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("assistant: CompleteTool panicked")
		}
	}()
	copyContext := *sctx
	copyEvidence := *evidence
	copyEvidence.Call, err = cloneToolCall(evidence.Call)
	if err != nil {
		return err
	}
	copyEvidence.Args = append(json.RawMessage(nil), evidence.Args...)
	return fn(ctx, &copyContext, &copyEvidence)
}
