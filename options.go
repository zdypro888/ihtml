package ihtml

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
)

// UserResolver 从请求解析当前用户标识("服务端根据参数找到当前用户")。
// 返回空串或错误都会让请求以 401 结束。
type UserResolver func(r *http.Request) (string, error)

// DefaultUserResolver 依次尝试: query 参数 uid → 请求头 X-Ihtml-User →
// cookie ihtml_uid。宿主服务通常应换成自己的会话/令牌解析。
func DefaultUserResolver(r *http.Request) (string, error) {
	if v := strings.TrimSpace(r.URL.Query().Get("uid")); v != "" {
		return v, nil
	}
	if v := strings.TrimSpace(r.Header.Get("X-Ihtml-User")); v != "" {
		return v, nil
	}
	if c, err := r.Cookie("ihtml_uid"); err == nil && strings.TrimSpace(c.Value) != "" {
		return strings.TrimSpace(c.Value), nil
	}
	return "", fmt.Errorf("missing user identity")
}

type options struct {
	resolver     UserResolver
	chat         ChatBackend
	maxRevisions int
	logger       *slog.Logger
	pageTitle    string
	checkOrigin  func(*http.Request) bool
	pageConfig   func(*http.Request, string) map[string]any
	apis         []APISpec
	cors         []string
	audit        func(context.Context, AuditEvent)
	access       func(*http.Request, string) Access
	limits       Limits
	metrics      Metrics
	broadcaster  Broadcaster
}

// Option 配置 Handler。
type Option func(*options)

// WithUserResolver 设置用户解析器。
func WithUserResolver(fn UserResolver) Option {
	return func(o *options) { o.resolver = fn }
}

// WithChatBackend 挂载 AI 对话后端(见子包 chat)。不设置时页面隐藏对话入口。
func WithChatBackend(b ChatBackend) Option {
	return func(o *options) { o.chat = b }
}

// WithMaxRevisions 设置每个用户保留的历史快照条数, 默认 DefaultMaxRevisions。
func WithMaxRevisions(n int) Option {
	return func(o *options) { o.maxRevisions = n }
}

// WithLogger 设置日志器, 默认 slog.Default()。
func WithLogger(l *slog.Logger) Option {
	return func(o *options) { o.logger = l }
}

// WithPageTitle 设置空壳页面的 <title>, 默认 "ihtml"。
func WithPageTitle(title string) Option {
	return func(o *options) { o.pageTitle = title }
}

// WithWSOriginCheck 覆盖对话 WebSocket 的 Origin 校验, 默认仅允许同源。
func WithWSOriginCheck(fn func(*http.Request) bool) Option {
	return func(o *options) { o.checkOrigin = fn }
}

// WithPageConfig 注入"宿主动态参数": fn 的返回值会出现在 GET /api/config 的
// params 字段里, 页面内核暴露为 ihtml.config, Item 代码可直接使用
// (如业务昵称/权限/上下文)。fn 按请求调用, 返回 nil 表示无参数。
func WithPageConfig(fn func(r *http.Request, user string) map[string]any) Option {
	return func(o *options) { o.pageConfig = fn }
}

// WithAPIs 声明宿主提供的业务 API 目录: 写进 AI 的系统提示, AI 生成的
// Item 会直接 fetch 这些接口取数; 同时 AI 可用只读探测工具实调它们
// 以了解真实返回结构。Path 是相对页面同源的绝对路径(如 /capi/players)。
func WithAPIs(apis ...APISpec) Option {
	return func(o *options) { o.apis = append(o.apis, apis...) }
}

// WithCORS 允许跨域访问(宿主页面把内核 mount 到自己域名下时必须开启)。
// origins 为允许的 Origin 列表, "*" 表示全部。同时放行对应 Origin 的
// 对话 WebSocket。鉴权走 UserResolver 的参数/头, 不依赖 cookie。
func WithCORS(origins ...string) Option {
	return func(o *options) { o.cors = append(o.cors, origins...) }
}
