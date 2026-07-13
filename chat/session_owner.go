package chat

import (
	"context"

	"github.com/zdypro888/ihtml"
)

// claimSessionOwner installs s as the current in-process owner immediately.
// Pending successors inherit the original active predecessor's barrier rather
// than each other's shutdownDone channel. Therefore rapid A -> B -> C reconnects
// retain one cancellable waiter instead of an unbounded goroutine chain.
func (b *Backend) claimSessionOwner(s *session) {
	if b == nil || s == nil {
		if s != nil {
			s.activateSessionOwner()
		}
		return
	}

	b.ownerMu.Lock()
	if b.owners == nil {
		b.owners = make(map[string]*session)
	}
	previous := b.owners[s.sessionID]
	barrier := b.ownerBarriers[s.sessionID]
	if previous != nil && previous != s {
		// Close marks the previous session closed synchronously. Its cleanup is
		// asynchronous, so calling it under ownerMu cannot wait on ownerMu.
		previous.Close()
		previous.workMu.Lock()
		previousActivated := previous.ownerActivated || previous.ownerIsReadyLocked()
		inherited := previous.ownerBarrier
		previous.workMu.Unlock()
		if previousActivated && previous.shutdownDone != nil {
			barrier = previous.shutdownDone
		} else if inherited != nil {
			barrier = inherited
		}
	}
	if ownerBarrierClosed(barrier) {
		barrier = nil
	}
	s.workMu.Lock()
	s.ownerBarrier = barrier
	s.workMu.Unlock()
	b.owners[s.sessionID] = s
	if barrier != nil {
		if b.ownerBarriers == nil {
			b.ownerBarriers = make(map[string]<-chan struct{})
		}
		b.ownerBarriers[s.sessionID] = barrier
	} else if b.ownerBarriers != nil {
		delete(b.ownerBarriers, s.sessionID)
	}
	b.ownerMu.Unlock()

	if barrier == nil {
		b.activateClaimedOwner(s, nil)
		return
	}
	cancel := s.ownerWaitCancel
	go func() {
		if cancel == nil {
			<-barrier
			b.activateClaimedOwner(s, barrier)
			return
		}
		select {
		case <-barrier:
			b.activateClaimedOwner(s, barrier)
		case <-cancel:
		}
	}()
}

func ownerBarrierClosed(barrier <-chan struct{}) bool {
	if barrier == nil {
		return true
	}
	select {
	case <-barrier:
		return true
	default:
		return false
	}
}

func (b *Backend) activateClaimedOwner(s *session, barrier <-chan struct{}) {
	if b == nil || s == nil {
		return
	}
	b.ownerMu.Lock()
	current := b.owners[s.sessionID] == s
	if current && barrier != nil && b.ownerBarriers[s.sessionID] == barrier {
		delete(b.ownerBarriers, s.sessionID)
	}
	b.ownerMu.Unlock()
	if current {
		s.activateSessionOwner()
	}
}

// activateSessionOwner closes the takeover barrier and atomically marks any
// deferred user-message builder as active. Holding workMu across both steps
// prevents a just-arriving frame from overtaking messages received while the
// predecessor was still shutting down.
func (s *session) activateSessionOwner() {
	if s == nil {
		return
	}
	s.workMu.Lock()
	if s.closed {
		s.pendingIngress = nil
		s.workMu.Unlock()
		return
	}
	if s.ownerReady != nil && !s.ownerIsReadyLocked() {
		close(s.ownerReady)
	}
	s.ownerActivated = true
	startIngress := s.startIngressLocked()
	s.workMu.Unlock()

	if startIngress {
		go s.drainUserMessages()
		return
	}
	s.drainPendingTurns()
}

// enqueueUserMessage puts every raw frame through one asynchronous FIFO. The
// WebSocket reader never performs a turn build itself, and a successor owner
// keeps this queue behind the predecessor's real shutdown barrier.
func (s *session) enqueueUserMessage(parent context.Context, ev *ihtml.ChatClientEvent) error {
	if s == nil || ev == nil {
		return nil
	}
	if parent == nil {
		parent = context.Background()
	}
	s.workMu.Lock()
	if s.closed {
		s.workMu.Unlock()
		return errTurnSuperseded
	}
	if len(s.pendingIngress)+len(s.pendingTurns) >= maxPendingTurns {
		s.workMu.Unlock()
		return errPendingTurnQueueFull
	}
	copyEvent := *ev
	copyEvent.Messages = append([]ihtml.ChatMessage(nil), ev.Messages...)
	if ev.ToolsEnabled != nil {
		value := *ev.ToolsEnabled
		copyEvent.ToolsEnabled = &value
	}
	s.pendingIngress = append(s.pendingIngress, pendingUserMessage{parent: parent, event: copyEvent})
	startIngress := s.startIngressLocked()
	s.workMu.Unlock()
	if startIngress {
		go s.drainUserMessages()
	}
	return nil
}

// startIngressLocked reserves the single build worker. Callers hold workMu and
// start drainUserMessages only after unlocking.
func (s *session) startIngressLocked() bool {
	if s == nil || s.closed || s.ingressDraining || len(s.pendingIngress) == 0 || !s.ownerIsReadyLocked() {
		return false
	}
	s.ingressDraining = true
	s.ingressDone = make(chan struct{})
	return true
}

func (s *session) drainUserMessages() {
	for {
		s.workMu.Lock()
		if s.closed || len(s.pendingIngress) == 0 {
			if s.closed {
				s.pendingIngress = nil
			}
			done := s.ingressDone
			s.ingressDone = nil
			s.ingressDraining = false
			if done != nil {
				close(done)
			}
			s.workMu.Unlock()
			s.drainPendingTurns()
			return
		}
		pending := s.pendingIngress[0]
		s.pendingIngress[0] = pendingUserMessage{}
		s.pendingIngress = s.pendingIngress[1:]
		buildCtx, buildCancel := context.WithCancel(pending.parent)
		s.ingressActiveCancel = buildCancel
		s.workMu.Unlock()

		if buildCtx.Err() == nil {
			s.runUserMessageBuild(buildCtx, &pending.event)
		}
		buildCancel()
		s.workMu.Lock()
		s.ingressActiveCancel = nil
		s.workMu.Unlock()
	}
}

// runUserMessageBuild holds both the per-session real-work barrier and the
// Backend-wide operation slot for the complete build. In particular, an
// inventory Service that ignores cancellation keeps the owner lease and its
// capacity slot until it actually returns.
func (s *session) runUserMessageBuild(ctx context.Context, ev *ihtml.ChatClientEvent) {
	done, err := s.tryBeginOperation()
	if err != nil {
		if ctx.Err() == nil {
			_ = s.emit(ihtml.ChatStreamEvent{Type: "turn_error", Error: userFacingError(err), ModelReady: true})
		}
		return
	}
	defer done()
	defer func() {
		if recover() != nil && ctx.Err() == nil {
			_ = s.emit(ihtml.ChatStreamEvent{Type: "turn_error", Error: "对话初始化失败，请稍后重试。", ModelReady: true})
		}
	}()
	if err := s.enqueueTurn(ctx, ev); err != nil && ctx.Err() == nil && err != errTurnSuperseded {
		_ = s.emit(ihtml.ChatStreamEvent{Type: "turn_error", Error: userFacingError(err), ModelReady: true})
	}
}

// releaseSessionOwner uses identity rather than SessionID alone. Consequently,
// a delayed Close from an old connection can never remove its successor's
// lease from the owner map.
func (b *Backend) releaseSessionOwner(s *session) {
	if b == nil || s == nil {
		return
	}
	b.ownerMu.Lock()
	if b.owners[s.sessionID] == s {
		delete(b.owners, s.sessionID)
	}
	if barrier := b.ownerBarriers[s.sessionID]; ownerBarrierClosed(barrier) {
		delete(b.ownerBarriers, s.sessionID)
	}
	b.ownerMu.Unlock()
}

func (b *Backend) clearClosedOwnerBarrier(sessionID string, barrier <-chan struct{}) {
	if b == nil || barrier == nil {
		return
	}
	b.ownerMu.Lock()
	if b.ownerBarriers[sessionID] == barrier && ownerBarrierClosed(barrier) {
		delete(b.ownerBarriers, sessionID)
	}
	b.ownerMu.Unlock()
}

// ownerIsReadyLocked reports the non-blocking ownership state. Callers use it
// while holding workMu so enqueue paths can defer work without waiting.
func (s *session) ownerIsReadyLocked() bool {
	if s == nil || s.ownerReady == nil {
		return true
	}
	select {
	case <-s.ownerReady:
		return true
	default:
		return false
	}
}

func (s *session) waitForOwnerReady() bool {
	if s == nil || s.ownerReady == nil {
		return true
	}
	if s.ownerWaitCancel == nil {
		<-s.ownerReady
		return true
	}
	select {
	case <-s.ownerReady:
		return true
	case <-s.ownerWaitCancel:
		return false
	}
}
