package chat_test

import (
	"errors"
	"testing"
	"time"

	"github.com/zdypro888/ihtml/chat"
)

func TestNewCheckedValidatesBeforeFirstTurn(t *testing.T) {
	valid := chat.Config{
		Provider:       chat.ProviderOpenAI,
		BaseURL:        "https://gateway.example/v1",
		Model:          "model",
		RequestTimeout: time.Minute,
	}
	if _, err := chat.NewChecked(valid); err != nil {
		t.Fatal(err)
	}

	invalid := valid
	invalid.Provider = "typo"
	_, err := chat.NewChecked(invalid)
	if !errors.Is(err, chat.ErrInvalidConfig) {
		t.Fatalf("error = %v", err)
	}
	var field *chat.ConfigError
	if !errors.As(err, &field) || field.Field != "Provider" {
		t.Fatalf("field error = %#v", field)
	}
}

func TestConfigValidateCrossFieldRules(t *testing.T) {
	base := chat.Config{Provider: chat.ProviderClaude, BaseURL: "https://gateway.example", Model: "model", AuthToken: "explicit", MaxTokens: 4096}
	tests := map[string]chat.Config{
		"dual credentials":    func() chat.Config { c := base; c.APIKey, c.AuthToken = "a", "b"; return c }(),
		"missing credentials": func() chat.Config { c := base; c.AuthToken = ""; return c }(),
		"thinking budget":     func() chat.Config { c := base; c.Thinking, c.ThinkingBudgetTokens = true, 4096; return c }(),
		"turn timeout": func() chat.Config {
			c := base
			c.RequestTimeout, c.MaxTurnDuration = time.Second, time.Minute
			return c
		}(),
		"short confirm secret": func() chat.Config { c := base; c.ConfirmSecret = []byte("short"); return c }(),
	}
	for name, cfg := range tests {
		t.Run(name, func(t *testing.T) {
			if err := cfg.Validate(); !errors.Is(err, chat.ErrInvalidConfig) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
