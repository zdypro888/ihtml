package chat

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

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
	if len(c.Model) > 256 || strings.ContainsAny(c.Model, "\r\n\x00") {
		return configError("Model", "must be at most 256 bytes and contain no control line characters")
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

	if c.RequestTimeout < 0 {
		return configError("RequestTimeout", "cannot be negative")
	}
	if c.MaxTokens < 0 {
		return configError("MaxTokens", "cannot be negative")
	}
	temperature, temperatureSet := effectiveTemperature(c)
	if temperature < 0 || temperature > 2 {
		return configError("Temperature", "must be between 0 and 2")
	}
	if c.Thinking && provider != ProviderClaude {
		return configError("Thinking", "is only supported by the Claude provider")
	}
	if !c.Thinking && c.ThinkingBudgetTokens != 0 {
		return configError("ThinkingBudgetTokens", "requires Thinking")
	}
	if c.Thinking {
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
	}

	if c.MaxIterations < 0 {
		return configError("MaxIterations", "cannot be negative")
	}
	if c.RequestTimeout > 0 && c.MaxTurnDuration > 0 && c.MaxTurnDuration > c.RequestTimeout {
		return configError("MaxTurnDuration", "cannot exceed RequestTimeout")
	}
	if len(c.ConfirmSecret) > 0 && len(c.ConfirmSecret) < 32 {
		return configError("ConfirmSecret", "must contain at least 32 bytes")
	}
	if c.WritesPerMinute < -1 {
		return configError("WritesPerMinute", "must be -1, 0, or a positive value")
	}

	if strings.TrimSpace(c.ProbeBaseURL) != "" {
		if _, _, err := validateProviderBaseURL(c.ProbeBaseURL); err != nil {
			return configError("ProbeBaseURL", err.Error())
		}
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
