package ihtml

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/zdypro888/ihtml/internal/nilcheck"
)

// AuditEvent 是一次数据变更的审计记录, 经 WithAudit 同步交给宿主留痕。
// 与滚动淘汰的快照不同, 审计流不丢失历史。
type AuditEvent struct {
	Time   time.Time       `json:"time"`
	User   string          `json:"user"`
	Action string          `json:"action"` // items.put/items.delete/items.replace/items.rollback/kv.set/kv.delete/page.set/page.delete/user.import/user.purge
	By     string          `json:"by"`     // api / chat / import ...
	Note   string          `json:"note,omitempty"`
	Detail json.RawMessage `json:"detail,omitempty"` // ids/key/page 等结构化明细
}

// WithAudit 注册审计回调。回调同步调用(需要异步的话由宿主自理),
// 覆盖全部写操作(含 AI 工具发起的)。
func WithAudit(fn func(ctx context.Context, e AuditEvent)) Option {
	return func(o *options) {
		if fn == nil {
			o.audit = nil
			return
		}
		o.audit = func(ctx context.Context, e AuditEvent) {
			if strings.TrimSpace(e.By) == "" {
				e.By = mutationSourceFromContext(ctx)
			}
			fn(ctx, e)
		}
	}
}

type mutationSourceKey struct{}

func withMutationSource(ctx context.Context, source string) context.Context {
	return context.WithValue(ctx, mutationSourceKey{}, source)
}

func mutationSourceFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(mutationSourceKey{}).(string)
	return strings.TrimSpace(v)
}

// Access 是一次请求的访问策略结果。
type Access struct {
	// ReadOnly 拒绝一切写操作(HTTP 写接口与 AI 工具写), 用于
	// 客服代查/演示账号/免费套餐只读等场景。
	ReadOnly bool
	// ChatDisabled 对该用户禁用 AI 对话(入口隐藏, WS 拒绝)。
	ChatDisabled bool
}

// WithAccessPolicy 设置按请求/用户的访问策略。长连接会重复且可能并发
// 调用 fn 以感知权限撤销，宿主实现必须并发安全。
func WithAccessPolicy(fn func(r *http.Request, user string) Access) Option {
	return func(o *options) {
		if fn == nil {
			o.access = nil
			return
		}
		o.access = func(r *http.Request, user string) (Access, error) {
			return fn(r, user), nil
		}
	}
}

// WithAccessPolicyE is the fail-closed form of WithAccessPolicy. It is intended
// for policies backed by databases or authorization services that can fail.
// An error rejects the request instead of silently granting the zero Access.
func WithAccessPolicyE(fn func(r *http.Request, user string) (Access, error)) Option {
	return func(o *options) { o.access = fn }
}

// Limits 是连接与频率限制。字段为 0 时使用默认值, 为负时不限制。
type Limits struct {
	MaxSSEPerUser      int // 每用户 SSE 连接数, 默认 8
	MaxWSPerUser       int // 每用户对话 WebSocket 连接数, 默认 4
	ChatTurnsPerMinute int // 每用户每分钟对话轮次(模型调用是真金白银), 默认 20
	WritesPerMinute    int // 每用户每分钟写操作次数, 默认 240
	MaxKVKeysPerUser   int // 每用户 KV 键数量(排除内核保留键), 默认 256
}

func (l Limits) normalized() Limits {
	def := func(v, d int) int {
		if v == 0 {
			return d
		}
		return v
	}
	return Limits{
		MaxSSEPerUser:      def(l.MaxSSEPerUser, 8),
		MaxWSPerUser:       def(l.MaxWSPerUser, 4),
		ChatTurnsPerMinute: def(l.ChatTurnsPerMinute, 20),
		WritesPerMinute:    def(l.WritesPerMinute, 240),
		MaxKVKeysPerUser:   def(l.MaxKVKeysPerUser, 256),
	}
}

// WithLimits 覆盖默认限额。
func WithLimits(l Limits) Option {
	return func(o *options) { o.limits = l.normalized() }
}

// Metrics 是可选的指标接收器, 宿主用它接入自己的监控(Prometheus 等)。
// 指标名: sse_connections(±1)/ws_sessions(±1)/events_published/chat_turns/
// writes/errors_reported/rate_limited/auth_rejected。
type Metrics interface {
	Add(name string, delta float64)
	Observe(name string, value float64)
}

// WithMetrics 注册指标接收器。无论是否注册, Handler.Stats() 都可读内置计数。
func WithMetrics(m Metrics) Option {
	return func(o *options) { o.metrics = m }
}

// WithBroadcaster 替换事件广播通道(多副本部署必须, 见 Broadcaster 文档)。
// 为了避免数据库提交与消息发布反序，该安全默认只向外部通道
// 发送不携带状态载荷的 resync。Handler.Close 不接管外部 Broadcaster
// 的生命周期；宿主仍须关闭它及其客户端。
func WithBroadcaster(b Broadcaster) Option {
	return func(o *options) { o.broadcaster = b }
}

// WithOrderedBroadcaster 显式声明宿主能保证同一用户的所有 Store 写入
// （跨全部 Handler/副本）从提交前到 Publish 返回都处于一个全局串行
// 边界内，因而允许 Handler 保留 items/templates/kv/pages 增量载荷。
//
// 这是一个需要宿主主动选择的危险能力：Broadcaster 自己按到达顺序
// Publish 并不能修复两个副本“先提交、后发布”的反序。普通 Redis
// Pub/Sub、NATS 或“数据库提交后再 publish”的实现都不满足条件，应继续
// 使用 WithBroadcaster。典型安全场景是每个用户只有一个全局 writer，或宿主
// 的分布式锁覆盖完整 Handler 写调用。Handler 无法验证该前提。
func WithOrderedBroadcaster(b Broadcaster) Option {
	return func(o *options) {
		if nilcheck.IsNil(b) {
			// Preserve a typed nil so NewHandler can identify the invalid explicit
			// dependency instead of hiding it inside a non-nil ordered wrapper.
			o.broadcaster = b
			return
		}
		o.broadcaster = wrapOrderedBroadcaster(b)
	}
}

var (
	// ErrServiceScope 表示会话后端尝试访问非当前用户数据。
	ErrServiceScope = errors.New("service user scope mismatch")
	// ErrAccessDenied 表示连接建立后鉴权/访问策略已撤销。
	ErrAccessDenied = errors.New("access denied")
	// ErrWriteRateLimited 表示 HTTP/AI 共享的用户写频率额度已用尽。
	ErrWriteRateLimited = errors.New("write rate limit exceeded")
)

// governedService 是只交给单条 AI 会话的 Service 视图。它绑定用户，
// 并在每次调用时重新检查认证/访问策略，使长连接不会继续使用
// 建连时的过期权限。写操作与 HTTP 共用 WritesPerMinute 额度。
type governedService struct {
	h           *Handler
	request     *http.Request
	user        string
	source      string
	requireChat bool
}

func (s *governedService) check(ctx context.Context, user string, write bool) (context.Context, error) {
	if ctx == nil || ctx.Err() != nil || s.h.closed.Load() || s.request.Context().Err() != nil {
		return ctx, ErrAccessDenied
	}
	if strings.TrimSpace(user) != s.user {
		return ctx, ErrServiceScope
	}
	if !s.h.requestUserValid(s.request, s.user) {
		return ctx, ErrAccessDenied
	}
	access, err := s.h.accessDecision(s.request, s.user)
	if err != nil {
		return ctx, ErrAccessDenied
	}
	if s.requireChat && access.ChatDisabled || write && access.ReadOnly {
		return ctx, ErrAccessDenied
	}
	if write && !s.h.allowRate("w:"+s.user, s.h.opts.limits.WritesPerMinute) {
		s.h.metric("rate_limited", 1)
		return ctx, ErrWriteRateLimited
	}
	return withMutationSource(ctx, s.source), nil
}

func (s *governedService) ListItems(ctx context.Context, user string) ([]Item, error) {
	ctx, err := s.check(ctx, user, false)
	if err != nil {
		return nil, err
	}
	return s.h.ListItems(ctx, user)
}

func (s *governedService) PutItems(ctx context.Context, user string, items []Item, _, note string) error {
	ctx, err := s.check(ctx, user, true)
	if err != nil {
		return err
	}
	return s.h.PutItems(ctx, user, items, s.source, note)
}

func (s *governedService) DeleteItems(ctx context.Context, user string, ids []string, _, note string) ([]string, error) {
	ctx, err := s.check(ctx, user, true)
	if err != nil {
		return nil, err
	}
	return s.h.DeleteItems(ctx, user, ids, s.source, note)
}

func (s *governedService) ReplaceItems(ctx context.Context, user string, items []Item, _, note string) error {
	ctx, err := s.check(ctx, user, true)
	if err != nil {
		return err
	}
	return s.h.ReplaceItems(ctx, user, items, s.source, note)
}

func (s *governedService) ListRevisions(ctx context.Context, user string) ([]RevisionInfo, error) {
	ctx, err := s.check(ctx, user, false)
	if err != nil {
		return nil, err
	}
	return s.h.ListRevisions(ctx, user)
}

func (s *governedService) RevisionItems(ctx context.Context, user, id string) ([]Item, error) {
	ctx, err := s.check(ctx, user, false)
	if err != nil {
		return nil, err
	}
	return s.h.RevisionItems(ctx, user, id)
}

func (s *governedService) Rollback(ctx context.Context, user, revisionID, _ string) error {
	ctx, err := s.check(ctx, user, true)
	if err != nil {
		return err
	}
	return s.h.Rollback(ctx, user, revisionID, s.source)
}

func (s *governedService) KVGet(ctx context.Context, user, key string) (json.RawMessage, error) {
	ctx, err := s.check(ctx, user, false)
	if err != nil {
		return nil, err
	}
	return s.h.KVGet(ctx, user, key)
}

func (s *governedService) KVSet(ctx context.Context, user, key string, value json.RawMessage) error {
	ctx, err := s.check(ctx, user, true)
	if err != nil {
		return err
	}
	return s.h.KVSet(ctx, user, key, value)
}

func (s *governedService) KVDelete(ctx context.Context, user, key string) error {
	ctx, err := s.check(ctx, user, true)
	if err != nil {
		return err
	}
	return s.h.KVDelete(ctx, user, key)
}

func (s *governedService) PageErrors(ctx context.Context, user string) ([]PageError, error) {
	ctx, err := s.check(ctx, user, false)
	if err != nil {
		return nil, err
	}
	return s.h.PageErrors(ctx, user)
}

func (s *governedService) ReportErrors(ctx context.Context, user string, errs []PageError) error {
	ctx, err := s.check(ctx, user, true)
	if err != nil {
		return err
	}
	return s.h.ReportErrors(ctx, user, errs)
}

func (s *governedService) ListPages(ctx context.Context, user string) ([]Page, error) {
	ctx, err := s.check(ctx, user, false)
	if err != nil {
		return nil, err
	}
	return s.h.ListPages(ctx, user)
}

func (s *governedService) SetPage(ctx context.Context, user string, page Page) error {
	ctx, err := s.check(ctx, user, true)
	if err != nil {
		return err
	}
	return s.h.SetPage(ctx, user, page)
}

func (s *governedService) DeletePage(ctx context.Context, user, name, _ string) ([]string, error) {
	ctx, err := s.check(ctx, user, true)
	if err != nil {
		return nil, err
	}
	return s.h.DeletePage(ctx, user, name, s.source)
}

// UserExport 是单用户全量数据包(备份/迁移/模板预置的 JSON 契约)。
// Version 为 0/1 的包按旧版契约读取；当前导出写入 CurrentDataVersion。
type UserExport struct {
	Version               int                        `json:"version,omitempty"`
	Items                 []Item                     `json:"items"`
	Pages                 []Page                     `json:"pages,omitempty"`
	Templates             []Template                 `json:"templates,omitempty"`
	RootTemplateID        string                     `json:"root_template_id,omitempty"`
	TemplateRevisionClock uint64                     `json:"template_revision_clock,omitempty"`
	KV                    map[string]json.RawMessage `json:"kv,omitempty"`
	Revisions             []Revision                 `json:"revisions,omitempty"`
}
