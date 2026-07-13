package chat

import (
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/zdypro888/ihtml/internal/nilcheck"
)

const maxConfiguredOutstandingWork = 4096

// ErrInvalidConfig is returned by Config.Validate and NewChecked when a
// programmatic chat configuration is not safe or internally consistent.
var ErrInvalidConfig = errors.New("chat: invalid configuration")

// ConfigError identifies the public configuration field that failed
// validation. Callers should use errors.As instead of matching Error strings.
type ConfigError struct {
	Field   string
	Problem string
}

func (e *ConfigError) Error() string {
	if e == nil {
		return ErrInvalidConfig.Error()
	}
	if e.Field == "" {
		return fmt.Sprintf("%s: %s", ErrInvalidConfig, e.Problem)
	}
	return fmt.Sprintf("%s: %s: %s", ErrInvalidConfig, e.Field, e.Problem)
}

func (e *ConfigError) Unwrap() error { return ErrInvalidConfig }

func configError(field, problem string) error {
	return &ConfigError{Field: field, Problem: problem}
}

// Validate checks a programmatically constructed Config without contacting the
// model provider. It intentionally validates only the SDK contract; provider
// authentication policies may impose additional deployment-specific rules.
func (c Config) Validate() error {
	provider := strings.TrimSpace(c.Provider)
	if provider != ProviderOpenAI && provider != ProviderClaude {
		return configError("Provider", "must be chat.ProviderOpenAI or chat.ProviderClaude")
	}

	_, _, err := validateProviderBaseURL(c.BaseURL)
	if err != nil {
		return configError("BaseURL", err.Error())
	}

	if strings.TrimSpace(c.Model) == "" && !c.AllowModelSelect {
		return configError("Model", "is required when AllowModelSelect is false")
	}
	if len(c.Model) > 256 || strings.ContainsFunc(c.Model, unicode.IsControl) {
		return configError("Model", "must be at most 256 bytes and contain no control characters")
	}
	if len(c.SystemPrompt) > 16<<10 || strings.ContainsRune(c.SystemPrompt, '\x00') {
		return configError("SystemPrompt", "must not contain NUL or exceed 16384 bytes")
	}
	if provider == ProviderOpenAI && strings.TrimSpace(c.AuthToken) != "" {
		return configError("AuthToken", "is only supported by the Claude provider")
	}
	if strings.TrimSpace(c.APIKey) != "" && strings.TrimSpace(c.AuthToken) != "" {
		return configError("APIKey", "cannot be combined with AuthToken")
	}
	if provider == ProviderClaude && strings.TrimSpace(c.APIKey) == "" && strings.TrimSpace(c.AuthToken) == "" {
		return configError("APIKey", "the Claude provider requires an explicit APIKey or AuthToken")
	}

	if c.RequestTimeout < 0 || c.RequestTimeout > time.Hour {
		return configError("RequestTimeout", "must be 0 (unlimited) or no greater than 1h")
	}
	if err := validateGenerationConfig(c); err != nil {
		return err
	}

	if c.MaxIterations < 0 || c.MaxIterations > 64 {
		return configError("MaxIterations", "must be 0 (default) or between 1 and 64")
	}
	if c.MaxModelRetries < -1 || c.MaxModelRetries > 5 {
		return configError("MaxModelRetries", "must be -1, 0, or between 1 and 5")
	}
	for _, limit := range []struct {
		field string
		value int
		max   int
	}{
		{"MaxModelCallsPerTurn", c.MaxModelCallsPerTurn, 128},
		{"MaxSubtasksPerTurn", c.MaxSubtasksPerTurn, 16},
		{"MaxToolCallsPerTurn", c.MaxToolCallsPerTurn, 512},
		{"MaxToolCallsPerTool", c.MaxToolCallsPerTool, 128},
		{"MaxRepeatedToolCalls", c.MaxRepeatedToolCalls, 32},
	} {
		if limit.value > limit.max {
			return configError(limit.field, fmt.Sprintf("must be negative (unlimited), 0 (default), or no greater than %d", limit.max))
		}
	}
	if c.SummarizeAfterTokens < 0 || c.SummarizeAfterTokens > 1<<20 {
		return configError("SummarizeAfterTokens", "must be 0..1048576")
	}
	if c.SummarizeAfterTokens > 0 && c.SummarizeAfterTokens < 1024 {
		return configError("SummarizeAfterTokens", "must be 0 or at least 1024")
	}
	if c.SummarizeAfterMessages < 0 || c.SummarizeAfterMessages > 10000 {
		return configError("SummarizeAfterMessages", "must be 0..10000")
	}
	if c.SummarizeAfterMessages > 0 && c.SummarizeAfterMessages < 8 {
		return configError("SummarizeAfterMessages", "must be 0 or at least 8")
	}
	for _, limit := range []struct {
		field string
		value int64
		min   int64
		max   int64
	}{
		{"MaxProviderResponseBytes", c.MaxProviderResponseBytes, minMaxProviderResponseBytes, maxMaxProviderResponseBytes},
		{"MaxModelOutputBytesPerCall", c.MaxModelOutputBytesPerCall, minMaxModelOutputBytesPerCall, maxMaxModelOutputBytesPerCall},
		{"MaxModelOutputBytesPerTurn", c.MaxModelOutputBytesPerTurn, minMaxModelOutputBytesPerTurn, maxMaxModelOutputBytesPerTurn},
		{"MaxAssistantMessageBytes", c.MaxAssistantMessageBytes, minMaxAssistantMessageBytes, maxMaxAssistantMessageBytes},
		{"MaxReasoningBytesPerTurn", c.MaxReasoningBytesPerTurn, minMaxReasoningBytesPerTurn, maxMaxReasoningBytesPerTurn},
	} {
		if limit.value != 0 && (limit.value < limit.min || limit.value > limit.max) {
			return configError(limit.field, fmt.Sprintf("must be 0 (default) or between %d and %d", limit.min, limit.max))
		}
	}
	perCall := c.MaxModelOutputBytesPerCall
	if perCall == 0 {
		perCall = defaultMaxModelOutputBytesPerCall
	}
	perTurn := c.MaxModelOutputBytesPerTurn
	if perTurn == 0 {
		perTurn = defaultMaxModelOutputBytesPerTurn
	}
	if perTurn < perCall {
		return configError("MaxModelOutputBytesPerTurn", "cannot be smaller than MaxModelOutputBytesPerCall")
	}
	if c.MaxModelOutputEventsPerTurn != 0 &&
		(c.MaxModelOutputEventsPerTurn < minMaxModelOutputEventsPerTurn || c.MaxModelOutputEventsPerTurn > maxMaxModelOutputEventsPerTurn) {
		return configError("MaxModelOutputEventsPerTurn", fmt.Sprintf("must be 0 (default) or between %d and %d", minMaxModelOutputEventsPerTurn, maxMaxModelOutputEventsPerTurn))
	}
	if c.MaxTurnDuration > time.Hour {
		return configError("MaxTurnDuration", "must be negative (unlimited), 0 (default), or no greater than 1h")
	}
	if c.MaxHistorySessions > 100000 {
		return configError("MaxHistorySessions", "must be negative (unlimited), 0 (default), or no greater than 100000")
	}
	if c.MaxHistoryEvents > 100000 {
		return configError("MaxHistoryEvents", "must be negative (unlimited), 0 (default), or no greater than 100000")
	}
	if c.MaxHistoryBytes > 1<<30 {
		return configError("MaxHistoryBytes", "must be negative (unlimited), 0 (default), or no greater than 1073741824")
	}
	if c.HistoryTTL > 365*24*time.Hour {
		return configError("HistoryTTL", "must be negative (unlimited), 0 (default), or no greater than 8760h")
	}
	if c.RequestTimeout > 0 && c.MaxTurnDuration > 0 && c.MaxTurnDuration > c.RequestTimeout {
		return configError("MaxTurnDuration", "cannot exceed RequestTimeout")
	}
	for _, limit := range []struct {
		field string
		value int
	}{
		{"MaxOutstandingHostCallbacks", c.MaxOutstandingHostCallbacks},
		{"MaxOutstandingObservations", c.MaxOutstandingObservations},
		{"MaxOutstandingOperations", c.MaxOutstandingOperations},
	} {
		if limit.value < 0 || limit.value > maxConfiguredOutstandingWork {
			return configError(limit.field, fmt.Sprintf("must be 0 (default) or between 1 and %d", maxConfiguredOutstandingWork))
		}
	}
	if len(c.ConfirmSecret) > 0 && len(c.ConfirmSecret) < 32 {
		return configError("ConfirmSecret", "must contain at least 32 bytes")
	}
	if c.ConfirmNonceStore != nil && nilcheck.IsNil(c.ConfirmNonceStore) {
		return configError("ConfirmNonceStore", "must not contain a typed nil implementation")
	}
	if c.SessionStore != nil && nilcheck.IsNil(c.SessionStore) {
		return configError("SessionStore", "must not contain a typed nil implementation")
	}
	if (c.SessionStore != nil || c.ConfirmNonceStore != nil) && strings.TrimSpace(c.SessionNamespace) == "" {
		return configError("SessionNamespace", "is required when SessionStore or ConfirmNonceStore is provided")
	}
	if err := validateScopeLabel(c.SessionNamespace); err != nil {
		return configError("SessionNamespace", err.Error())
	}
	if err := validateScopeLabel(c.SessionCapabilityVersion); err != nil {
		return configError("SessionCapabilityVersion", err.Error())
	}
	if c.RuntimeConfigProvider != nil && nilcheck.IsNil(c.RuntimeConfigProvider) {
		return configError("RuntimeConfigProvider", "must not contain a typed nil implementation")
	}
	if c.TurnObserver != nil && nilcheck.IsNil(c.TurnObserver) {
		return configError("TurnObserver", "must not contain a typed nil implementation")
	}
	if c.WritesPerMinute < -1 || c.WritesPerMinute > 10000 {
		return configError("WritesPerMinute", "must be -1, 0, or between 1 and 10000")
	}

	if strings.TrimSpace(c.ProbeBaseURL) != "" {
		if _, _, err := validateProviderBaseURL(c.ProbeBaseURL); err != nil {
			return configError("ProbeBaseURL", err.Error())
		}
	}
	if c.ProbeHTTPClient != nil {
		if c.ProbeHTTPClient.Transport != nil && nilcheck.IsNil(c.ProbeHTTPClient.Transport) {
			return configError("ProbeHTTPClient.Transport", "must not contain a typed nil implementation")
		}
		if c.ProbeHTTPClient.Jar != nil && nilcheck.IsNil(c.ProbeHTTPClient.Jar) {
			return configError("ProbeHTTPClient.Jar", "must not contain a typed nil implementation")
		}
	}
	return nil
}

func validateScopeLabel(value string) error {
	if len(value) > 256 {
		return errors.New("must be at most 256 bytes")
	}
	if strings.ContainsFunc(value, unicode.IsControl) {
		return errors.New("must not contain control characters")
	}
	return nil
}

// validateGenerationConfig checks only model-generation fields. Keeping this
// separate lets trusted per-turn overrides be validated without revalidating or
// exposing transport credentials and host safety policy.
func validateGenerationConfig(c Config) error {
	provider := strings.TrimSpace(c.Provider)
	if c.MaxTokens < 0 || c.MaxTokens > 1<<20 {
		return configError("MaxTokens", "must be between 0 and 1048576")
	}
	temperature, temperatureSet := effectiveTemperature(c)
	if math.IsNaN(float64(temperature)) || math.IsInf(float64(temperature), 0) || temperature < 0 || temperature > 2 {
		return configError("Temperature", "must be between 0 and 2")
	}
	if c.Thinking && provider != ProviderClaude {
		return configError("Thinking", "is only supported by the Claude provider")
	}
	if !c.Thinking && c.ThinkingBudgetTokens != 0 {
		return configError("ThinkingBudgetTokens", "requires Thinking")
	}
	if !c.Thinking {
		return nil
	}
	budget := c.ThinkingBudgetTokens
	if budget == 0 {
		budget = defaultClaudeThinkingBudget
	}
	maxTokens := c.MaxTokens
	if maxTokens == 0 {
		maxTokens = defaultClaudeMaxTokens
	}
	if budget < 1024 || maxTokens <= budget {
		return configError("ThinkingBudgetTokens", "must be at least 1024 and smaller than MaxTokens")
	}
	if temperatureSet && temperature != 1 {
		return configError("Temperature", "Claude extended thinking supports only 1 or the zero-value default")
	}
	return nil
}

func effectiveTemperature(c Config) (float32, bool) {
	if c.TemperatureOverride != nil {
		return *c.TemperatureOverride, true
	}
	return c.Temperature, c.Temperature > 0
}

// NewChecked is the non-panicking, validation-first constructor recommended
// for SDK consumers. New remains available for source compatibility.
func NewChecked(cfg Config) (*Backend, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return New(cfg), nil
}

func validateProviderBaseURL(raw string) (*url.URL, bool, error) {
	value := strings.TrimSpace(raw)
	if value == "" || len(value) > 2048 {
		return nil, false, errors.New("must be a non-empty URL no longer than 2048 bytes")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, false, errors.New("must be an absolute HTTP or HTTPS URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, false, errors.New("must not contain userinfo, query, or fragment")
	}
	loopback := strings.EqualFold(parsed.Hostname(), "localhost")
	if ip := net.ParseIP(parsed.Hostname()); ip != nil && ip.IsLoopback() {
		loopback = true
	}
	if parsed.Scheme != "https" && !loopback {
		return nil, false, errors.New("must use HTTPS unless the host is loopback")
	}
	return parsed, loopback, nil
}
