package chat

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/zdypro888/ihtml"
)

// RuntimeConfigProvider resolves trusted, host-controlled generation settings
// immediately before a model is constructed for a turn. Implementations may
// consult any host-owned configuration source and must be safe for concurrent
// calls from independent sessions.
//
// The request intentionally excludes prompts, message bodies, credentials and
// provider URLs. Model is the effective model after the server has validated
// any allowed client selection; the trusted provider may replace it. Returning
// an error rejects the turn without exposing that error to the browser.
type RuntimeConfigProvider interface {
	ResolveTurnConfig(ctx context.Context, request RuntimeConfigRequest) (RuntimeConfigOverride, error)
}

// RuntimeConfigRequest is the minimal trusted context supplied to a
// RuntimeConfigProvider. User is the authenticated host identity; SessionID is
// an opaque, hashed identifier and does not contain User or the browser csid.
type RuntimeConfigRequest struct {
	User         string
	SessionID    string
	AgentMode    string
	ToolsEnabled bool

	Model                string
	Thinking             bool
	ThinkingBudgetTokens int
	MaxTokens            int
	Temperature          *float32
}

// RuntimeConfigOverride contains the only fields that may vary per turn.
// Pointer fields preserve the difference between "not overridden" and an
// explicit false/zero. Transport, credentials, prompts, tool permissions,
// client model-selection policy and safety budgets always remain authoritative
// in Config and cannot be changed here.
type RuntimeConfigOverride struct {
	Model                *string
	Thinking             *bool
	ThinkingBudgetTokens *int
	MaxTokens            *int
	Temperature          *float32
}

// TurnObservationPhase identifies one immutable lifecycle snapshot.
type TurnObservationPhase string

const (
	TurnObservationStarted   TurnObservationPhase = "started"
	TurnObservationCompleted TurnObservationPhase = "completed"
	TurnObservationFailed    TurnObservationPhase = "failed"
)

// TurnFailureKind is a content-free failure category suitable for metrics.
type TurnFailureKind string

const (
	TurnFailureNone          TurnFailureKind = ""
	TurnFailureCanceled      TurnFailureKind = "canceled"
	TurnFailureTimeout       TurnFailureKind = "timeout"
	TurnFailureRejected      TurnFailureKind = "rejected"
	TurnFailureConfiguration TurnFailureKind = "configuration"
	TurnFailureUnavailable   TurnFailureKind = "unavailable"
	TurnFailureInternal      TurnFailureKind = "internal"
)

// TurnObservation is a privacy-preserving turn snapshot. It deliberately
// carries no user identity, prompt, message/reasoning content, tool name or
// arguments, model response, provider URL, credential, or raw error. Hosts that
// need content-level audit must add it explicitly at their own trust boundary.
type TurnObservation struct {
	Phase        TurnObservationPhase
	Sequence     int
	RunID        string
	TurnID       string
	SessionID    string
	AgentMode    string
	Provider     string
	Model        string
	ToolsEnabled bool

	StartedAt  time.Time
	FinishedAt time.Time
	Duration   time.Duration

	ModelCalls   int64
	Subtasks     int64
	ToolCalls    int64
	OutputBytes  int64
	Streamed     bool
	FinishReason string
	Usage        *ihtml.ChatUsage
	Repaired     bool

	FailureKind TurnFailureKind
}

// TurnObserver receives best-effort lifecycle snapshots. Accepted snapshots are
// ordered per turn (started before completed/failed), but overload or a callback
// that does not return after its context ends may cause later snapshots to be
// dropped. Delivery is asynchronous and uses a pool isolated from critical host
// callbacks, so a slow exporter cannot block or reject the model turn. One
// observer may be called concurrently by different sessions and must be
// thread-safe. Returned errors and panics are isolated from the model turn.
type TurnObserver interface {
	ObserveTurn(ctx context.Context, observation TurnObservation) error
}

var errRuntimeConfigProvider = errors.New("chat: runtime configuration provider failed")

const runtimeConfigResolveTimeout = 5 * time.Second

func runtimeConfigRequest(cfg Config, user, sessionID, mode string, toolsEnabled bool) RuntimeConfigRequest {
	temperature, set := effectiveTemperature(cfg)
	var temperatureCopy *float32
	if set {
		temperatureCopy = &temperature
	}
	return RuntimeConfigRequest{
		User: user, SessionID: sessionID, AgentMode: mode, ToolsEnabled: toolsEnabled,
		Model: cfg.Model, Thinking: cfg.Thinking,
		ThinkingBudgetTokens: cfg.ThinkingBudgetTokens,
		MaxTokens:            cfg.MaxTokens, Temperature: temperatureCopy,
	}
}

func resolveRuntimeConfig(ctx context.Context, provider RuntimeConfigProvider, cfg Config, request RuntimeConfigRequest) (resolved Config, err error) {
	if provider == nil {
		return cfg, nil
	}
	defer func() {
		if recover() != nil {
			resolved = cfg
			err = errRuntimeConfigProvider
		}
	}()
	override, err := provider.ResolveTurnConfig(ctx, request)
	if err != nil {
		return cfg, errRuntimeConfigProvider
	}
	resolved, err = applyRuntimeConfigOverride(cfg, override)
	if err != nil {
		return cfg, errRuntimeConfigProvider
	}
	return resolved, nil
}

// resolveRuntimeConfigBounded keeps a slow or accidentally wedged host
// provider out of the WebSocket read loop. The provider receives the same
// deadline and its eventual result is discarded after cancellation.
func resolveRuntimeConfigBounded(ctx context.Context, provider RuntimeConfigProvider, cfg Config, request RuntimeConfigRequest, limiters ...*hostCallbackLimiter) (Config, error) {
	if provider == nil {
		return cfg, nil
	}
	limiter := selectHostCallbackLimiter(limiters...)
	if !limiter.tryAcquire() {
		return cfg, errRuntimeConfigProvider
	}
	type result struct {
		cfg Config
		err error
	}
	resultCh := make(chan result, 1)
	go func() {
		defer limiter.release()
		resolved, err := resolveRuntimeConfig(ctx, provider, cfg, request)
		resultCh <- result{cfg: resolved, err: err}
	}()
	select {
	case <-ctx.Done():
		return cfg, errRuntimeConfigProvider
	case resolved := <-resultCh:
		return resolved.cfg, resolved.err
	}
}

func runtimeConfigResolveContext(parent context.Context, budget *turnBudget) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	deadline := time.Now().Add(runtimeConfigResolveTimeout)
	if budget != nil && !budget.deadline.IsZero() && budget.deadline.Before(deadline) {
		deadline = budget.deadline
	}
	return context.WithDeadline(parent, deadline)
}

func applyRuntimeConfigOverride(cfg Config, override RuntimeConfigOverride) (Config, error) {
	if override.Model != nil {
		model, err := normalizeModel(*override.Model)
		if err != nil || model == "" {
			return cfg, configError("RuntimeConfigProvider.Model", "must contain 1..256 non-control characters")
		}
		cfg.Model = model
	}
	if override.MaxTokens != nil {
		cfg.MaxTokens = *override.MaxTokens
	}
	if override.Temperature != nil {
		value := *override.Temperature
		cfg.Temperature = 0
		cfg.TemperatureOverride = &value
	}
	if override.Thinking != nil {
		cfg.Thinking = *override.Thinking
		if !cfg.Thinking && override.ThinkingBudgetTokens == nil {
			cfg.ThinkingBudgetTokens = 0
		}
	}
	if override.ThinkingBudgetTokens != nil {
		cfg.ThinkingBudgetTokens = *override.ThinkingBudgetTokens
	}
	if err := validateGenerationConfig(cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

type turnObservationState struct {
	mu       sync.Mutex
	observer TurnObserver
	limiter  *hostCallbackLimiter
	dispatch chan TurnObservation
	base     TurnObservation
	started  bool
	terminal bool
}

func newTurnObservationState(observer TurnObserver, runID, turnID, sessionID, mode string, toolsEnabled bool, cfg Config, limiters ...*hostCallbackLimiter) *turnObservationState {
	if observer == nil {
		return nil
	}
	state := &turnObservationState{
		observer: observer,
		limiter:  selectObservationCallbackLimiter(limiters...),
		dispatch: make(chan TurnObservation, 2),
		base: TurnObservation{
			RunID: runID, TurnID: turnID, SessionID: sessionID,
			AgentMode: mode, Provider: strings.TrimSpace(cfg.Provider), Model: strings.TrimSpace(cfg.Model),
			ToolsEnabled: toolsEnabled,
		},
	}
	go state.runObserver()
	return state
}

func (s *turnObservationState) runObserver() {
	if s == nil {
		return
	}
	for observation := range s.dispatch {
		safeObserveTurn(s.observer, observation, s.limiter)
		if observation.Phase == TurnObservationCompleted || observation.Phase == TurnObservationFailed {
			return
		}
	}
}

func (s *turnObservationState) updateConfig(cfg Config) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.base.Provider = strings.TrimSpace(cfg.Provider)
	s.base.Model = strings.TrimSpace(cfg.Model)
	s.mu.Unlock()
}

func (s *turnObservationState) start() {
	if s == nil {
		return
	}
	s.mu.Lock()
	observation, ok := s.startLocked()
	if ok {
		s.dispatch <- observation
	}
	s.mu.Unlock()
}

func (s *turnObservationState) startLocked() (TurnObservation, bool) {
	if s.started {
		return TurnObservation{}, false
	}
	s.started = true
	s.base.StartedAt = time.Now().UTC()
	out := s.base
	out.Phase = TurnObservationStarted
	out.Sequence = 1
	return out, true
}

func (s *turnObservationState) observeEvent(event ihtml.ChatStreamEvent) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if event.Type == ihtml.ChatStreamAssistantDelta {
		s.base.Streamed = true
	}
	if event.Type == ihtml.ChatStreamToolStart {
		s.base.ToolCalls++
	}
	if event.Budget != nil {
		s.base.ModelCalls = event.Budget.ModelCalls
		s.base.Subtasks = event.Budget.Subtasks
	}
	if !s.terminal && (event.Type == ihtml.ChatStreamAssistantDone || event.Type == ihtml.ChatStreamTurnError) {
		if started, ok := s.startLocked(); ok {
			s.dispatch <- started
		}
		s.terminal = true
		finished := time.Now().UTC()
		s.base.FinishedAt = finished
		s.base.Duration = finished.Sub(s.base.StartedAt)
		terminal := s.base
		terminal.Sequence = 2
		if event.Type == ihtml.ChatStreamAssistantDone {
			terminal.Phase = TurnObservationCompleted
			terminal.OutputBytes = int64(len(event.Message))
			terminal.FinishReason = event.FinishReason
			terminal.Repaired = event.Repaired
			if event.Usage != nil {
				usage := *event.Usage
				terminal.Usage = &usage
			}
		} else {
			terminal.Phase = TurnObservationFailed
			terminal.FailureKind = classifyTurnFailure(event.Error)
		}
		s.dispatch <- terminal
	}
	s.mu.Unlock()
}

func (s *turnObservationState) fail(kind TurnFailureKind) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.terminal {
		s.mu.Unlock()
		return
	}
	if started, ok := s.startLocked(); ok {
		s.dispatch <- started
	}
	s.terminal = true
	finished := time.Now().UTC()
	s.base.FinishedAt = finished
	s.base.Duration = finished.Sub(s.base.StartedAt)
	terminal := s.base
	terminal.Phase = TurnObservationFailed
	terminal.Sequence = 2
	terminal.FailureKind = kind
	s.dispatch <- terminal
	s.mu.Unlock()
}

func safeObserveTurn(observer TurnObserver, observation TurnObservation, limiters ...*hostCallbackLimiter) {
	if observer == nil {
		return
	}
	limiter := selectObservationCallbackLimiter(limiters...)
	// Observability is best effort. When a broken exporter has consumed every
	// slot, drop this snapshot instead of creating another waiting goroutine.
	if !limiter.tryAcquire() {
		return
	}
	defer limiter.release()
	defer func() { _ = recover() }()
	// Do not pass request values or its cancellation into an observability
	// boundary. The callback runs on the ordered asynchronous dispatcher, and a
	// deadline tells cooperative implementations when to abandon slow exports.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = observer.ObserveTurn(ctx, observation)
}

func classifyTurnFailure(message string) TurnFailureKind {
	lower := strings.ToLower(strings.TrimSpace(message))
	switch {
	case strings.Contains(lower, "取消"), strings.Contains(lower, "cancel"):
		return TurnFailureCanceled
	case strings.Contains(lower, "超时"), strings.Contains(lower, "timeout"), strings.Contains(lower, "deadline"):
		return TurnFailureTimeout
	case strings.Contains(lower, "配置"), strings.Contains(lower, "未开启"), strings.Contains(lower, "尚未"):
		return TurnFailureConfiguration
	case strings.Contains(lower, "缺少"), strings.Contains(lower, "不支持"), strings.Contains(lower, "非法"), strings.Contains(lower, "不允许"):
		return TurnFailureRejected
	case strings.Contains(lower, "连接"), strings.Contains(lower, "繁忙"), strings.Contains(lower, "稍后重试"):
		return TurnFailureUnavailable
	default:
		return TurnFailureInternal
	}
}
