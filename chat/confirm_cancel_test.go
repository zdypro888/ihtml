package chat

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/zdypro888/ihtml"
)

type panickingConfirmationService struct{ ihtml.Service }

func (s *panickingConfirmationService) KVSet(context.Context, string, string, json.RawMessage) error {
	panic("service-super-secret")
}

func confirmationTestSession(backend *Backend, user, sessionID, capabilityScope string) *session {
	return &session{
		backend:         backend,
		user:            user,
		sessionID:       sessionID,
		capabilityScope: capabilityScope,
		emit:            func(ihtml.ChatStreamEvent) error { return nil },
	}
}

func issueSessionConfirmation(t *testing.T, s *session) ihtml.ProposedToolCall {
	t.Helper()
	call, err := s.backend.signer.prepareContext(
		context.Background(),
		s.user,
		newConfirmationScope(s.sessionID, s.capabilityScope),
		ihtml.ProposedToolCall{
			ID: "delete-old", Name: "ui_page_delete", Method: "TOOL", Path: "ui://ui_page_delete",
			RequiresConfirmation: true, Body: map[string]any{"name": "old"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return call
}

func captureSessionEvents(s *session) <-chan ihtml.ChatStreamEvent {
	events := make(chan ihtml.ChatStreamEvent, 64)
	s.emit = func(event ihtml.ChatStreamEvent) error {
		events <- event
		return nil
	}
	return events
}

func waitSessionEvent(t *testing.T, events <-chan ihtml.ChatStreamEvent, eventType string) ihtml.ChatStreamEvent {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-events:
			if event.Type == eventType {
				return event
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for %s", eventType)
		}
	}
}

func assertCorrelatedConfirmationFailure(
	t *testing.T,
	events <-chan ihtml.ChatStreamEvent,
	call ihtml.ProposedToolCall,
) ihtml.ChatStreamEvent {
	t.Helper()
	result := waitSessionEvent(t, events, ihtml.ChatStreamToolResult)
	if result.ToolStatus != ihtml.ChatToolError || result.ToolCall == nil {
		t.Fatalf("confirmation failure = %#v", result)
	}
	if result.ToolCall.ID != call.ID || result.ToolCall.ConfirmationNonce != call.ConfirmationNonce {
		t.Fatalf(
			"confirmation failure correlation = %q/%q, want %q/%q",
			result.ToolCall.ID, result.ToolCall.ConfirmationNonce, call.ID, call.ConfirmationNonce,
		)
	}
	if !strings.Contains(result.ToolSummary, "操作未执行") {
		t.Fatalf("confirmation failure did not state that execution never started: %#v", result)
	}
	done := waitSessionEvent(t, events, ihtml.ChatStreamAssistantDone)
	if !strings.Contains(done.Message, "操作未执行") {
		t.Fatalf("confirmation failure did not terminate with an accurate answer: %#v", done)
	}
	return result
}

func waitApprovalLane(t *testing.T, s *session) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.workMu.Lock()
		done := s.manualDone
		worker := s.manualWorker
		s.workMu.Unlock()
		if !worker && done == nil {
			return
		}
		if done != nil {
			select {
			case <-done:
				return
			case <-time.After(time.Until(deadline)):
				t.Fatal("timed out waiting for approval lane")
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for approval lane")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSessionCancelToolRevokesCardBeforeConfirmReplay(t *testing.T) {
	backend := New(Config{ConfirmSecret: []byte("a long shared cancellation secret for tests")})
	s := confirmationTestSession(backend, "alice", "session-a", "application-a")
	call := issueSessionConfirmation(t, s)

	s.HandleEvent(context.Background(), &ihtml.ChatClientEvent{
		Type: ihtml.ChatClientCancelTool, ToolCall: &call,
	})
	waitApprovalLane(t, s)

	ok, err := backend.signer.consumeContext(
		context.Background(), s.user, newConfirmationScope(s.sessionID, s.capabilityScope), call,
	)
	if err != nil || ok {
		t.Fatalf("confirm replay after cancel_tool was accepted: ok=%v err=%v", ok, err)
	}
}

func TestSessionCancelToolAcknowledgesExactCardOnlyAfterRevocation(t *testing.T) {
	backend := New(Config{ConfirmSecret: []byte("a long shared cancellation secret for tests")})
	s := confirmationTestSession(backend, "alice", "session-a", "application-a")
	events := captureSessionEvents(s)
	call := issueSessionConfirmation(t, s)

	s.HandleEvent(context.Background(), &ihtml.ChatClientEvent{
		Type: ihtml.ChatClientCancelTool, ToolCall: &call,
	})

	ack := waitSessionEvent(t, events, ihtml.ChatStreamCancelToolResult)
	if ack.Type != ihtml.ChatStreamCancelToolResult || ack.ToolStatus != ihtml.ChatToolCancelled {
		t.Fatalf("cancel acknowledgement = %#v", ack)
	}
	if ack.ToolID != call.ID || ack.ConfirmationNonce != call.ConfirmationNonce {
		t.Fatalf("cancel correlation = %q/%q, want %q/%q", ack.ToolID, ack.ConfirmationNonce, call.ID, call.ConfirmationNonce)
	}
	if ack.ToolCall != nil || ack.Error != "" || ack.ToolSummary == "" {
		t.Fatalf("cancel success leaked the card or omitted its result: %#v", ack)
	}
}

func TestSessionCancelToolReportsNonceRaceAsFailure(t *testing.T) {
	backend := New(Config{ConfirmSecret: []byte("a long shared cancellation secret for tests")})
	s := confirmationTestSession(backend, "alice", "session-a", "application-a")
	events := captureSessionEvents(s)
	call := issueSessionConfirmation(t, s)
	if ok, err := backend.signer.consumeContext(
		context.Background(), s.user, newConfirmationScope(s.sessionID, s.capabilityScope), call,
	); err != nil || !ok {
		t.Fatalf("pre-consume confirmation: ok=%v err=%v", ok, err)
	}

	s.HandleEvent(context.Background(), &ihtml.ChatClientEvent{
		Type: ihtml.ChatClientCancelTool, ToolCall: &call,
	})

	ack := waitSessionEvent(t, events, ihtml.ChatStreamCancelToolResult)
	if ack.Type != ihtml.ChatStreamCancelToolResult || ack.ToolStatus != ihtml.ChatToolError || ack.Error == "" {
		t.Fatalf("nonce race must be reported as a cancellation failure: %#v", ack)
	}
	if ack.ToolSummary != "" {
		t.Fatalf("failed cancellation claimed success: %#v", ack)
	}
}

func TestSessionCancelToolInvalidRequestStillGetsBoundedFailureAck(t *testing.T) {
	backend := New(Config{ConfirmSecret: []byte("a long shared cancellation secret for tests")})
	s := confirmationTestSession(backend, "alice", "session-a", "application-a")
	events := captureSessionEvents(s)
	call := ihtml.ProposedToolCall{ID: strings.Repeat("x", 1024), ConfirmationNonce: strings.Repeat("y", 1024)}
	s.HandleEvent(context.Background(), &ihtml.ChatClientEvent{Type: ihtml.ChatClientCancelTool, ToolCall: &call})

	ack := waitSessionEvent(t, events, ihtml.ChatStreamCancelToolResult)
	if ack.ToolStatus != ihtml.ChatToolError {
		t.Fatalf("invalid cancellation acknowledgement = %#v", ack)
	}
	if len(ack.ToolID) > maxConfirmationReferenceBytes || len(ack.ConfirmationNonce) > maxConfirmationReferenceBytes {
		t.Fatalf("unbounded cancellation correlation: %#v", ack)
	}
}

func TestCrossSessionOrApplicationCancelCannotRevokeCard(t *testing.T) {
	backend := New(Config{ConfirmSecret: []byte("a long shared cancellation secret for tests")})
	issuer := confirmationTestSession(backend, "alice", "session-a", "application-a")
	call := issueSessionConfirmation(t, issuer)

	wrongSession := confirmationTestSession(backend, "alice", "session-b", "application-a")
	wrongSession.HandleEvent(context.Background(), &ihtml.ChatClientEvent{
		Type: ihtml.ChatClientCancelTool, ToolCall: &call,
	})
	wrongApp := confirmationTestSession(backend, "alice", "session-b", "application-b")
	wrongApp.HandleEvent(context.Background(), &ihtml.ChatClientEvent{
		Type: ihtml.ChatClientCancelTool, ToolCall: &call,
	})
	waitApprovalLane(t, wrongSession)
	waitApprovalLane(t, wrongApp)

	ok, err := backend.signer.consumeContext(
		context.Background(), issuer.user, newConfirmationScope(issuer.sessionID, issuer.capabilityScope), call,
	)
	if err != nil || !ok {
		t.Fatalf("cross-scope cancellation burned issuer card: ok=%v err=%v", ok, err)
	}
}

func prepareOrderedApproval(t *testing.T, s *session, id, key string) ihtml.ProposedToolCall {
	t.Helper()
	call, err := s.backend.signer.prepareContext(
		context.Background(), s.user, newConfirmationScope(s.sessionID, s.capabilityScope),
		ihtml.ProposedToolCall{
			ID: id, Name: "ui_kv_set", Method: "TOOL", Path: "ui://ui_kv_set",
			RequiresConfirmation: true, Body: map[string]any{"key": key, "value": "written"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return call
}

func TestApprovalWorkerPreservesCancelAndConfirmFrameOrder(t *testing.T) {
	t.Run("cancel then confirm", func(t *testing.T) {
		service := ihtml.New(ihtml.NewMemoryStore())
		s := newLifecycleTestSession(service)
		events := captureSessionEvents(s)
		call := prepareOrderedApproval(t, s, "cancel-first", "cancel-first-key")

		s.HandleEvent(context.Background(), &ihtml.ChatClientEvent{Type: ihtml.ChatClientCancelTool, ToolCall: &call})
		s.HandleEvent(context.Background(), &ihtml.ChatClientEvent{Type: ihtml.ChatClientConfirmTool, ToolCall: &call})
		ack := waitSessionEvent(t, events, ihtml.ChatStreamCancelToolResult)
		waitApprovalLane(t, s)

		if ack.ToolStatus != ihtml.ChatToolCancelled {
			t.Fatalf("cancel-first acknowledgement = %#v", ack)
		}
		if value, err := service.KVGet(context.Background(), s.user, "cancel-first-key"); err == nil {
			t.Fatalf("confirm overtook cancellation and wrote %s", value)
		}
	})

	t.Run("confirm then cancel", func(t *testing.T) {
		service := ihtml.New(ihtml.NewMemoryStore())
		s := newLifecycleTestSession(service)
		events := captureSessionEvents(s)
		call := prepareOrderedApproval(t, s, "confirm-first", "confirm-first-key")

		s.HandleEvent(context.Background(), &ihtml.ChatClientEvent{Type: ihtml.ChatClientConfirmTool, ToolCall: &call})
		s.HandleEvent(context.Background(), &ihtml.ChatClientEvent{Type: ihtml.ChatClientCancelTool, ToolCall: &call})
		ack := waitSessionEvent(t, events, ihtml.ChatStreamCancelToolResult)
		waitApprovalLane(t, s)

		if ack.ToolStatus != ihtml.ChatToolError || ack.Error == "" {
			t.Fatalf("confirm-first cancellation must report the lost nonce race: %#v", ack)
		}
		value, err := service.KVGet(context.Background(), s.user, "confirm-first-key")
		if err != nil || string(value) != `"written"` {
			t.Fatalf("confirmed write = %s, %v", value, err)
		}
	})
}

type blockingConsumeNonceStore struct {
	base    *memoryConfirmNonceStore
	started chan string
	release chan struct{}
}

func (s *blockingConsumeNonceStore) Put(ctx context.Context, nonce string, expiresAt time.Time) error {
	return s.base.Put(ctx, nonce, expiresAt)
}

func (s *blockingConsumeNonceStore) Consume(ctx context.Context, nonce string, now time.Time) (bool, error) {
	s.started <- nonce
	<-s.release // deliberately ignores ctx; the bounded host lane must contain it
	return s.base.Consume(ctx, nonce, now)
}

func TestBlockingCancelNonceStoreNeverBlocksWebSocketReader(t *testing.T) {
	store := &blockingConsumeNonceStore{
		base: newMemoryConfirmNonceStore(), started: make(chan string, 2), release: make(chan struct{}),
	}
	backend := New(Config{
		ConfirmSecret: []byte("a long shared cancellation secret for tests"), ConfirmNonceStore: store,
	})
	s := confirmationTestSession(backend, "alice", "session-a", "application-a")
	events := captureSessionEvents(s)
	call := issueSessionConfirmation(t, s)
	second := issueSessionConfirmation(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()

	startedAt := time.Now()
	s.HandleEvent(ctx, &ihtml.ChatClientEvent{Type: ihtml.ChatClientCancelTool, ToolCall: &call})
	if elapsed := time.Since(startedAt); elapsed > 25*time.Millisecond {
		t.Fatalf("cancel_tool blocked websocket reader for %s", elapsed)
	}
	select {
	case nonce := <-store.started:
		if nonce != call.ConfirmationNonce {
			t.Fatalf("first nonce = %q", nonce)
		}
	case <-time.After(time.Second):
		t.Fatal("nonce store was not called")
	}
	s.HandleEvent(context.Background(), &ihtml.ChatClientEvent{Type: ihtml.ChatClientCancelTool, ToolCall: &second})
	select {
	case nonce := <-store.started:
		t.Fatalf("later approval %q overtook blocked nonce CAS", nonce)
	case <-time.After(100 * time.Millisecond):
	}
	close(store.release)
	firstAck := waitSessionEvent(t, events, ihtml.ChatStreamCancelToolResult)
	if firstAck.ToolStatus != ihtml.ChatToolError || firstAck.Error == "" {
		t.Fatalf("blocked store acknowledgement = %#v", firstAck)
	}
	select {
	case nonce := <-store.started:
		if nonce != second.ConfirmationNonce {
			t.Fatalf("second nonce = %q", nonce)
		}
	case <-time.After(time.Second):
		t.Fatal("second cancellation did not run after the first returned")
	}
	secondAck := waitSessionEvent(t, events, ihtml.ChatStreamCancelToolResult)
	if secondAck.ToolStatus != ihtml.ChatToolCancelled {
		t.Fatalf("second cancellation acknowledgement = %#v", secondAck)
	}
	waitApprovalLane(t, s)
}

func TestFullApprovalQueueReturnsCorrelatedCancelFailure(t *testing.T) {
	backend := New(Config{ConfirmSecret: []byte("a long shared cancellation secret for tests")})
	s := confirmationTestSession(backend, "alice", "session-a", "application-a")
	events := captureSessionEvents(s)
	call := issueSessionConfirmation(t, s)

	s.workMu.Lock()
	for index := 0; index < maxPendingConfirmations; index++ {
		ctx, cancel := context.WithCancel(context.Background())
		s.manualQueue = append(s.manualQueue, pendingConfirmation{ctx: ctx, cancel: cancel})
	}
	s.workMu.Unlock()
	s.HandleEvent(context.Background(), &ihtml.ChatClientEvent{Type: ihtml.ChatClientCancelTool, ToolCall: &call})

	ack := waitSessionEvent(t, events, ihtml.ChatStreamCancelToolResult)
	if ack.ToolStatus != ihtml.ChatToolError || ack.ToolID != call.ID || ack.ConfirmationNonce != call.ConfirmationNonce {
		t.Fatalf("full queue acknowledgement = %#v", ack)
	}
	s.workMu.Lock()
	for _, pending := range s.manualQueue {
		pending.cancel()
	}
	s.manualQueue = nil
	s.workMu.Unlock()
}

func TestConfirmationRejectionsTerminateTheExactToolCard(t *testing.T) {
	t.Run("approval rate", func(t *testing.T) {
		backend := New(Config{ConfirmSecret: []byte("a long shared confirmation secret for tests")})
		s := confirmationTestSession(backend, "alice", "session-a", "application-a")
		events := captureSessionEvents(s)
		call := issueSessionConfirmation(t, s)
		s.workMu.Lock()
		s.approvalWindow = time.Now()
		s.approvalEvents = maxApprovalEventsMinute
		s.workMu.Unlock()

		s.HandleEvent(context.Background(), &ihtml.ChatClientEvent{
			Type: ihtml.ChatClientConfirmTool, ToolCall: &call,
		})
		assertCorrelatedConfirmationFailure(t, events, call)
	})

	t.Run("full queue", func(t *testing.T) {
		backend := New(Config{ConfirmSecret: []byte("a long shared confirmation secret for tests")})
		s := confirmationTestSession(backend, "alice", "session-a", "application-a")
		events := captureSessionEvents(s)
		call := issueSessionConfirmation(t, s)
		s.workMu.Lock()
		for index := 0; index < maxPendingConfirmations; index++ {
			ctx, cancel := context.WithCancel(context.Background())
			s.manualQueue = append(s.manualQueue, pendingConfirmation{ctx: ctx, cancel: cancel})
		}
		s.workMu.Unlock()

		s.HandleEvent(context.Background(), &ihtml.ChatClientEvent{
			Type: ihtml.ChatClientConfirmTool, ToolCall: &call,
		})
		assertCorrelatedConfirmationFailure(t, events, call)

		s.workMu.Lock()
		for _, pending := range s.manualQueue {
			pending.cancel()
		}
		s.manualQueue = nil
		s.workMu.Unlock()
	})

	t.Run("closed session", func(t *testing.T) {
		backend := New(Config{ConfirmSecret: []byte("a long shared confirmation secret for tests")})
		s := confirmationTestSession(backend, "alice", "session-a", "application-a")
		events := captureSessionEvents(s)
		call := issueSessionConfirmation(t, s)
		s.workMu.Lock()
		s.closed = true
		s.workMu.Unlock()

		s.HandleEvent(context.Background(), &ihtml.ChatClientEvent{
			Type: ihtml.ChatClientConfirmTool, ToolCall: &call,
		})
		assertCorrelatedConfirmationFailure(t, events, call)
	})
}

func TestConfirmationCapacityFailureTerminatesExactCardWithoutExecuting(t *testing.T) {
	service := ihtml.New(ihtml.NewMemoryStore())
	s := newLifecycleTestSession(service)
	s.backend.operations = newOperationLimiter(1)
	events := captureSessionEvents(s)
	call := prepareOrderedApproval(t, s, "capacity-confirmation", "capacity-confirmation-key")
	occupier := &session{backend: s.backend}
	release, err := occupier.tryBeginOperation()
	if err != nil {
		t.Fatal(err)
	}

	s.HandleEvent(context.Background(), &ihtml.ChatClientEvent{
		Type: ihtml.ChatClientConfirmTool, ToolCall: &call,
	})
	assertCorrelatedConfirmationFailure(t, events, call)
	waitApprovalLane(t, s)
	release()

	if value, err := service.KVGet(context.Background(), s.user, "capacity-confirmation-key"); err == nil {
		t.Fatalf("capacity-rejected confirmation executed: %s", value)
	}
}

func TestGeneralCancelReportsRequestRatherThanGuaranteedCompletion(t *testing.T) {
	s := newLifecycleTestSession(ihtml.New(ihtml.NewMemoryStore()))
	events := captureSessionEvents(s)
	s.HandleEvent(context.Background(), &ihtml.ChatClientEvent{Type: ihtml.ChatClientCancel})

	status := waitSessionEvent(t, events, ihtml.ChatStreamAssistantStatus)
	if !strings.Contains(status.Status, "已请求停止") || strings.Contains(status.Status, "已取消当前请求") {
		t.Fatalf("cancel status over-promised completion: %#v", status)
	}
}

func TestConfirmedServicePanicIsCorrelatedAndReportsUnknownState(t *testing.T) {
	service := &panickingConfirmationService{Service: ihtml.New(ihtml.NewMemoryStore())}
	s := newLifecycleTestSession(service)
	events := captureSessionEvents(s)
	call := prepareOrderedApproval(t, s, "panic-confirmation", "panic-confirmation-key")

	s.HandleEvent(context.Background(), &ihtml.ChatClientEvent{
		Type: ihtml.ChatClientConfirmTool, ToolCall: &call,
	})
	result := waitSessionEvent(t, events, ihtml.ChatStreamToolResult)
	if result.ToolStatus != ihtml.ChatToolError || result.ToolCall == nil {
		t.Fatalf("panic tool result = %#v", result)
	}
	if result.ToolCall.ID != call.ID || result.ToolCall.ConfirmationNonce != call.ConfirmationNonce {
		t.Fatalf("panic result correlation = %#v", result.ToolCall)
	}
	if !strings.Contains(result.ToolSummary, "最终状态无法确认") || strings.Contains(result.ToolSummary, "super-secret") {
		t.Fatalf("panic result did not safely report unknown state: %#v", result)
	}
	done := waitSessionEvent(t, events, ihtml.ChatStreamAssistantDone)
	if !strings.Contains(done.Message, "最终状态无法确认") || strings.Contains(done.Message, "super-secret") {
		t.Fatalf("panic terminal answer = %#v", done)
	}
	waitApprovalLane(t, s)
}
