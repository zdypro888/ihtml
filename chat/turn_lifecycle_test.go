package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	einoModel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/zdypro888/ihtml"
)

type lifecycleTestAgent struct {
	started chan struct{}
	release <-chan struct{}
	once    sync.Once
	message string
	begin   func() func()
}

func (a *lifecycleTestAgent) Name(context.Context) string        { return "lifecycle-test" }
func (a *lifecycleTestAgent) Description(context.Context) string { return "lifecycle test agent" }
func (a *lifecycleTestAgent) Run(_ context.Context, _ *adk.AgentInput, _ ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	go func() {
		defer generator.Close()
		if a.begin != nil {
			done := a.begin()
			defer done()
		}
		a.once.Do(func() { close(a.started) })
		if a.release != nil {
			<-a.release // Deliberately ignore ctx to model a broken provider/tool.
		}
		generator.Send(&adk.AgentEvent{Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{
			Role:    schema.Assistant,
			Message: schema.AssistantMessage(a.message, nil),
		}}})
	}()
	return iter
}

type blockingPutService struct {
	ihtml.Service
	started chan struct{}
	release <-chan struct{}
	once    sync.Once
}

type blockingLifecycleModel struct {
	started chan struct{}
	release <-chan struct{}
	once    sync.Once
}

type sequentialKVService struct {
	ihtml.Service
	started  chan string
	releases []<-chan struct{}
	mu       sync.Mutex
	calls    int
}

func (s *sequentialKVService) KVSet(ctx context.Context, user, key string, value json.RawMessage) error {
	s.mu.Lock()
	index := s.calls
	s.calls++
	s.mu.Unlock()
	s.started <- key
	if index >= len(s.releases) {
		return context.Canceled
	}
	select {
	case <-s.releases[index]:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.Service.KVSet(context.Background(), user, key, value)
}

func (m *blockingLifecycleModel) WithTools([]*schema.ToolInfo) (einoModel.ToolCallingChatModel, error) {
	return m, nil
}

func (m *blockingLifecycleModel) Generate(context.Context, []*schema.Message, ...einoModel.Option) (*schema.Message, error) {
	m.once.Do(func() { close(m.started) })
	<-m.release // Deliberately ignore ctx.
	return schema.AssistantMessage("done", nil), nil
}

func (m *blockingLifecycleModel) Stream(ctx context.Context, input []*schema.Message, opts ...einoModel.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func (s *blockingPutService) PutItems(_ context.Context, user string, items []ihtml.Item, by, note string) error {
	s.once.Do(func() { close(s.started) })
	<-s.release // Deliberately ignore ctx: the session barrier must still hold.
	return s.Service.PutItems(context.Background(), user, items, by, note)
}

func newLifecycleTestSession(svc ihtml.Service) *session {
	backend := New(Config{WritesPerMinute: -1})
	return &session{
		backend:         backend,
		user:            "lifecycle-user",
		svc:             svc,
		emit:            func(ihtml.ChatStreamEvent) error { return nil },
		sessionID:       "lifecycle-session",
		capabilityScope: "lifecycle-capability",
		store:           newBoundedSessionStore(historyLimitsFromConfig(Config{})),
		inspect:         newInspectBroker(),
	}
}

func newLifecycleTurn(agent adk.Agent) *turnItem {
	emit := func(ihtml.ChatStreamEvent) error { return nil }
	cfg := Config{Stream: false}
	return &turnItem{
		agent:        agent,
		cfg:          cfg,
		mode:         AgentModeFast,
		budget:       newTurnBudget(cfg, AgentModeFast, emit),
		current:      schema.UserMessage("test"),
		emit:         emit,
		timeline:     &timeline{emit: emit, mode: AgentModeFast},
		toolsEnabled: true,
		observation:  newTurnObservationState(nil, "run", "turn", "session", AgentModeFast, true, cfg),
	}
}

func waitLifecycleSignal(t *testing.T, signal <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func assertLifecycleBlocked(t *testing.T, signal <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-signal:
		t.Fatalf("%s started before its predecessor completed", label)
	case <-time.After(100 * time.Millisecond):
	}
}

func waitLifecycleValue[T any](t *testing.T, values <-chan T, label string) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(2 * time.Second):
		var zero T
		t.Fatalf("timed out waiting for %s", label)
		return zero
	}
}

func stopLifecycleSession(t *testing.T, s *session) {
	t.Helper()
	s.workMu.Lock()
	done := s.loopDone
	s.workMu.Unlock()
	s.stopActiveWork()
	if done != nil {
		waitLifecycleSignal(t, done, "loop shutdown")
	}
}

func TestStopLoopQueuesSuccessorUntilBlockingModelActuallyExits(t *testing.T) {
	s := newLifecycleTestSession(ihtml.New(ihtml.NewMemoryStore()))
	oldRelease := make(chan struct{})
	oldStarted := make(chan struct{})
	newStarted := make(chan struct{})
	oldAgent := &lifecycleTestAgent{started: oldStarted, release: oldRelease, message: "old", begin: s.beginOperation}
	newAgent := &lifecycleTestAgent{started: newStarted, message: "new"}

	if err := s.queueTurn(context.Background(), newLifecycleTurn(oldAgent)); err != nil {
		t.Fatal(err)
	}
	waitLifecycleSignal(t, oldStarted, "old model")

	startedAt := time.Now()
	s.stopLoop()
	if elapsed := time.Since(startedAt); elapsed > 250*time.Millisecond {
		t.Fatalf("stopLoop blocked the event handler for %s", elapsed)
	}
	if err := s.queueTurn(context.Background(), newLifecycleTurn(newAgent)); err != nil {
		t.Fatal(err)
	}
	assertLifecycleBlocked(t, newStarted, "successor model")

	close(oldRelease)
	waitLifecycleSignal(t, newStarted, "successor model after old exit")
	stopLifecycleSession(t, s)
}

func TestActiveLoopStillReceivesPreemptiveTurnWhileOperationIsTracked(t *testing.T) {
	s := newLifecycleTestSession(ihtml.New(ihtml.NewMemoryStore()))
	oldRelease := make(chan struct{})
	oldStarted := make(chan struct{})
	newStarted := make(chan struct{})
	oldAgent := &lifecycleTestAgent{started: oldStarted, release: oldRelease, message: "old", begin: s.beginOperation}
	newAgent := &lifecycleTestAgent{started: newStarted, message: "new"}

	if err := s.queueTurn(context.Background(), newLifecycleTurn(oldAgent)); err != nil {
		t.Fatal(err)
	}
	waitLifecycleSignal(t, oldStarted, "old model")
	if err := s.queueTurn(context.Background(), newLifecycleTurn(newAgent)); err != nil {
		t.Fatal(err)
	}
	s.workMu.Lock()
	queuedOutsideLoop := len(s.pendingTurns)
	s.workMu.Unlock()
	if queuedOutsideLoop != 0 {
		t.Fatalf("active-loop preemption was deferred into lifecycle queue: %d", queuedOutsideLoop)
	}

	close(oldRelease)
	waitLifecycleSignal(t, newStarted, "preemptive successor")
	stopLifecycleSession(t, s)
}

func TestCanceledBuildEpochCannotInstallAStaleLoop(t *testing.T) {
	s := newLifecycleTestSession(ihtml.New(ihtml.NewMemoryStore()))
	staleEpoch := s.snapshotWorkEpoch()
	s.stopActiveWork()

	started := make(chan struct{})
	agent := &lifecycleTestAgent{started: started, message: "stale"}
	err := s.queueTurnAtEpoch(context.Background(), newLifecycleTurn(agent), staleEpoch)
	if err == nil || err != errTurnSuperseded {
		t.Fatalf("stale build epoch error = %v", err)
	}
	assertLifecycleBlocked(t, started, "stale canceled turn")
}

func TestTrackedModelThatIgnoresContextBlocksSuccessorUntilRealReturn(t *testing.T) {
	s := newLifecycleTestSession(ihtml.New(ihtml.NewMemoryStore()))
	modelRelease := make(chan struct{})
	modelStarted := make(chan struct{})
	model := trackChatModel(s, &blockingLifecycleModel{started: modelStarted, release: modelRelease})
	modelDone := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer close(modelDone)
		_, _ = model.Generate(ctx, []*schema.Message{schema.UserMessage("test")})
	}()
	waitLifecycleSignal(t, modelStarted, "tracked blocking model")

	cancel()
	s.stopActiveWork()
	newStarted := make(chan struct{})
	if err := s.queueTurn(context.Background(), newLifecycleTurn(&lifecycleTestAgent{started: newStarted, message: "new"})); err != nil {
		t.Fatal(err)
	}
	assertLifecycleBlocked(t, newStarted, "turn behind ignored model context")

	close(modelRelease)
	waitLifecycleSignal(t, modelDone, "tracked model return")
	waitLifecycleSignal(t, newStarted, "turn after tracked model return")
	stopLifecycleSession(t, s)
}

func TestTrackedToolThatIgnoresContextBlocksSuccessorUntilRealReturn(t *testing.T) {
	toolRelease := make(chan struct{})
	toolStarted := make(chan struct{})
	baseService := ihtml.New(ihtml.NewMemoryStore())
	service := &blockingPutService{Service: baseService, started: toolStarted, release: toolRelease}
	s := newLifecycleTestSession(service)
	emit := func(ihtml.ChatStreamEvent) error { return nil }
	cfg := Config{AllowUnconfirmedItemWrites: true, WritesPerMinute: -1}
	rt := &runtime{
		svc: service, user: s.user, cfg: cfg, mode: AgentModeFast,
		signer: s.backend.signer, timeline: &timeline{emit: emit, mode: AgentModeFast},
		writeLimiter: newUserWriteLimiter(-1), budget: newTurnBudget(cfg, AgentModeFast, emit), session: s,
	}
	tool := findTool(t, rt, "ui_item_put")
	ctx, cancel := context.WithCancel(context.Background())
	toolDone := make(chan struct{})
	go func() {
		defer close(toolDone)
		_, _ = tool.InvokableRun(ctx, `{"items":[{"id":"tracked","type":"html","content":"<p>ok</p>"}]}`)
	}()
	waitLifecycleSignal(t, toolStarted, "tracked blocking tool")

	cancel()
	s.stopActiveWork()
	newStarted := make(chan struct{})
	if err := s.queueTurn(context.Background(), newLifecycleTurn(&lifecycleTestAgent{started: newStarted, message: "new"})); err != nil {
		t.Fatal(err)
	}
	assertLifecycleBlocked(t, newStarted, "turn behind ignored tool context")

	close(toolRelease)
	waitLifecycleSignal(t, toolDone, "tracked tool return")
	waitLifecycleSignal(t, newStarted, "turn after tracked tool return")
	stopLifecycleSession(t, s)
}

func TestConfirmationAndQueuedTurnRemainSingleFlightWithIgnoredContexts(t *testing.T) {
	putRelease := make(chan struct{})
	putStarted := make(chan struct{})
	baseService := ihtml.New(ihtml.NewMemoryStore())
	service := &blockingPutService{Service: baseService, started: putStarted, release: putRelease}
	s := newLifecycleTestSession(service)

	oldRelease := make(chan struct{})
	oldStarted := make(chan struct{})
	oldAgent := &lifecycleTestAgent{started: oldStarted, release: oldRelease, message: "old", begin: s.beginOperation}
	if err := s.queueTurn(context.Background(), newLifecycleTurn(oldAgent)); err != nil {
		t.Fatal(err)
	}
	waitLifecycleSignal(t, oldStarted, "old model")

	call, err := s.backend.signer.prepareContext(context.Background(), s.user, newConfirmationScope(s.sessionID, s.capabilityScope), ihtml.ProposedToolCall{
		ID: "confirmed-put", Name: "ui_item_put", Title: "写入 Item", Risk: "medium",
		RequiresConfirmation: true,
		Body: map[string]any{"items": []any{map[string]any{
			"id": "confirmed", "type": "html", "content": "<p>ok</p>",
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	startedAt := time.Now()
	s.startConfirm(context.Background(), &ihtml.ChatClientEvent{Type: "confirm_tool", ToolCall: &call})
	if elapsed := time.Since(startedAt); elapsed > 250*time.Millisecond {
		t.Fatalf("startConfirm blocked the event handler for %s", elapsed)
	}
	assertLifecycleBlocked(t, putStarted, "confirmed tool")

	close(oldRelease)
	waitLifecycleSignal(t, putStarted, "confirmed tool after old loop exit")

	// A new user message cancels the confirmation context, but the host Service
	// deliberately ignores it. The successor must still wait for real return.
	s.cancelManual()
	newStarted := make(chan struct{})
	newAgent := &lifecycleTestAgent{started: newStarted, message: "new"}
	if err := s.queueTurn(context.Background(), newLifecycleTurn(newAgent)); err != nil {
		t.Fatal(err)
	}
	assertLifecycleBlocked(t, newStarted, "turn after canceled confirmation")

	close(putRelease)
	waitLifecycleSignal(t, newStarted, "turn after confirmed tool return")
	stopLifecycleSession(t, s)

	items, err := baseService.ListItems(context.Background(), s.user)
	if err != nil || len(items) != 1 || items[0].ID != "confirmed" {
		t.Fatalf("confirmed tool result = %+v, %v", items, err)
	}
}

func TestBackToBackConfirmationsExecuteInClickOrderWithoutCancelingFirst(t *testing.T) {
	firstRelease := make(chan struct{})
	secondRelease := make(chan struct{})
	baseService := ihtml.New(ihtml.NewMemoryStore())
	service := &sequentialKVService{
		Service: baseService, started: make(chan string, 2),
		releases: []<-chan struct{}{firstRelease, secondRelease},
	}
	s := newLifecycleTestSession(service)
	scope := newConfirmationScope(s.sessionID, s.capabilityScope)
	prepare := func(id, key string) ihtml.ProposedToolCall {
		t.Helper()
		call, err := s.backend.signer.prepareContext(context.Background(), s.user, scope, ihtml.ProposedToolCall{
			ID: id, Name: "ui_kv_set", Title: "写入 KV", Risk: "medium", RequiresConfirmation: true,
			Body: map[string]any{"key": key, "value": key + "-value"},
		})
		if err != nil {
			t.Fatal(err)
		}
		return call
	}
	first := prepare("confirm-first", "first")
	second := prepare("confirm-second", "second")

	s.startConfirm(context.Background(), &ihtml.ChatClientEvent{Type: "confirm_tool", ToolCall: &first})
	if key := waitLifecycleValue(t, service.started, "first confirmation"); key != "first" {
		t.Fatalf("first executed key = %q", key)
	}
	s.startConfirm(context.Background(), &ihtml.ChatClientEvent{Type: "confirm_tool", ToolCall: &second})
	select {
	case key := <-service.started:
		t.Fatalf("second confirmation %q started before first completed", key)
	case <-time.After(100 * time.Millisecond):
	}

	close(firstRelease)
	if key := waitLifecycleValue(t, service.started, "second confirmation"); key != "second" {
		t.Fatalf("second executed key = %q", key)
	}
	s.workMu.Lock()
	manualDone := s.manualDone
	s.workMu.Unlock()
	close(secondRelease)
	waitLifecycleSignal(t, manualDone, "both confirmations")

	for _, key := range []string{"first", "second"} {
		value, err := baseService.KVGet(context.Background(), s.user, key)
		if err != nil || string(value) != `"`+key+`-value"` {
			t.Fatalf("%s confirmation result = %s, %v", key, value, err)
		}
	}
}

func TestCloseKeepsHistoryPinnedUntilTrackedWorkReallyReturns(t *testing.T) {
	s := newLifecycleTestSession(ihtml.New(ihtml.NewMemoryStore()))
	history := s.backend.history
	history.acquire(s.sessionID)
	done := s.beginOperation()
	s.Close()

	history.mu.Lock()
	activeAfterClose := history.sessions[s.sessionID].active
	history.mu.Unlock()
	if activeAfterClose != 1 {
		t.Fatalf("history was released while tracked work was active: %d", activeAfterClose)
	}

	done()
	deadline := time.Now().Add(2 * time.Second)
	for {
		history.mu.Lock()
		active := history.sessions[s.sessionID].active
		history.mu.Unlock()
		if active == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("history remained pinned after tracked work returned")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestConfirmationFloodUsesOneBoundedWorkerQueue(t *testing.T) {
	release := make(chan struct{})
	base := ihtml.New(ihtml.NewMemoryStore())
	service := &ownerBlockingKVService{Service: base, started: make(chan string, 16), firstRelease: release}
	s := newLifecycleTestSession(service)

	first := prepareOwnerKVConfirmation(t, s, "flood-0", "key-0")
	s.startConfirm(context.Background(), &ihtml.ChatClientEvent{Type: "confirm_tool", ToolCall: &first})
	if key := waitLifecycleValue(t, service.started, "first flooded confirmation"); key != "key-0" {
		t.Fatalf("first key = %q", key)
	}

	startedAt := time.Now()
	for index := 1; index <= maxPendingConfirmations+1; index++ {
		key := fmt.Sprintf("key-%d", index)
		call := prepareOwnerKVConfirmation(t, s, fmt.Sprintf("flood-%d", index), key)
		s.startConfirm(context.Background(), &ihtml.ChatClientEvent{Type: "confirm_tool", ToolCall: &call})
	}
	if elapsed := time.Since(startedAt); elapsed > 250*time.Millisecond {
		t.Fatalf("confirmation flood blocked event handler for %s", elapsed)
	}
	s.workMu.Lock()
	queued := len(s.manualQueue)
	worker := s.manualWorker
	done := s.manualDone
	s.workMu.Unlock()
	if queued != maxPendingConfirmations || !worker || done == nil {
		t.Fatalf("manual lane = queued:%d worker:%v done:%v", queued, worker, done != nil)
	}
	select {
	case key := <-service.started:
		t.Fatalf("queued confirmation %q overlapped the single worker", key)
	case <-time.After(100 * time.Millisecond):
	}

	// Closing cancels the bounded queue. The active Service ignores ctx, so the
	// completion barrier must remain until its real return, then drain without
	// executing any of the canceled flood entries.
	s.closeActiveWork()
	close(release)
	waitLifecycleSignal(t, done, "bounded confirmation queue drain")
	service.mu.Lock()
	calls := service.calls
	service.mu.Unlock()
	if calls != 1 {
		t.Fatalf("canceled flood executed %d Service calls, want 1", calls)
	}
}

func TestPendingTurnQueueHasHardLimitDuringOwnershipHandoff(t *testing.T) {
	s := newLifecycleTestSession(ihtml.New(ihtml.NewMemoryStore()))
	s.ownerReady = make(chan struct{})
	for index := 0; index < maxPendingTurns; index++ {
		if err := s.queueTurn(context.Background(), newLifecycleTurn(&lifecycleTestAgent{
			started: make(chan struct{}), message: fmt.Sprintf("turn-%d", index),
		})); err != nil {
			t.Fatalf("queue turn %d: %v", index, err)
		}
	}
	err := s.queueTurn(context.Background(), newLifecycleTurn(&lifecycleTestAgent{
		started: make(chan struct{}), message: "overflow",
	}))
	if !errors.Is(err, errPendingTurnQueueFull) {
		t.Fatalf("overflow error = %v", err)
	}
	s.workMu.Lock()
	queued := len(s.pendingTurns)
	s.workMu.Unlock()
	if queued != maxPendingTurns {
		t.Fatalf("pending turns = %d, want %d", queued, maxPendingTurns)
	}
	s.closeActiveWork()
	close(s.ownerReady)
	s.drainPendingTurns()
}

type deadlineKVService struct {
	ihtml.Service
	remaining chan time.Duration
}

func (s *deadlineKVService) KVSet(ctx context.Context, _ string, _ string, _ json.RawMessage) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		s.remaining <- -1
		return errors.New("missing confirmation deadline")
	}
	s.remaining <- time.Until(deadline)
	<-ctx.Done()
	return ctx.Err()
}

func TestConfirmationUsesMaxTurnDurationWhenRequestTimeoutUnset(t *testing.T) {
	service := &deadlineKVService{
		Service: ihtml.New(ihtml.NewMemoryStore()), remaining: make(chan time.Duration, 1),
	}
	s := newLifecycleTestSession(service)
	s.backend = New(Config{WritesPerMinute: -1, MaxTurnDuration: 50 * time.Millisecond})
	call := prepareOwnerKVConfirmation(t, s, "deadline-confirm", "deadline")
	s.startConfirm(context.Background(), &ihtml.ChatClientEvent{Type: "confirm_tool", ToolCall: &call})
	remaining := waitLifecycleValue(t, service.remaining, "confirmation deadline")
	if remaining <= 0 || remaining > 100*time.Millisecond {
		t.Fatalf("confirmation deadline remaining = %s", remaining)
	}
	s.workMu.Lock()
	done := s.manualDone
	s.workMu.Unlock()
	waitLifecycleSignal(t, done, "confirmation deadline expiry")
}

func TestApprovalFrameLimiterIsConcurrentAndBounded(t *testing.T) {
	s := newLifecycleTestSession(ihtml.New(ihtml.NewMemoryStore()))
	var accepted atomic.Int64
	var wg sync.WaitGroup
	for index := 0; index < maxApprovalEventsMinute*4; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s.takeApprovalEvent() {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := accepted.Load(); got != maxApprovalEventsMinute {
		t.Fatalf("accepted approval frames = %d, want %d", got, maxApprovalEventsMinute)
	}
}

func TestBackendOperationLimiterBoundsIgnoredModelsAcrossSessions(t *testing.T) {
	backend := New(Config{})
	backend.operations = newOperationLimiter(2)
	first := &session{backend: backend}
	second := &session{backend: backend}
	third := &session{backend: backend}
	firstRelease := make(chan struct{})
	secondRelease := make(chan struct{})
	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	firstDone := make(chan struct{})
	secondDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		_, _ = trackChatModel(first, &blockingLifecycleModel{started: firstStarted, release: firstRelease}).Generate(context.Background(), nil)
	}()
	go func() {
		defer close(secondDone)
		_, _ = trackChatModel(second, &blockingLifecycleModel{started: secondStarted, release: secondRelease}).Generate(context.Background(), nil)
	}()
	waitLifecycleSignal(t, firstStarted, "first globally tracked model")
	waitLifecycleSignal(t, secondStarted, "second globally tracked model")

	thirdStarted := make(chan struct{})
	thirdRelease := make(chan struct{})
	thirdModel := trackChatModel(third, &blockingLifecycleModel{started: thirdStarted, release: thirdRelease})
	startedAt := time.Now()
	if _, err := thirdModel.Generate(context.Background(), nil); !errors.Is(err, errOperationCapacity) {
		t.Fatalf("saturated model error = %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed > 250*time.Millisecond {
		t.Fatalf("saturated model did not fail fast: %s", elapsed)
	}
	assertLifecycleBlocked(t, thirdStarted, "provider behind global operation capacity")

	close(firstRelease)
	waitLifecycleSignal(t, firstDone, "first global model return")
	thirdDone := make(chan struct{})
	go func() {
		defer close(thirdDone)
		_, _ = thirdModel.Generate(context.Background(), nil)
	}()
	waitLifecycleSignal(t, thirdStarted, "third model after slot release")
	close(thirdRelease)
	close(secondRelease)
	waitLifecycleSignal(t, thirdDone, "third global model return")
	waitLifecycleSignal(t, secondDone, "second global model return")
}

func TestBackendOperationLimiterRejectsServiceBeforeSideEffect(t *testing.T) {
	backend := New(Config{WritesPerMinute: -1})
	backend.operations = newOperationLimiter(1)
	blockingSession := &session{backend: backend}
	release := make(chan struct{})
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = trackChatModel(blockingSession, &blockingLifecycleModel{started: started, release: release}).Generate(context.Background(), nil)
	}()
	waitLifecycleSignal(t, started, "capacity occupying model")

	base := ihtml.New(ihtml.NewMemoryStore())
	toolSession := &session{backend: backend}
	emit := func(ihtml.ChatStreamEvent) error { return nil }
	cfg := Config{AllowUnconfirmedItemWrites: true, WritesPerMinute: -1}
	rt := &runtime{
		svc: base, user: "capacity-user", cfg: cfg, mode: AgentModeFast,
		signer: backend.signer, timeline: &timeline{emit: emit, mode: AgentModeFast},
		writeLimiter: backend.writeLimiter, budget: newTurnBudget(cfg, AgentModeFast, emit), session: toolSession,
	}
	tool := findTool(t, rt, "ui_item_put")
	if _, err := tool.InvokableRun(context.Background(), `{"items":[{"id":"must-not-write","type":"html","content":"<p>x</p>"}]}`); !errors.Is(err, errOperationCapacity) {
		t.Fatalf("saturated Service error = %v", err)
	}
	items, err := base.ListItems(context.Background(), "capacity-user")
	if err != nil || len(items) != 0 {
		t.Fatalf("Service executed while capacity saturated: items=%+v err=%v", items, err)
	}
	close(release)
	waitLifecycleSignal(t, done, "capacity occupying model return")
}
