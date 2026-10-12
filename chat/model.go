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

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/cloudwego/eino-ext/components/model/claude"
	"github.com/cloudwego/eino-ext/components/model/openai"
	einoModel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/internal/nilcheck"
)

const (
	defaultClaudeMaxTokens      = 4096
	defaultClaudeThinkingBudget = 1024
)

func newChatModel(ctx context.Context, cfg Config) (einoModel.ToolCallingChatModel, error) {
	if cfg.ModelFactory != nil {
		model, err := cfg.ModelFactory(ctx, cfg)
		if err != nil {
			return nil, err
		}
		if nilcheck.IsNil(model) {
			return nil, errors.New("model factory returned nil model")
		}
		return model, nil
	}
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
		APIKey:     cfg.APIKey,
		BaseURL:    strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"),
		Model:      cfg.Model,
		HTTPClient: newProviderHTTPClient(cfg.BaseURL, cfg.RequestTimeout, nil, cfg.MaxProviderResponseBytes),
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
		// eino-ext's Claude adapter does not expose anthropic's
		// option.WithMaxRetries. The SDK defaults to two hidden retries, which
		// would multiply the ADK retry policy and bypass the per-turn model-call
		// budget. Keep the provider transport single-attempt; ADK is the only
		// retry owner and every retry therefore passes through withTurnBudget.
		HTTPClient: newClaudeSingleAttemptHTTPClient(cfg.BaseURL, cfg.RequestTimeout, cfg.MaxProviderResponseBytes),
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

const claudeSDKShouldRetryHeader = "x-should-retry"

// newClaudeSingleAttemptHTTPClient disables the Anthropic SDK's implicit
// request retries without forking eino-ext's Claude adapter. anthropic-sdk-go
// treats x-should-retry:false on a response as authoritative while retaining
// the original status code, so the outer ADK policy can still classify 429 and
// transient 5xx responses correctly.
//
// A transport error has no response on which to set that signal. Convert it to
// one safe synthetic response (except when the request context has ended), so
// the SDK returns immediately and ADK alone decides whether another actual HTTP
// attempt is allowed. The response body intentionally excludes the underlying
// URL and transport error.
func newClaudeSingleAttemptHTTPClient(baseURL string, timeout time.Duration, responseLimit ...int64) *http.Client {
	transport := http.DefaultTransport
	if transport == nil {
		transport = http.DefaultTransport
	}
	return newProviderHTTPClient(baseURL, timeout, claudeSingleAttemptRoundTripper{base: transport}, responseLimit...)
}

// newProviderHTTPClient keeps credential-bearing generation requests on the
// exact configured origin. Go's default redirect policy only recognizes a
// small set of sensitive header names; provider-specific credentials such as
// x-api-key could otherwise be copied to a redirected host. Same-origin
// redirects are retained for compatible gateways, but may not change method
// and are bounded to prevent loops.
func newProviderHTTPClient(baseURL string, timeout time.Duration, transport http.RoundTripper, responseLimit ...int64) *http.Client {
	if transport == nil {
		transport = http.DefaultTransport
	}
	limit := defaultMaxProviderResponseBytes
	if len(responseLimit) > 0 && responseLimit[0] >= minMaxProviderResponseBytes && responseLimit[0] <= maxMaxProviderResponseBytes {
		limit = responseLimit[0]
	}
	transport = providerResponseLimitRoundTripper{base: transport, limit: limit}
	client := http.Client{Transport: transport}
	if timeout > 0 {
		client.Timeout = timeout
	}
	origin, parseErr := url.Parse(strings.TrimSpace(baseURL))
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("provider redirect limit exceeded")
		}
		if parseErr != nil || origin == nil || origin.Scheme == "" || origin.Host == "" ||
			req == nil || req.URL == nil || len(via) == 0 ||
			!strings.EqualFold(req.URL.Scheme, origin.Scheme) ||
			!strings.EqualFold(req.URL.Host, origin.Host) ||
			req.Method != via[0].Method {
			return errors.New("provider rejected unsafe redirect")
		}
		return nil
	}
	return &client
}

type providerResponseLimitRoundTripper struct {
	base  http.RoundTripper
	limit int64
}

func (t providerResponseLimitRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp == nil || resp.Body == nil {
		return resp, err
	}
	if resp.ContentLength > t.limit {
		resp.ContentLength = -1
		if resp.Header != nil {
			resp.Header = resp.Header.Clone()
			resp.Header.Del("Content-Length")
		}
	}
	resp.Body = &providerResponseLimitBody{base: resp.Body, remaining: t.limit, limit: t.limit}
	return resp, nil
}

// providerResponseLimitBody permits at most limit response bytes and performs
// one non-retained probe read at the boundary. The probe distinguishes an exact
// length response from an over-limit response without buffering the body.
type providerResponseLimitBody struct {
	base      io.ReadCloser
	remaining int64
	limit     int64
}

func (b *providerResponseLimitBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if b == nil || b.base == nil {
		return 0, io.EOF
	}
	if b.remaining > 0 {
		if int64(len(p)) > b.remaining {
			p = p[:b.remaining]
		}
		n, err := b.base.Read(p)
		b.remaining -= int64(n)
		return n, err
	}
	var probe [1]byte
	n, err := b.base.Read(probe[:])
	if n > 0 {
		return 0, &ModelOutputLimitError{Scope: ModelOutputLimitProviderResponseBytes, Limit: b.limit}
	}
	return 0, err
}

func (b *providerResponseLimitBody) Close() error {
	if b == nil || b.base == nil {
		return nil
	}
	return b.base.Close()
}

type claudeSingleAttemptRoundTripper struct {
	base http.RoundTripper
}

func (t claudeSingleAttemptRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err == nil {
		if resp.Header == nil {
			resp.Header = make(http.Header)
		} else {
			resp.Header = resp.Header.Clone()
		}
		resp.Header.Set(claudeSDKShouldRetryHeader, "false")
		return resp, nil
	}
	if req.Context().Err() != nil {
		return nil, err
	}

	status := http.StatusBadRequest
	errorType := "invalid_request_error"
	message := "provider transport request failed"
	if isRetryableModelError(req.Context(), err) {
		status = http.StatusBadGateway
		errorType = "api_error"
		message = "provider transport temporarily unavailable"
	}
	body, _ := json.Marshal(map[string]any{
		"type": "error",
		"error": map[string]string{
			"type":    errorType,
			"message": message,
		},
	})
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set(claudeSDKShouldRetryHeader, "false")
	return &http.Response{
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		StatusCode:    status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        headers,
		Body:          io.NopCloser(strings.NewReader(string(body))),
		ContentLength: int64(len(body)),
		Request:       req,
	}, nil
}

// Anthropic SDK 会自行追加 v1/messages。兼容用户沿用 OpenAI 风格把 /v1
// 写进 BaseURL 的常见配置，避免最终请求成 /v1/v1/messages。
func normalizeClaudeBaseURL(raw string) string {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	return strings.TrimSuffix(base, "/v1")
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
	thinking := anthropic.ThinkingConfigParamOfEnabled(int64(m.budgetTokens))
	return claude.WithThinkingConfig(&thinking)
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
	// Model discovery is an operational capability, not a prerequisite for
	// using the server-locked model. Avoid an unnecessary credential-bearing
	// network request when the host has not exposed model selection.
	if !out.ModelReady || !b.cfg.AllowModelSelect || strings.TrimSpace(b.cfg.Provider) != ProviderOpenAI {
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
	res, err := modelDiscoveryClient(base).Do(req)
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
	origin, parseErr := url.Parse(base)
	if parseErr != nil {
		return nil, parseErr
	}
	res, err := modelDiscoveryClient(origin).Do(req)
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

// Model discovery carries the provider credential. Keep redirects on the
// exact configured origin so a compromised or misconfigured gateway cannot
// bounce the request (and its Authorization header) to another host. Model
// listing never needs method-changing redirects or an unbounded redirect chain.
func modelDiscoveryClient(origin *url.URL) *http.Client {
	// Never inherit http.DefaultClient.Jar: model discovery has an explicit
	// Authorization policy and must not silently attach ambient process cookies.
	client := http.Client{Transport: http.DefaultTransport}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("model discovery redirect limit exceeded")
		}
		if req == nil || req.URL == nil || req.Method != http.MethodGet || origin == nil ||
			!strings.EqualFold(req.URL.Scheme, origin.Scheme) || !strings.EqualFold(req.URL.Host, origin.Host) {
			return errors.New("model discovery rejected cross-origin redirect")
		}
		return nil
	}
	return &client
}
