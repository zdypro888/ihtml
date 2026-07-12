package ihtml

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

// UserResolver 从宿主已验证的会话/令牌解析当前用户标识。
// 返回空串或错误都会让请求以 401 结束。为了让 SSE/WS
// 能感知会话撤销，同一请求的 resolver 可能被重复调用；实现应并发安全。
type UserResolver func(r *http.Request) (string, error)

// DefaultUserResolver 是安全默认: 在宿主未显式接入认证时拒绝请求。
// ihtml 无法自行判断哪个 cookie/header 代表已验证会话，因此生产
// 服务必须通过 WithUserResolver 提供宿主的验证逻辑。
func DefaultUserResolver(*http.Request) (string, error) {
	return "", fmt.Errorf("user resolver is not configured")
}

// DevelopmentUserResolver 从 uid query、X-Ihtml-User header 或 ihtml_uid
// cookie 直接取用户名。这些值都由客户端控制，只能用于本地演示/测试，
// 绝不应用于生产环境。
func DevelopmentUserResolver(r *http.Request) (string, error) {
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
	resolver       UserResolver
	chat           ChatBackend
	maxRevisions   int
	logger         *slog.Logger
	pageTitle      string
	checkOrigin    func(*http.Request) bool
	pageConfig     func(*http.Request, string) map[string]any
	apis           []APISpec
	cors           []string
	messageOrigins []string
	audit          func(context.Context, AuditEvent)
	access         func(*http.Request, string) (Access, error)
	limits         Limits
	metrics        Metrics
	broadcaster    Broadcaster
}

// Option 配置 Handler。
type Option func(*options)

// WithUserResolver 设置用户解析器。
func WithUserResolver(fn UserResolver) Option {
	return func(o *options) {
		if fn == nil {
			o.resolver = DefaultUserResolver
			return
		}
		o.resolver = fn
	}
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
	return func(o *options) {
		if l == nil {
			o.logger = slog.Default()
			return
		}
		o.logger = l
	}
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
// 对话 WebSocket。跨域认证应使用 UserResolver 验证的 Authorization
// 等非 cookie 凭据；本库不会下发 Access-Control-Allow-Credentials。
// TLS 在反向代理终止时，还应把外部 https Origin 显式列入，
// 因为库不会信任客户端可伪造的 X-Forwarded-Proto。
func WithCORS(origins ...string) Option {
	return func(o *options) { o.cors = append(o.cors, origins...) }
}

// WithMessageOrigins 显式允许哪些父页面 Origin 通过 postMessage
// 与 iframe 中的 ihtml 通信，并同步用于 CSP frame-ancestors。
// 默认不配置时只允许同源。"*" 会允许任意父页面，仅应在明确
// 接受任意站点嵌入和消息交互风险时使用。非法 Origin 会被安全地忽略。
func WithMessageOrigins(origins ...string) Option {
	return func(o *options) {
		for _, origin := range origins {
			if normalized, ok := normalizeMessageOrigin(origin); ok && !containsString(o.messageOrigins, normalized) {
				o.messageOrigins = append(o.messageOrigins, normalized)
			}
		}
	}
}

func normalizeMessageOrigin(origin string) (string, bool) {
	origin = strings.TrimSpace(origin)
	if origin == "*" {
		return origin, true
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return "", false
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host), true
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
