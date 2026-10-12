package assistant

import (
	"context"
	"errors"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/agent"
)

// NativeConfirmationConfig connects checkpoint approval to host-owned durable
// state. Hooks must honor cancellation and be safe across processes. This bridge
// never accepts a client checkpoint, permissions, transcript, or resume targets.
type NativeConfirmationConfig struct {
	// Pause atomically persists the suspended request/result and exact signed,
	// redacted cards before returning. It must be recoverable if delivery fails.
	// Do not publish final history or mark the durable turn complete here.
	Pause func(context.Context, *ihtml.ChatSessionContext, *agent.TurnRequest, *agent.TurnResult) ([]ihtml.ProposedToolCall, error)
	// PrepareResume authenticates ownership, validates/consumes the exact signed
	// card, records consent, and atomically claims the original suspended turn.
	// Return fresh authorized handlers and original session/invocation binding.
	// False must target rejection, never execute the handler. Concurrent/replayed,
	// expired or revoked consent must fail closed. A failed/crashed claim must not
	// be blindly retried: tool side effects can have an unknown outcome.
	PrepareResume func(context.Context, *ihtml.ChatSessionContext, ihtml.ProposedToolCall, bool) (*agent.TurnRequest, error)
	// Pending reloads unexpired cards on connection/reconnection from trusted
	// durable state. No model runs or browser history replay are performed.
	Pending func(context.Context, *ihtml.ChatSessionContext) ([]ihtml.ProposedToolCall, error)
}

func (s *session) restoreApprovals() {
	defer func() {
		if recover() != nil {
			_ = s.emit(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamTurnError, Error: "Pending approvals unavailable."})
		}
	}()
	cfg := s.backend.cfg.NativeConfirmation
	if cfg == nil {
		return
	}
	host := s.sctx
	cards, err := cfg.Pending(s.ctx, &host)
	if err != nil {
		_ = s.emit(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamTurnError, Error: "Pending approvals unavailable."})
		return
	}
	if len(cards) == 0 {
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	p := newProjection(s, ctx, cancel)
	models, err := s.backend.Models(ctx)
	if err != nil {
		return
	}
	p.modelReady = models.ModelReady
	p.mu.Lock()
	defer p.mu.Unlock()
	p.send(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamTurnStart, RestoredApproval: true})
	if err := p.approvalCards(cards); err != nil {
		p.send(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamTurnError, Error: "Pending approvals unavailable."})
		return
	}
	p.send(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamTurnPaused, Status: "Awaiting confirmation."})
}

func safeNativePause(fn func(context.Context, *ihtml.ChatSessionContext, *agent.TurnRequest, *agent.TurnResult) ([]ihtml.ProposedToolCall, error), ctx context.Context, sc *ihtml.ChatSessionContext, req *agent.TurnRequest, result *agent.TurnResult) (cards []ihtml.ProposedToolCall, err error) {
	defer func() {
		if recover() != nil {
			cards = nil
			err = errors.New("assistant: Pause panicked")
		}
	}()
	return fn(ctx, sc, req, result)
}

// Called with projection.mu held. Card issuance/security remain host-owned.
func (p *projection) approvalCards(cards []ihtml.ProposedToolCall) error {
	if len(cards) == 0 || len(cards) > 256 {
		return errors.New("invalid native approval cards")
	}
	// Validate the whole batch before emitting a partially usable view.
	for _, call := range cards {
		if call.ID == "" || call.Name == "" || !call.RequiresConfirmation || call.ConfirmationToken == "" || call.ConfirmationNonce == "" || call.ApprovalResume != "checkpoint" {
			return errors.New("invalid native approval card")
		}
	}
	for _, call := range cards {
		view, err := cloneToolCall(call)
		if err != nil {
			return err
		}
		p.send(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamConfirmationRequired, ToolCall: &view, ToolID: call.ID, ToolName: call.Name, ToolStatus: ihtml.ChatToolPending, ToolSummary: "Awaiting confirmation; not executed."})
	}
	return p.err
}
