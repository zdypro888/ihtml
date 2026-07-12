package ihtml

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
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
	return func(o *options) { o.audit = fn }
}

// Access 是一次请求的访问策略结果。
type Access struct {
	// ReadOnly 拒绝一切写操作(HTTP 写接口与 AI 工具写), 用于
	// 客服代查/演示账号/免费套餐只读等场景。
	ReadOnly bool
	// ChatDisabled 对该用户禁用 AI 对话(入口隐藏, WS 拒绝)。
	ChatDisabled bool
}

// WithAccessPolicy 设置按请求/用户的访问策略。
func WithAccessPolicy(fn func(r *http.Request, user string) Access) Option {
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
func WithBroadcaster(b Broadcaster) Option {
	return func(o *options) { o.broadcaster = b }
}

// UserExport 是单用户全量数据包(备份/迁移/模板预置的 JSON 契约)。
type UserExport struct {
	Items     []Item                     `json:"items"`
	Pages     []Page                     `json:"pages,omitempty"`
	KV        map[string]json.RawMessage `json:"kv,omitempty"`
	Revisions []Revision                 `json:"revisions,omitempty"`
}
