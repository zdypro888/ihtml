package chat_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	"github.com/zdypro888/ihtml/chat"
)

type typedNilSessionStore struct{}

func (*typedNilSessionStore) LoadEvents(context.Context, string, *adk.LoadSessionEventsRequest) (*adk.LoadSessionEventsResult[*schema.Message], error) {
	panic("typed nil session store must never be called")
}

func (*typedNilSessionStore) AppendEvents(context.Context, string, []*adk.SessionEvent[*schema.Message]) error {
	panic("typed nil session store must never be called")
}

type typedNilConfirmNonceStore struct{}

func (*typedNilConfirmNonceStore) Put(context.Context, string, time.Time) error {
	panic("typed nil nonce store must never be called")
}

func (*typedNilConfirmNonceStore) Consume(context.Context, string, time.Time) (bool, error) {
	panic("typed nil nonce store must never be called")
}

type typedNilRoundTripper struct{}

func (*typedNilRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	panic("typed nil transport must never be called")
}

type typedNilCookieJar struct{}

func (*typedNilCookieJar) SetCookies(*url.URL, []*http.Cookie) {
	panic("typed nil cookie jar must never be called")
}

func (*typedNilCookieJar) Cookies(*url.URL) []*http.Cookie {
	panic("typed nil cookie jar must never be called")
}

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

func TestConfigRejectsTypedNilInterfaceDependencies(t *testing.T) {
	base := chat.Config{
		Provider: chat.ProviderOpenAI,
		BaseURL:  "https://gateway.example/v1",
		Model:    "model",
	}
	var sessionStore *typedNilSessionStore
	var nonceStore *typedNilConfirmNonceStore
	var transport *typedNilRoundTripper
	var jar *typedNilCookieJar
	tests := []struct {
		field string
		apply func(*chat.Config)
	}{
		{"SessionStore", func(c *chat.Config) { c.SessionStore = sessionStore }},
		{"ConfirmNonceStore", func(c *chat.Config) { c.ConfirmNonceStore = nonceStore }},
		{"ProbeHTTPClient.Transport", func(c *chat.Config) {
			c.ProbeHTTPClient = &http.Client{Transport: transport}
		}},
		{"ProbeHTTPClient.Jar", func(c *chat.Config) {
			c.ProbeHTTPClient = &http.Client{Jar: jar}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.field, func(t *testing.T) {
			cfg := base
			tc.apply(&cfg)
			err := cfg.Validate()
			if !errors.Is(err, chat.ErrInvalidConfig) {
				t.Fatalf("Validate error = %v", err)
			}
			var fieldErr *chat.ConfigError
			if !errors.As(err, &fieldErr) || fieldErr.Field != tc.field {
				t.Fatalf("field error = %#v, want %q", fieldErr, tc.field)
			}
			if _, err := chat.NewChecked(cfg); !errors.Is(err, chat.ErrInvalidConfig) {
				t.Fatalf("NewChecked error = %v", err)
			}
		})
	}
}
