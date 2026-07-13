package ihtml

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type recordingChatWSWriter struct {
	mu        sync.Mutex
	messages  [][]byte
	controls  [][]byte
	closed    int
	deadlines int
}

type blockingChatWSWriter struct {
	recordingChatWSWriter
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *blockingChatWSWriter) WriteMessage(kind int, payload []byte) error {
	if err := w.recordingChatWSWriter.WriteMessage(kind, payload); err != nil {
		return err
	}
	w.once.Do(func() { close(w.started) })
	<-w.release
	return nil
}

type panickingJSONMarshaler struct{}

func (panickingJSONMarshaler) MarshalJSON() ([]byte, error) {
	panic("untrusted MarshalJSON must not run during egress preflight")
}

type panickingTextMapKey string

func (panickingTextMapKey) MarshalText() ([]byte, error) {
	panic("untrusted map-key MarshalText must not run during egress preflight")
}

func (w *recordingChatWSWriter) SetWriteDeadline(time.Time) error {
	w.mu.Lock()
	w.deadlines++
	w.mu.Unlock()
	return nil
}

func (w *recordingChatWSWriter) WriteMessage(_ int, payload []byte) error {
	w.mu.Lock()
	w.messages = append(w.messages, append([]byte(nil), payload...))
	w.mu.Unlock()
	return nil
}

func (w *recordingChatWSWriter) WriteControl(_ int, payload []byte, _ time.Time) error {
	w.mu.Lock()
	w.controls = append(w.controls, append([]byte(nil), payload...))
	w.mu.Unlock()
	return nil
}

func (w *recordingChatWSWriter) Close() error {
	w.mu.Lock()
	w.closed++
	w.mu.Unlock()
	return nil
}

func (w *recordingChatWSWriter) snapshot() (messages, controls [][]byte, closed int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, message := range w.messages {
		messages = append(messages, append([]byte(nil), message...))
	}
	for _, control := range w.controls {
		controls = append(controls, append([]byte(nil), control...))
	}
	return messages, controls, w.closed
}

func testChatWSEgressLimits() chatWSEgressLimits {
	return chatWSEgressLimits{
		maxFrameBytes:       64 << 10,
		maxTurnEvents:       100,
		maxTurnBytes:        2 << 20,
		maxConnectionEvents: 1000,
		maxConnectionBytes:  8 << 20,
		maxJSONDepth:        chatWSMaxJSONDepth,
		maxJSONNodes:        chatWSMaxJSONNodes,
	}
}

func TestChatWSEgressAllowsNormalSnapshotAndConfirmation(t *testing.T) {
	writer := &recordingChatWSWriter{}
	guard := newChatWSEgressGuard(writer, &sync.Mutex{})
	if err := guard.beginTurn(); err != nil {
		t.Fatal(err)
	}

	answer := strings.Repeat("a", 256<<10) // 256 KiB assistant snapshot.
	if err := guard.emit(ChatStreamEvent{
		Type:          ChatStreamAssistantDone,
		Message:       answer,
		ContentFormat: ChatContentFormatHTML,
		ModelReady:    true,
	}); err != nil {
		t.Fatalf("256 KiB assistant snapshot should pass: %v", err)
	}
	if err := guard.beginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := guard.emit(ChatStreamEvent{
		Type:   ChatStreamConfirmationRequired,
		TurnID: "untrusted-backend-turn",
		ToolCall: &ProposedToolCall{
			ID:                   "update-weather",
			Name:                 "ui_update",
			Method:               "POST",
			Path:                 "/api/items",
			Query:                map[string]any{"page": "dashboard", "preview": true},
			Body:                 map[string]any{"title": "天气", "values": []any{18, 21, 20}, "at": time.Unix(1, 0).UTC()},
			Risk:                 ChatRiskMedium,
			RequiresConfirmation: true,
			Reason:               "更新现有模块",
			// Exercise a large atomic card below the signing layer's 896-KiB
			// ceiling, leaving room for the event envelope and future fields.
			ConfirmationToken: strings.Repeat("t", 880<<10),
		},
		ModelReady: true,
	}); err != nil {
		t.Fatalf("ordinary confirmation card should pass: %v", err)
	}

	messages, controls, closed := writer.snapshot()
	if len(messages) != 2 || len(controls) != 0 || closed != 0 {
		t.Fatalf("normal writes = messages:%d controls:%d closed:%d", len(messages), len(controls), closed)
	}
	for index, payload := range messages {
		if len(payload) >= 1<<20 {
			t.Fatalf("message %d is not strictly below 1 MiB: %d", index, len(payload))
		}
		if !json.Valid(payload) {
			t.Fatalf("message %d is invalid JSON", index)
		}
	}
}

func TestChatWSEgressOversizedFrameLocksAfterOneTerminalError(t *testing.T) {
	writer := &recordingChatWSWriter{}
	guard := newChatWSEgressGuard(writer, &sync.Mutex{})
	if err := guard.beginTurn(); err != nil {
		t.Fatal(err)
	}
	err := guard.emit(ChatStreamEvent{
		Type:    ChatStreamAssistantDone,
		TurnID:  "attacker-selected-turn",
		Message: strings.Repeat("x", chatWSMaxFrameBytes),
	})
	if !errors.Is(err, errChatWSOutputLimit) {
		t.Fatalf("oversized error = %v", err)
	}

	messages, controls, closed := writer.snapshot()
	if len(messages) != 1 || len(controls) != 1 || closed != 1 {
		t.Fatalf("terminal writes = messages:%d controls:%d closed:%d", len(messages), len(controls), closed)
	}
	var terminal ChatStreamEvent
	if err := json.Unmarshal(messages[0], &terminal); err != nil {
		t.Fatalf("decode terminal event: %v", err)
	}
	if terminal.Type != ChatStreamTurnError || terminal.Error == "" || terminal.TurnID != "" {
		t.Fatalf("terminal event leaked untrusted metadata or is incomplete: %+v", terminal)
	}
	if len(messages[0]) >= 1<<20 {
		t.Fatalf("terminal frame is too large: %d", len(messages[0]))
	}

	// Once locked, emit must return before inspecting this cyclic value and must
	// not write or close a second time.
	cycle := map[string]any{}
	cycle["self"] = cycle
	err = guard.emit(ChatStreamEvent{Type: ChatStreamToolStart, ToolCall: &ProposedToolCall{Body: cycle}})
	if !errors.Is(err, errChatWSOutputClosed) {
		t.Fatalf("subsequent emit error = %v", err)
	}
	afterMessages, afterControls, afterClosed := writer.snapshot()
	if len(afterMessages) != len(messages) || len(afterControls) != len(controls) || afterClosed != closed {
		t.Fatalf("locked guard wrote again: messages:%d controls:%d closed:%d", len(afterMessages), len(afterControls), afterClosed)
	}
}

func TestChatWSEgressLockedFastPathDoesNotWaitForTerminalWrite(t *testing.T) {
	writer := &blockingChatWSWriter{started: make(chan struct{}), release: make(chan struct{})}
	guard := newChatWSEgressGuard(writer, &sync.Mutex{})
	firstResult := make(chan error, 1)
	go func() {
		firstResult <- guard.emit(ChatStreamEvent{
			Type:    ChatStreamAssistantDone,
			Message: strings.Repeat("x", chatWSMaxFrameBytes),
		})
	}()

	released := false
	defer func() {
		if !released {
			close(writer.release)
		}
	}()
	select {
	case <-writer.started:
	case <-time.After(time.Second):
		t.Fatal("terminal write did not start")
	}

	// The first goroutine is still blocked in network I/O. A later Emit must
	// observe the atomic terminal latch without waiting on writeMu.
	secondResult := make(chan error, 1)
	go func() {
		cycle := map[string]any{}
		cycle["self"] = cycle
		secondResult <- guard.emit(ChatStreamEvent{Type: ChatStreamToolStart, ToolCall: &ProposedToolCall{Body: cycle}})
	}()
	select {
	case err := <-secondResult:
		if !errors.Is(err, errChatWSOutputClosed) {
			t.Fatalf("fast-path error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("locked Emit waited behind terminal network write")
	}

	close(writer.release)
	released = true
	select {
	case err := <-firstResult:
		if !errors.Is(err, errChatWSOutputLimit) {
			t.Fatalf("first emit error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first Emit did not finish after releasing writer")
	}
}

func TestChatWSEgressRejectsCyclicAndDeepToolBodies(t *testing.T) {
	t.Run("cyclic", func(t *testing.T) {
		writer := &recordingChatWSWriter{}
		guard := newChatWSEgressGuard(writer, &sync.Mutex{})
		cycle := map[string]any{}
		cycle["self"] = cycle
		err := guard.emit(ChatStreamEvent{
			Type:     ChatStreamConfirmationRequired,
			ToolCall: &ProposedToolCall{Body: cycle},
		})
		if !errors.Is(err, errChatWSOutputLimit) {
			t.Fatalf("cyclic body error = %v", err)
		}
		messages, controls, closed := writer.snapshot()
		if len(messages) != 1 || len(controls) != 1 || closed != 1 {
			t.Fatalf("cyclic terminal writes = messages:%d controls:%d closed:%d", len(messages), len(controls), closed)
		}
	})

	t.Run("deep", func(t *testing.T) {
		writer := &recordingChatWSWriter{}
		guard := newChatWSEgressGuard(writer, &sync.Mutex{})
		var body any = "leaf"
		for depth := 0; depth < chatWSMaxJSONDepth+2; depth++ {
			body = []any{body}
		}
		err := guard.emit(ChatStreamEvent{
			Type:     ChatStreamToolStart,
			ToolCall: &ProposedToolCall{Body: body},
		})
		if !errors.Is(err, errChatWSOutputLimit) {
			t.Fatalf("deep body error = %v", err)
		}
	})

	t.Run("custom_marshaler_is_not_invoked", func(t *testing.T) {
		writer := &recordingChatWSWriter{}
		guard := newChatWSEgressGuard(writer, &sync.Mutex{})
		err := guard.emit(ChatStreamEvent{
			Type:     ChatStreamToolStart,
			ToolCall: &ProposedToolCall{Body: panickingJSONMarshaler{}},
		})
		if !errors.Is(err, errChatWSOutputLimit) {
			t.Fatalf("custom marshaler error = %v", err)
		}
	})

	t.Run("custom_map_key_marshaler_is_not_invoked", func(t *testing.T) {
		writer := &recordingChatWSWriter{}
		guard := newChatWSEgressGuard(writer, &sync.Mutex{})
		err := guard.emit(ChatStreamEvent{
			Type: ChatStreamToolStart,
			ToolCall: &ProposedToolCall{Body: map[panickingTextMapKey]any{
				"unsafe": "value",
			}},
		})
		if !errors.Is(err, errChatWSOutputLimit) {
			t.Fatalf("custom map key error = %v", err)
		}
	})
}

func TestChatWSEgressBackendIDsCannotResetTurnQuota(t *testing.T) {
	limits := testChatWSEgressLimits()
	limits.maxTurnEvents = 3
	writer := &recordingChatWSWriter{}
	guard := newChatWSEgressGuardWithLimits(writer, &sync.Mutex{}, limits)
	if err := guard.beginTurn(); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 3; index++ {
		if err := guard.emit(ChatStreamEvent{
			Type:   ChatStreamAssistantDelta,
			RunID:  "forged-run-" + strings.Repeat("x", index),
			TurnID: "forged-turn-" + strings.Repeat("y", index),
			Delta:  "chunk",
		}); err != nil {
			t.Fatalf("chunk %d: %v", index, err)
		}
		if index < 2 {
			// More accepted user messages while the first backend task is still
			// streaming must queue, not reset the active turn bucket.
			if err := guard.beginTurn(); err != nil {
				t.Fatalf("queue turn %d: %v", index, err)
			}
		}
	}
	if guard.activeTurnKey != 1 || guard.pendingTurns != 2 {
		t.Fatalf("backend IDs/client pipelining reset active turn: key=%d pending=%d", guard.activeTurnKey, guard.pendingTurns)
	}
	if err := guard.emit(ChatStreamEvent{Type: ChatStreamAssistantDelta, TurnID: "brand-new-forged-turn", Delta: "overflow"}); !errors.Is(err, errChatWSOutputLimit) {
		t.Fatalf("event flood error = %v", err)
	}
	messages, controls, closed := writer.snapshot()
	if len(messages) != 4 || len(controls) != 1 || closed != 1 {
		t.Fatalf("event quota writes = messages:%d controls:%d closed:%d", len(messages), len(controls), closed)
	}
}

func TestChatWSEgressTurnAndConnectionBudgets(t *testing.T) {
	t.Run("queued_turn_activates_only_at_backend_terminal", func(t *testing.T) {
		writer := &recordingChatWSWriter{}
		guard := newChatWSEgressGuardWithLimits(writer, &sync.Mutex{}, testChatWSEgressLimits())
		if err := guard.beginTurn(); err != nil {
			t.Fatal(err)
		}
		if err := guard.emit(ChatStreamEvent{Type: ChatStreamAssistantDelta, Delta: "first"}); err != nil {
			t.Fatal(err)
		}
		if err := guard.beginTurn(); err != nil {
			t.Fatal(err)
		}
		if guard.activeTurnKey != 1 || guard.pendingTurns != 1 || guard.turnEvents != 1 {
			t.Fatalf("queued turn reset early: key=%d pending=%d events=%d", guard.activeTurnKey, guard.pendingTurns, guard.turnEvents)
		}
		if err := guard.emitInternal(ChatStreamEvent{Type: ChatStreamTurnError, Error: "rate limited"}); err != nil {
			t.Fatal(err)
		}
		if guard.activeTurnKey != 1 || guard.pendingTurns != 1 {
			t.Fatalf("internal error completed backend turn: key=%d pending=%d", guard.activeTurnKey, guard.pendingTurns)
		}
		if err := guard.emit(ChatStreamEvent{Type: ChatStreamAssistantDone, Message: "done"}); err != nil {
			t.Fatal(err)
		}
		if guard.activeTurnKey != 2 || guard.pendingTurns != 0 || !guard.turnActive || guard.turnEvents != 0 {
			t.Fatalf("queued turn did not activate at terminal: key=%d pending=%d active=%v events=%d",
				guard.activeTurnKey, guard.pendingTurns, guard.turnActive, guard.turnEvents)
		}
	})

	t.Run("server_turn_reset_does_not_reset_connection", func(t *testing.T) {
		limits := testChatWSEgressLimits()
		limits.maxTurnEvents = 2
		limits.maxConnectionEvents = 4
		writer := &recordingChatWSWriter{}
		guard := newChatWSEgressGuardWithLimits(writer, &sync.Mutex{}, limits)
		for turn := 0; turn < 2; turn++ {
			if err := guard.beginTurn(); err != nil {
				t.Fatal(err)
			}
			for event := 0; event < 2; event++ {
				typeName := ChatStreamAssistantDelta
				if event == 1 {
					typeName = ChatStreamAssistantDone
				}
				if err := guard.emit(ChatStreamEvent{Type: typeName, TurnID: "ignored", Delta: "ok"}); err != nil {
					t.Fatalf("turn %d event %d: %v", turn, event, err)
				}
			}
		}
		if err := guard.beginTurn(); err != nil {
			t.Fatal(err)
		}
		if guard.activeTurnKey != 3 {
			t.Fatalf("active turn key = %d", guard.activeTurnKey)
		}
		if err := guard.emit(ChatStreamEvent{Type: ChatStreamAssistantDelta, Delta: "connection overflow"}); !errors.Is(err, errChatWSOutputLimit) {
			t.Fatalf("connection event quota error = %v", err)
		}
	})

	t.Run("turn_bytes", func(t *testing.T) {
		limits := testChatWSEgressLimits()
		limits.maxTurnBytes = 7000
		writer := &recordingChatWSWriter{}
		guard := newChatWSEgressGuardWithLimits(writer, &sync.Mutex{}, limits)
		if err := guard.beginTurn(); err != nil {
			t.Fatal(err)
		}
		chunk := strings.Repeat("x", 3000)
		if err := guard.emit(ChatStreamEvent{Type: ChatStreamAssistantDelta, Delta: chunk}); err != nil {
			t.Fatalf("first byte chunk: %v", err)
		}
		if err := guard.emit(ChatStreamEvent{Type: ChatStreamAssistantDelta, Delta: chunk}); !errors.Is(err, errChatWSOutputLimit) {
			t.Fatalf("turn byte quota error = %v", err)
		}
	})

	t.Run("connection_bytes", func(t *testing.T) {
		limits := testChatWSEgressLimits()
		limits.maxConnectionBytes = 9000
		writer := &recordingChatWSWriter{}
		guard := newChatWSEgressGuardWithLimits(writer, &sync.Mutex{}, limits)
		chunk := strings.Repeat("x", 3000)
		for turn := 0; turn < 2; turn++ {
			if err := guard.beginTurn(); err != nil {
				t.Fatal(err)
			}
			if err := guard.emit(ChatStreamEvent{Type: ChatStreamAssistantDone, Message: chunk}); err != nil {
				t.Fatalf("turn %d connection byte chunk: %v", turn, err)
			}
		}
		if err := guard.beginTurn(); err != nil {
			t.Fatal(err)
		}
		if err := guard.emit(ChatStreamEvent{Type: ChatStreamAssistantDone, Message: chunk}); !errors.Is(err, errChatWSOutputLimit) {
			t.Fatalf("connection byte quota error = %v", err)
		}
	})
}

func TestChatWSEgressConcurrentEmitAndTurnAdvance(t *testing.T) {
	limits := testChatWSEgressLimits()
	limits.maxTurnEvents = 1000
	limits.maxConnectionEvents = 1000
	limits.maxTurnBytes = 8 << 20
	limits.maxConnectionBytes = 16 << 20
	writer := &recordingChatWSWriter{}
	guard := newChatWSEgressGuardWithLimits(writer, &sync.Mutex{}, limits)
	if err := guard.beginTurn(); err != nil {
		t.Fatal(err)
	}

	var wait sync.WaitGroup
	errorsSeen := make(chan error, 256)
	for worker := 0; worker < 8; worker++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			for event := 0; event < 20; event++ {
				errorsSeen <- guard.emit(ChatStreamEvent{
					Type:   ChatStreamAssistantDelta,
					TurnID: "backend-id",
					Delta:  "concurrent chunk",
				})
			}
		}(worker)
	}
	for worker := 0; worker < 4; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for turn := 0; turn < 20; turn++ {
				errorsSeen <- guard.beginTurn()
			}
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("concurrent guard operation: %v", err)
		}
	}
	messages, controls, closed := writer.snapshot()
	if len(messages) != 160 || len(controls) != 0 || closed != 0 {
		t.Fatalf("concurrent writes = messages:%d controls:%d closed:%d", len(messages), len(controls), closed)
	}
}

type oversizedChatBackend struct {
	first  chan error
	second chan error
}

type oversizedChatSession struct {
	context *ChatSessionContext
	backend *oversizedChatBackend
}

func (b *oversizedChatBackend) NewSession(_ context.Context, sessionContext *ChatSessionContext) (ChatSession, error) {
	return &oversizedChatSession{context: sessionContext, backend: b}, nil
}

func (s *oversizedChatSession) HandleEvent(_ context.Context, event *ChatClientEvent) {
	if event.Type != ChatClientUserMessage {
		return
	}
	s.backend.first <- s.context.Emit(ChatStreamEvent{
		Type:    ChatStreamAssistantDone,
		TurnID:  "backend-controls-this-only-as-display-metadata",
		Message: strings.Repeat("x", chatWSMaxFrameBytes),
	})
	s.backend.second <- s.context.Emit(ChatStreamEvent{Type: ChatStreamAssistantDelta, Delta: "must not be written"})
}

func (*oversizedChatSession) Close() {}

func TestChatWebSocketClosesAfterBackendOutputLimit(t *testing.T) {
	backend := &oversizedChatBackend{first: make(chan error, 1), second: make(chan error, 1)}
	handler := New(NewMemoryStore(), WithUserResolver(DevelopmentUserResolver), WithChatBackend(backend))
	server := httptest.NewServer(handler)
	defer server.Close()
	defer handler.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/chat/ws?uid=egress-limit"
	connection, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.WriteJSON(ChatClientEvent{
		Type:     ChatClientUserMessage,
		Messages: []ChatMessage{{Role: "user", Content: "trigger"}},
	}); err != nil {
		t.Fatal(err)
	}
	_ = connection.SetReadDeadline(time.Now().Add(3 * time.Second))
	var terminal ChatStreamEvent
	if err := connection.ReadJSON(&terminal); err != nil {
		t.Fatalf("read terminal event: %v", err)
	}
	if terminal.Type != ChatStreamTurnError || terminal.Error == "" || terminal.TurnID != "" {
		t.Fatalf("terminal event = %+v", terminal)
	}
	if _, _, err := connection.ReadMessage(); err == nil {
		t.Fatal("connection remained readable after terminal output limit")
	}

	select {
	case err := <-backend.first:
		if !errors.Is(err, errChatWSOutputLimit) {
			t.Fatalf("first backend emit error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for first backend emit")
	}
	select {
	case err := <-backend.second:
		if !errors.Is(err, errChatWSOutputClosed) {
			t.Fatalf("second backend emit error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for second backend emit")
	}
}
