package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/cloudwego/eino-ext/components/model/claude"
	"github.com/cloudwego/eino-ext/components/model/openai"
	einoModel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/zdypro888/ihtml"
)

const (
	defaultClaudeMaxTokens      = 4096
	defaultClaudeThinkingBudget = 1024
)

func newChatModel(ctx context.Context, cfg Config) (einoModel.ToolCallingChatModel, error) {
	switch strings.TrimSpace(cfg.Provider) {
	case ProviderOpenAI:
		return newOpenAIChatModel(ctx, cfg)
	case ProviderClaude:
		return newClaudeChatModel(ctx, cfg)
	default:
		return nil, fmt.Errorf("unsupported provider %q", cfg.Provider)
	}
}

func newOpenAIChatModel(ctx context.Context, cfg Config) (einoModel.ToolCallingChatModel, error) {
	chatCfg := &openai.ChatModelConfig{
		APIKey:  cfg.APIKey,
		BaseURL: strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"),
		Model:   cfg.Model,
	}
	if cfg.RequestTimeout > 0 {
		chatCfg.Timeout = cfg.RequestTimeout
	}
	if cfg.MaxTokens > 0 {
		maxTokens := cfg.MaxTokens
		chatCfg.MaxTokens = &maxTokens
	}
	temperature, temperatureSet := effectiveTemperature(cfg)
	if temperatureSet {
		temp := temperature
		chatCfg.Temperature = &temp
	}
	return openai.NewChatModel(ctx, chatCfg)
}

func newClaudeChatModel(ctx context.Context, cfg Config) (einoModel.ToolCallingChatModel, error) {
	// anthropic-sdk-go otherwise consults ambient ANTHROPIC_* variables. A
	// reusable SDK must never attach an unrelated process credential to a BaseURL
	// chosen by this Config, so credentials are always explicit here.
	if strings.TrimSpace(cfg.APIKey) == "" && strings.TrimSpace(cfg.AuthToken) == "" {
		return nil, errors.New("claude provider requires an explicit API key or auth token")
	}
	baseURL := normalizeClaudeBaseURL(cfg.BaseURL)
	maxTokens := cfg.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultClaudeMaxTokens
	}
	thinkingBudget := cfg.ThinkingBudgetTokens
	if thinkingBudget <= 0 {
		thinkingBudget = defaultClaudeThinkingBudget
	}
	if cfg.Thinking && maxTokens <= thinkingBudget {
		return nil, fmt.Errorf("claude max tokens (%d) must be greater than thinking budget (%d)", maxTokens, thinkingBudget)
	}
	temperature, temperatureSet := effectiveTemperature(cfg)
	if cfg.Thinking && temperatureSet && temperature != 1 {
		return nil, errors.New("claude thinking only supports temperature 1")
	}
	chatCfg := &claude.Config{
		APIKey:    cfg.APIKey,
		AuthToken: cfg.AuthToken,
		Model:     cfg.Model,
		MaxTokens: maxTokens,
	}
	if baseURL != "" {
		chatCfg.BaseURL = &baseURL
	}
	if temperatureSet {
		temp := temperature
		chatCfg.Temperature = &temp
	}
	base, err := claude.NewChatModel(ctx, chatCfg)
	if err != nil {
		return nil, err
	}
	if !cfg.Thinking {
		return base, nil
	}
	return &claudeThinkingChatModel{base: base, budgetTokens: thinkingBudget}, nil
}

// Anthropic SDK 会自行追加 v1/messages。兼容用户沿用 OpenAI 风格把 /v1
// 写进 BaseURL 的常见配置，避免最终请求成 /v1/v1/messages。
func normalizeClaudeBaseURL(raw string) string {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	if strings.HasSuffix(base, "/v1") {
		base = strings.TrimSuffix(base, "/v1")
	}
	return base
}

// claudeThinkingChatModel 把显式 Thinking 配置注入 Generate/Stream，并在
// WithTools 派生后继续保留；否则 eino agent 绑定工具时会悄悄丢失该能力。
type claudeThinkingChatModel struct {
	base         einoModel.ToolCallingChatModel
	budgetTokens int
}

func (m *claudeThinkingChatModel) WithTools(tools []*schema.ToolInfo) (einoModel.ToolCallingChatModel, error) {
	next, err := m.base.WithTools(tools)
	if err != nil {
		return nil, err
	}
	return &claudeThinkingChatModel{base: next, budgetTokens: m.budgetTokens}, nil
}

func (m *claudeThinkingChatModel) thinkingOption() einoModel.Option {
	return claude.WithThinking(&claude.Thinking{Enable: true, BudgetTokens: m.budgetTokens})
}

func (m *claudeThinkingChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...einoModel.Option) (*schema.Message, error) {
	return m.base.Generate(ctx, input, append(opts, m.thinkingOption())...)
}

func (m *claudeThinkingChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einoModel.Option) (*schema.StreamReader[*schema.Message], error) {
	return m.base.Stream(ctx, input, append(opts, m.thinkingOption())...)
}

// Models 实现 ihtml.ModelLister。发现顺序与 casino 一致:
// 先查网关"已加载实例"(Ollama 风格 /ollama/api/ps → /api/ps, 目录里的模型
// 不等于能服务的模型), 查不到再退回 OpenAI 标准 GET /models 目录;
// claude 不提供模型发现, 返回空列表让前端手填。
func (b *Backend) Models(ctx context.Context) (ihtml.ChatModels, error) {
	out := ihtml.ChatModels{
		ProtocolVersion:  ihtml.ChatProtocolVersion,
		Provider:         b.cfg.Provider,
		ModelReady:       b.cfg.ready(),
		CurrentModel:     b.cfg.Model,
		Models:           []string{},
		AgentModes:       []string{AgentModeFast},
		RequestTimeoutMS: b.cfg.RequestTimeout.Milliseconds(),
		ModelSelect:      b.cfg.AllowModelSelect,
	}
	if b.cfg.EnableDeepAgent {
		out.AgentModes = append(out.AgentModes, AgentModeDeep)
		out.DeepApprovalResume = "manual_tool_only"
	}
	if !out.ModelReady || strings.TrimSpace(b.cfg.Provider) != ProviderOpenAI {
		return out, nil
	}
	models, err := listLoadedModels(ctx, b.cfg)
	if err != nil || len(models) == 0 {
		if models, err = listOpenAIModels(ctx, b.cfg); err != nil {
			// 模型发现失败不算致命: 让前端手填模型名。
			return out, nil
		}
	}
	out.Models = models
	// 只有显式允许终端选模型时才让前端回退到目录首项。选择器关闭时，
	// CurrentModel 始终保持服务端配置，客户端不能借模型发现结果覆盖它。
	if b.cfg.AllowModelSelect && len(models) > 0 && !slices.Contains(models, out.CurrentModel) {
		out.CurrentModel = models[0]
	}
	return out, nil
}

// listLoadedModels 查询 Ollama 风格的"当前已加载模型"端点。
// 端点挂在网关根路径(去掉 /v1), 404 时降级尝试 /api/ps。
func listLoadedModels(ctx context.Context, cfg Config) ([]string, error) {
	models, err := fetchLoadedModels(ctx, cfg, "/ollama/api/ps")
	if err == nil {
		return models, nil
	}
	return fetchLoadedModels(ctx, cfg, "/api/ps")
}

func fetchLoadedModels(ctx context.Context, cfg Config, endpoint string) ([]string, error) {
	base, err := url.Parse(strings.TrimSpace(cfg.BaseURL))
	if err != nil {
		return nil, err
	}
	target := base.Scheme + "://" + base.Host + endpoint
	reqCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("loaded models: status %d", res.StatusCode)
	}
	var payload struct {
		Models []struct {
			Model string `json:"model"`
			Name  string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&payload); err != nil {
		return nil, err
	}
	models := make([]string, 0, len(payload.Models))
	for _, m := range payload.Models {
		name := strings.TrimSpace(m.Model)
		if name == "" {
			name = strings.TrimSpace(m.Name)
		}
		if name != "" {
			models = append(models, name)
		}
	}
	return models, nil
}

func listOpenAIModels(ctx context.Context, cfg Config) ([]string, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	reqCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, err
	}
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list models: status %d", res.StatusCode)
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&payload); err != nil {
		return nil, err
	}
	if len(payload.Data) == 0 {
		return nil, errors.New("no models")
	}
	models := make([]string, 0, len(payload.Data))
	for _, m := range payload.Data {
		if strings.TrimSpace(m.ID) != "" {
			models = append(models, m.ID)
		}
	}
	return models, nil
}
