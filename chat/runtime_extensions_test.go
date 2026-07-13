package chat

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zdypro888/ihtml"
)

type runtimeProviderFunc func(context.Context, RuntimeConfigRequest) (RuntimeConfigOverride, error)

func (f runtimeProviderFunc) ResolveTurnConfig(ctx context.Context, request RuntimeConfigRequest) (RuntimeConfigOverride, error) {
	return f(ctx, request)
}

type recordingTurnObserver struct {
	mu           sync.Mutex
	observations []TurnObservation
	err          error
	panic        bool
}

func (o *recordingTurnObserver) ObserveTurn(_ context.Context, observation TurnObservation) error {
	if o.panic {
		panic("observer secret panic")
	}
	o.mu.Lock()
	o.observations = append(o.observations, observation)
	o.mu.Unlock()
	return o.err
}

func (o *recordingTurnObserver) snapshot() []TurnObservation {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]TurnObservation(nil), o.observations...)
}

func waitForObservations(t *testing.T, observer *recordingTurnObserver, count int) []TurnObservation {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if got := observer.snapshot(); len(got) >= count {
			return got
		}
		time.Sleep(time.Millisecond)
	}
	return observer.snapshot()
}

func TestRuntimeConfigProviderAppliesOnlyGenerationFields(t *testing.T) {
	temperature := float32(0.25)
	model := " runtime-model "
	maxTokens := 8192
	overrideTemperature := float32(0)
	providerCalls := 0
	provider := runtimeProviderFunc(func(_ context.Context, request RuntimeConfigRequest) (RuntimeConfigOverride, error) {
		providerCalls++
		if request.User != "tenant-user" || request.SessionID != "opaque-session" || request.AgentMode != AgentModeDeep || !request.ToolsEnabled {
			t.Fatalf("request identity/mode = %+v", request)
		}
		if request.Model != "base-model" || request.MaxTokens != 2048 || request.Temperature == nil || *request.Temperature != temperature {
			t.Fatalf("request generation snapshot = %+v", request)
		}
		return RuntimeConfigOverride{
			Model: &model, MaxTokens: &maxTokens, Temperature: &overrideTemperature,
		}, nil
	})
	base := Config{
		Provider: ProviderOpenAI, BaseURL: "https://models.example/v1", APIKey: "server-secret",
		Model: "base-model", MaxTokens: 2048, TemperatureOverride: &temperature,
		SystemPrompt: "trusted system policy", AllowModelSelect: false,
		ConfirmWrites: true, MaxModelCallsPerTurn: 7,
	}
	request := runtimeConfigRequest(base, "tenant-user", "opaque-session", AgentModeDeep, true)
	resolved, err := resolveRuntimeConfig(context.Background(), provider, base, request)
	if err != nil {
		t.Fatalf("resolve runtime config: %v", err)
	}
	if providerCalls != 1 {
		t.Fatalf("provider calls = %d", providerCalls)
	}
	gotTemperature, set := effectiveTemperature(resolved)
	if resolved.Model != "runtime-model" || resolved.MaxTokens != maxTokens || !set || gotTemperature != 0 {
		t.Fatalf("resolved generation = model:%q tokens:%d temperature:%v/%v", resolved.Model, resolved.MaxTokens, gotTemperature, set)
	}
	if resolved.Provider != base.Provider || resolved.BaseURL != base.BaseURL || resolved.APIKey != base.APIKey || resolved.SystemPrompt != base.SystemPrompt {
		t.Fatalf("trusted transport/prompt policy changed: %+v", resolved)
	}
	if resolved.AllowModelSelect || !resolved.ConfirmWrites || resolved.MaxModelCallsPerTurn != 7 {
		t.Fatalf("trusted safety/budget policy changed: %+v", resolved)
	}
	// The provider receives a defensive temperature copy, not Config's pointer.
	*request.Temperature = 1.5
	if *base.TemperatureOverride != temperature {
		t.Fatalf("request mutated base temperature = %v", *base.TemperatureOverride)
	}
}

func TestRuntimeConfigProviderCannotEnableUnsupportedThinking(t *testing.T) {
	enabled := true
	oversizedMaxTokens := 1<<20 + 1
	secretErr := errors.New("vault token super-secret failed")
	tests := []struct {
		name     string
		provider RuntimeConfigProvider
	}{
		{
			name: "invalid override",
			provider: runtimeProviderFunc(func(context.Context, RuntimeConfigRequest) (RuntimeConfigOverride, error) {
				return RuntimeConfigOverride{Thinking: &enabled}, nil
			}),
		},
		{
			name: "provider error",
			provider: runtimeProviderFunc(func(context.Context, RuntimeConfigRequest) (RuntimeConfigOverride, error) {
				return RuntimeConfigOverride{}, secretErr
			}),
		},
		{
			name: "oversized generation override",
			provider: runtimeProviderFunc(func(context.Context, RuntimeConfigRequest) (RuntimeConfigOverride, error) {
				return RuntimeConfigOverride{MaxTokens: &oversizedMaxTokens}, nil
			}),
		},
		{
			name: "non-finite temperature override",
			provider: runtimeProviderFunc(func(context.Context, RuntimeConfigRequest) (RuntimeConfigOverride, error) {
				value := float32(math.NaN())
				return RuntimeConfigOverride{Temperature: &value}, nil
			}),
		},
		{
			name: "provider panic",
			provider: runtimeProviderFunc(func(context.Context, RuntimeConfigRequest) (RuntimeConfigOverride, error) {
				panic("credential super-secret")
			}),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := Config{Provider: ProviderOpenAI, Model: "base-model"}
			resolved, err := resolveRuntimeConfig(context.Background(), test.provider, base, runtimeConfigRequest(base, "user", "session", AgentModeFast, true))
			if !errors.Is(err, errRuntimeConfigProvider) {
				t.Fatalf("error = %v", err)
			}
			if strings.Contains(err.Error(), "secret") || resolved.Model != base.Model || resolved.Thinking {
				t.Fatalf("provider internals leaked or partial override applied: config=%+v err=%v", resolved, err)
			}
		})
	}
}

func TestRuntimeOverrideDoesNotBypassClientModelPolicy(t *testing.T) {
	runtimeModel := "runtime-model"
	resolved, err := applyRuntimeConfigOverride(Config{
		Provider: ProviderOpenAI, Model: "base-model", AllowModelSelect: false,
	}, RuntimeConfigOverride{Model: &runtimeModel})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := selectTurnModel(resolved, "client-model")
	if err == nil || selected.Model != runtimeModel {
		t.Fatalf("client bypassed model policy: model=%q err=%v", selected.Model, err)
	}
}

func TestRuntimeThinkingOverrideIsValidatedAsAWhole(t *testing.T) {
	enabled := true
	budget := 2048
	maxTokens := 4096
	temperature := float32(1)
	resolved, err := applyRuntimeConfigOverride(Config{Provider: ProviderClaude, Model: "claude"}, RuntimeConfigOverride{
		Thinking: &enabled, ThinkingBudgetTokens: &budget, MaxTokens: &maxTokens, Temperature: &temperature,
	})
	if err != nil {
		t.Fatalf("valid thinking override: %v", err)
	}
	if !resolved.Thinking || resolved.ThinkingBudgetTokens != budget || resolved.MaxTokens != maxTokens {
		t.Fatalf("resolved thinking config = %+v", resolved)
	}

	disabled := false
	resolved, err = applyRuntimeConfigOverride(resolved, RuntimeConfigOverride{Thinking: &disabled})
	if err != nil {
		t.Fatalf("disable thinking: %v", err)
	}
	if resolved.Thinking || resolved.ThinkingBudgetTokens != 0 {
		t.Fatalf("disabled thinking retained budget: %+v", resolved)
	}
}

func TestTurnObserverGetsContentFreeOrderedSnapshots(t *testing.T) {
	observer := &recordingTurnObserver{err: errors.New("metrics backend unavailable")}
	state := newTurnObservationState(observer, "run", "turn", "opaque-session", AgentModeFast, true, Config{
		Provider: ProviderOpenAI, Model: "model",
	})
	state.start()
	state.observeEvent(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamAssistantDelta, Delta: "private response"})
	state.observeEvent(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamToolStart, ToolName: "private_tool", ToolInput: `{"token":"private"}`})
	state.observeEvent(ihtml.ChatStreamEvent{
		Type: ihtml.ChatStreamAssistantDone, Message: "private response",
		Budget: &ihtml.ChatTurnBudget{ModelCalls: 3, Subtasks: 1},
	})
	// Duplicate terminal events must not duplicate the observer terminal snapshot.
	state.observeEvent(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamTurnError, Error: "private provider failure"})

	got := waitForObservations(t, observer, 2)
	if len(got) != 2 || got[0].Phase != TurnObservationStarted || got[1].Phase != TurnObservationCompleted {
		t.Fatalf("observations = %+v", got)
	}
	if got[0].Sequence != 1 || got[1].Sequence != 2 || got[1].ModelCalls != 3 || got[1].Subtasks != 1 || got[1].ToolCalls != 1 {
		t.Fatalf("terminal metrics = %+v", got[1])
	}
	if !got[1].Streamed || got[1].OutputBytes != int64(len("private response")) || got[1].Duration < 0 {
		t.Fatalf("terminal stream/output metrics = %+v", got[1])
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private response", "private_tool", "token", "private provider failure"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("observer leaked %q: %s", secret, encoded)
		}
	}
}

func TestTurnObserverFailureAndPanicDoNotEscape(t *testing.T) {
	recording := &recordingTurnObserver{}
	failed := newTurnObservationState(recording, "run", "turn", "session", AgentModeFast, false, Config{})
	failed.observeEvent(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamTurnError, Error: "模型响应超时: private upstream detail"})
	observations := waitForObservations(t, recording, 2)
	if len(observations) != 2 || observations[1].Phase != TurnObservationFailed || observations[1].FailureKind != TurnFailureTimeout {
		t.Fatalf("failed observations = %+v", observations)
	}
	encoded, err := json.Marshal(observations)
	if err != nil || strings.Contains(string(encoded), "private upstream detail") {
		t.Fatalf("failed observation leaked error: json=%s err=%v", encoded, err)
	}

	observer := &recordingTurnObserver{panic: true}
	state := newTurnObservationState(observer, "run", "turn", "session", AgentModeFast, false, Config{})
	state.start()
	state.observeEvent(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamTurnError, Error: "模型响应超时"})
	// The assertion is reaching this point without a panic; also exercise an
	// explicit duplicate failure after the terminal event.
	state.fail(TurnFailureInternal)
}

func TestRuntimeConfigProviderWaitIsBoundedByContext(t *testing.T) {
	release := make(chan struct{})
	provider := runtimeProviderFunc(func(context.Context, RuntimeConfigRequest) (RuntimeConfigOverride, error) {
		<-release // deliberately ignores cancellation like a broken host callback
		return RuntimeConfigOverride{}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := resolveRuntimeConfigBounded(ctx, provider, Config{}, RuntimeConfigRequest{})
	close(release)
	if !errors.Is(err, errRuntimeConfigProvider) {
		t.Fatalf("bounded provider error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("blocked runtime provider held caller for %v", elapsed)
	}
}

func TestRuntimeConfigProviderCapsCallbacksThatIgnoreContext(t *testing.T) {
	limiter := newHostCallbackLimiter(1)
	started := make(chan struct{})
	release := make(chan struct{})
	provider := runtimeProviderFunc(func(context.Context, RuntimeConfigRequest) (RuntimeConfigOverride, error) {
		close(started)
		<-release
		return RuntimeConfigOverride{}, nil
	})
	firstDone := make(chan error, 1)
	go func() {
		_, err := resolveRuntimeConfigBounded(context.Background(), provider, Config{}, RuntimeConfigRequest{}, limiter)
		firstDone <- err
	}()
	<-started

	_, err := resolveRuntimeConfigBounded(context.Background(), provider, Config{}, RuntimeConfigRequest{}, limiter)
	if !errors.Is(err, errRuntimeConfigProvider) {
		t.Fatalf("saturated provider error = %v", err)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first provider: %v", err)
	}
}

type blockingTurnObserver struct {
	entered chan TurnObservation
	release chan struct{}
}

func (o *blockingTurnObserver) ObserveTurn(context.Context, TurnObservation) error {
	o.entered <- TurnObservation{}
	<-o.release
	return nil
}

func TestTurnObserverCannotBlockTurnEventProjection(t *testing.T) {
	observer := &blockingTurnObserver{entered: make(chan TurnObservation, 2), release: make(chan struct{})}
	state := newTurnObservationState(observer, "run", "turn", "session", AgentModeFast, true, Config{})
	started := time.Now()
	state.start()
	state.observeEvent(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamAssistantDone, Message: "done"})
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("observer blocked turn projection for %v", elapsed)
	}
	select {
	case <-observer.entered:
	case <-time.After(time.Second):
		t.Fatal("observer did not receive asynchronous started event")
	}
	close(observer.release)
}

func TestTurnObserverCapacityIsIsolatedFromCriticalHostCallbacks(t *testing.T) {
	backend := New(Config{MaxOutstandingHostCallbacks: 1, MaxOutstandingObservations: 1})
	observer := &blockingTurnObserver{entered: make(chan TurnObservation, 1), release: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		safeObserveTurn(observer, TurnObservation{Phase: TurnObservationStarted}, backend.observations)
		close(done)
	}()
	select {
	case <-observer.entered:
	case <-time.After(time.Second):
		t.Fatal("observer did not occupy its isolated slot")
	}

	called := false
	if err := runHostInterceptorBounded(context.Background(), func(context.Context) error {
		called = true
		return nil
	}, backend.hostCallbacks); err != nil || !called {
		t.Fatalf("observer starved critical host callback: called=%v err=%v", called, err)
	}
	close(observer.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("observer did not return")
	}
}

type typedNilRuntimeProvider struct{}

func (*typedNilRuntimeProvider) ResolveTurnConfig(context.Context, RuntimeConfigRequest) (RuntimeConfigOverride, error) {
	return RuntimeConfigOverride{}, nil
}

type typedNilTurnObserver struct{}

func (*typedNilTurnObserver) ObserveTurn(context.Context, TurnObservation) error { return nil }

func TestRuntimeExtensionTypedNilValidationAndUncheckedNormalization(t *testing.T) {
	valid := Config{
		Provider: ProviderOpenAI, BaseURL: "https://models.example/v1", Model: "model",
	}
	var provider *typedNilRuntimeProvider
	withProvider := valid
	withProvider.RuntimeConfigProvider = provider
	if err := withProvider.Validate(); err == nil || !strings.Contains(err.Error(), "RuntimeConfigProvider") {
		t.Fatalf("typed-nil provider validation = %v", err)
	}
	if backend := New(withProvider); backend.cfg.RuntimeConfigProvider != nil {
		t.Fatal("unchecked constructor retained typed-nil runtime provider")
	}

	var observer *typedNilTurnObserver
	withObserver := valid
	withObserver.TurnObserver = observer
	if err := withObserver.Validate(); err == nil || !strings.Contains(err.Error(), "TurnObserver") {
		t.Fatalf("typed-nil observer validation = %v", err)
	}
	if backend := New(withObserver); backend.cfg.TurnObserver != nil {
		t.Fatal("unchecked constructor retained typed-nil turn observer")
	}
}
