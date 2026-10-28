package einoengine

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/cloudwego/eino/adk"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const (
	ProviderOpenAI        = "openai"
	ProviderClaude        = "claude"
	EngineEino            = "eino"
	ToolSearchReminderKey = einoToolSearchReminderKey
)

// Config belongs to the runtime, not to a host's database or secret loader.
// Credentials must be resolved explicitly by the host before construction.
type Config struct {
	AgentName             string  `json:"agent_name,omitempty"`
	AgentDescription      string  `json:"agent_description,omitempty"`
	EnableWriteTodos      bool    `json:"enable_write_todos,omitempty"`
	EnableGeneralSubAgent bool    `json:"enable_general_subagent,omitempty"`
	Provider              string  `json:"provider"`
	OpenAIAPI             string  `json:"openai_api,omitempty"`
	APIKey                string  `json:"api_key"`
	BaseURL               string  `json:"base_url"`
	Model                 string  `json:"model"`
	MaxTokens             int     `json:"max_tokens"`
	MaxCompletionTokens   int     `json:"max_completion_tokens"`
	ReasoningEffort       string  `json:"reasoning_effort"`
	ReasoningSummary      string  `json:"reasoning_summary,omitempty"`
	Temperature           float32 `json:"temperature"`
	TimeoutMS             int     `json:"timeout_ms"`
	MaxTurns              int     `json:"max_turns"`
	// ContextCompaction=provider 使用 Responses 原生压缩；未指定阈值时交由服务端决定。
	ContextCompaction      string `json:"context_compaction,omitempty"`
	SummarizeAfterTokens   int    `json:"summarize_after_tokens"`
	SummarizeAfterMessages int    `json:"summarize_after_messages"`
	// SessionNamespace must distinguish hosts sharing one runtime store.
	SessionNamespace string `json:"session_namespace,omitempty"`
	// ValidateSessionID optionally narrows IDs accepted for durable sessions.
	ValidateSessionID func(string) bool `json:"-"`
	// HTTPClient is an explicit host transport override. The default refuses
	// redirects so credentials cannot move to another endpoint.
	HTTPClient *http.Client `json:"-"`
}

// RuntimeStore is private Agent working memory, never the product transcript.
// The host owns its lifecycle, migration and tenant isolation.
type RuntimeStore interface {
	adk.SessionEventStore[*schema.Message]
	adk.CheckPointStore
	adk.CheckPointDeleter
}

// NewChatModel exposes the same Responses/Chat/Claude transport to hosts that
// already own an Eino agent. It does not create another agent or session store.
func NewChatModel(ctx context.Context, cfg Config) (einomodel.ToolCallingChatModel, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	return newChatModel(ctx, cfg)
}

func validateConfig(cfg Config) error {
	if cfg.ContextCompaction != "" && cfg.ContextCompaction != "eino" && cfg.ContextCompaction != "provider" {
		return errors.New("unsupported context compaction mode")
	}
	if cfg.ContextCompaction == "provider" && (cfg.Provider != ProviderOpenAI || cfg.OpenAIAPI != "responses" || cfg.SummarizeAfterMessages != 0) {
		return errors.New("provider compaction requires Responses and no local message trigger")
	}
	if cfg.SummarizeAfterTokens < 0 || cfg.SummarizeAfterMessages < 0 {
		return errors.New("compaction thresholds must be nonnegative")
	}
	if cfg.ReasoningSummary != "" && cfg.ReasoningSummary != "auto" && cfg.ReasoningSummary != "concise" && cfg.ReasoningSummary != "detailed" {
		return errors.New("unsupported reasoning summary")
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return errors.New("agent requires an explicit API key")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return errors.New("agent requires a model")
	}
	if cfg.Provider != ProviderOpenAI && cfg.Provider != ProviderClaude {
		return errors.New("unsupported agent provider")
	}
	if cfg.OpenAIAPI != "" && cfg.OpenAIAPI != "chat_completions" && cfg.OpenAIAPI != "responses" {
		return errors.New("unsupported OpenAI API")
	}
	if cfg.OpenAIAPI != "" && cfg.Provider != ProviderOpenAI {
		return errors.New("OpenAI API selection requires OpenAI provider")
	}
	if cfg.BaseURL != "" {
		base, err := url.Parse(cfg.BaseURL)
		if err != nil || base.Host == "" || (base.Scheme != "https" && base.Scheme != "http") || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
			return errors.New("agent base URL must be an HTTP(S) directory without credentials, query or fragment")
		}
	}
	return nil
}

func providerHTTPClient(cfg Config) *http.Client {
	if cfg.HTTPClient != nil {
		return cfg.HTTPClient
	}
	return &http.Client{Timeout: chatHTTPTimeout(cfg.TimeoutMS), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func validSessionID(id string) bool {
	return strings.TrimSpace(id) != "" && len(id) <= 512 && !strings.ContainsAny(id, "\x00\r\n")
}
