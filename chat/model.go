package chat

import (
	"bytes"
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

	"github.com/zdypro888/ihtml"
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
	if cfg.Temperature > 0 {
		temp := cfg.Temperature
		chatCfg.Temperature = &temp
	}
	return openai.NewChatModel(ctx, chatCfg)
}

func newClaudeChatModel(ctx context.Context, cfg Config) (einoModel.ToolCallingChatModel, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	chatCfg := &claude.Config{
		APIKey: cfg.APIKey,
		Model:  cfg.Model,
	}
	if baseURL != "" {
		chatCfg.BaseURL = &baseURL
	}
	if cfg.MaxTokens > 0 {
		chatCfg.MaxTokens = cfg.MaxTokens
	} else {
		// claude 适配器把 max_tokens 编码为必填, 0 会被网关拒; 从请求体里删掉。
		chatCfg.HTTPClient = claudeOmitZeroMaxTokensHTTPClient(nil)
	}
	if cfg.Temperature > 0 {
		temp := cfg.Temperature
		chatCfg.Temperature = &temp
	}
	return claude.NewChatModel(ctx, chatCfg)
}

func claudeOmitZeroMaxTokensHTTPClient(base *http.Client) *http.Client {
	if base == nil {
		base = http.DefaultClient
	}
	out := *base
	transport := out.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	out.Transport = omitZeroMaxTokensTransport{base: transport}
	return &out
}

type omitZeroMaxTokensTransport struct {
	base http.RoundTripper
}

func (t omitZeroMaxTokensTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || req.Body == nil {
		return t.base.RoundTrip(req)
	}
	body, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return nil, err
	}
	nextBody := body
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err == nil {
		if n, ok := obj["max_tokens"].(float64); ok && n == 0 {
			delete(obj, "max_tokens")
			if encoded, err := json.Marshal(obj); err == nil {
				nextBody = encoded
			}
		}
	}
	req.Body = io.NopCloser(bytes.NewReader(nextBody))
	req.ContentLength = int64(len(nextBody))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(nextBody)), nil
	}
	return t.base.RoundTrip(req)
}

// Models 实现 ihtml.ModelLister。发现顺序与 casino 一致:
// 先查网关"已加载实例"(Ollama 风格 /ollama/api/ps → /api/ps, 目录里的模型
// 不等于能服务的模型), 查不到再退回 OpenAI 标准 GET /models 目录;
// claude 不提供模型发现, 返回空列表让前端手填。
func (b *Backend) Models(ctx context.Context) (ihtml.ChatModels, error) {
	out := ihtml.ChatModels{
		ModelReady:       b.cfg.ready(),
		CurrentModel:     b.cfg.Model,
		Models:           []string{},
		RequestTimeoutMS: b.cfg.RequestTimeout.Milliseconds(),
		ModelSelect:      b.cfg.AllowModelSelect,
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
	// 配置的默认模型没在可用列表里时, 指向第一个可用的, 避免前端默认选中打不通的模型。
	if len(models) > 0 && !slices.Contains(models, out.CurrentModel) {
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
