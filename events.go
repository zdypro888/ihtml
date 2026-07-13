package ihtml

import (
	"sync"
)

// SSE 事件协议常量。这些字符串是公共线格式契约(见 COMPATIBILITY.md):
// 同一协议版本内只做增量扩展, 消费者可安全地按常量比较。
const (
	// EventItems 表示 Item 集合变化。
	EventItems = "items"
	// EventTemplates 表示布局模板变化。
	EventTemplates = "templates"
	// EventKV 表示某个用户级 KV 键变化(Event.Key 为键名)。
	EventKV = "kv"
	// EventPages 表示页面注册表变化。
	EventPages = "pages"
	// EventResync 要求客户端放弃增量、全量刷新(Event.Reason 说明原因)。
	EventResync = "resync"

	// EventOpPut 是 items/templates 事件的"写入/更新"操作。
	EventOpPut = "put"
	// EventOpPublish 是模板 Draft 发布为 Published 的操作。
	EventOpPublish = "publish"
	// EventOpDelete 是删除操作(Event.IDs 为目标)。
	EventOpDelete = "delete"
	// EventOpReplace 是 Item 全集整体替换操作。
	EventOpReplace = "replace"
	// EventOpRollback 是快照回滚操作。
	EventOpRollback = "rollback"
	// EventOpImport 是全量导入操作。
	EventOpImport = "import"
	// EventOpPurge 是用户数据清除操作。
	EventOpPurge = "purge"

	// ResyncSubscribe: 订阅建立时的基线 resync。
	ResyncSubscribe = "subscribe"
	// ResyncOverflow: 订阅积压溢出、增量队列已被清空。
	ResyncOverflow = "overflow"
	// ResyncPeriodic: 周期性校准(兜底不能报告丢包的外部 Broadcaster)。
	ResyncPeriodic = "periodic"
	// ResyncExternal: 外部/跨副本 Broadcaster 收到变更通知后要求全量刷新。
	// 持久化提交与消息发布不在同一事务内，因此外部通道不携带可能
	// 因发布反序而过期的增量载荷，客户端改为重读已提交的权威状态。
	ResyncExternal = "external"
)

// Event 是推送给页面内核的实时事件(经 SSE 下发)。
type Event struct {
	// Type: "items" 表示 Item 集合变化; "templates" 表示模板变化;
	// "kv" 表示某个 KV 键变化; "pages" 表示页面注册表变化;
	// "resync" 表示客户端必须全量刷新。
	Type string `json:"type"`
	// Op 是 items/templates 事件的操作: put / publish / delete / replace /
	// rollback / import / purge。
	Op string `json:"op,omitempty"`
	// Items 携带 put 的新内容, 内核原地应用。
	Items []Item `json:"items,omitempty"`
	// Templates 携带模板 put/publish 的最新完整值。
	Templates []Template `json:"templates,omitempty"`
	// IDs 携带 delete 的目标。
	IDs []string `json:"ids,omitempty"`
	// Key 是 kv 事件变化的键名。
	Key string `json:"key,omitempty"`
	// By 是变更来源(api / chat / import 等), 供宿主联动与前端展示。
	By string `json:"by,omitempty"`
	// Version 是某用户事件流的单调版本。默认 hub 总会填充；
	// 外部 Broadcaster 若能提供全局单调版本也应填充。
	Version uint64 `json:"version,omitempty"`
	// Reason 说明 resync 原因: subscribe / overflow / periodic / external。
	Reason string `json:"reason,omitempty"`
}

// Broadcaster 把变更事件广播给某用户的全部页面订阅者。
// 默认实现是进程内 hub(单副本); 多副本部署时用 WithBroadcaster 接入
// Redis Pub/Sub / NATS 等跨节点通道, 否则副本 A 的写入无法推到
// 连接在副本 B 上的页面。
//
// Handler 只在内置进程内 hub 上发送低延迟增量。通过
// WithBroadcaster 注入的外部 Broadcaster 收到 reason=external 的
// resync 通知：Store 提交顺序与外部消息发布顺序可能相反，
// 全量重读才能保证客户端不应用过期载荷。只有宿主把同一用户在所有
// 副本上的完整写调用（含提交到 Publish）全局串行化时，才可显式使用
// WithOrderedBroadcaster 保留增量载荷；Broadcaster 内部排序本身不够。
type Broadcaster interface {
	// Publish 向该用户的所有订阅者投递事件。实现应保证不阻塞调用方。
	// 若实现会丢弃积压事件，必须改为投递 Type="resync" 以显式
	// 告知订阅者，不得静默丢弃。
	Publish(user string, ev Event)
	// Subscribe 订阅该用户的事件流; cancel 幂等, 调用后通道会被关闭。
	Subscribe(user string) (ch <-chan Event, cancel func())
}

// VersionedBroadcaster 是 Broadcaster 的可选扩展。多副本实现可用
// Redis INCR/流序号等返回用户级全局版本，使 SSE resync 事件
// 能建立准确基线。Publish 投递的 Event.Version 应与此版本一致。
type VersionedBroadcaster interface {
	Broadcaster
	CurrentVersion(user string) uint64
}

// hub 是进程内 Broadcaster 默认实现。订阅者积压时会清空
// 不完整的增量队列并投递 resync，不会静默丢事件。
type hub struct {
	mu       sync.Mutex
	subs     map[string]map[chan Event]struct{}
	versions map[string]uint64
	closed   bool
}

// orderedIncrementalBroadcaster 是包内标记。宿主不能通过偶然实现
// 某个方法而启用危险的增量路径；必须显式选择 WithOrderedBroadcaster，
// 该 Option 会用下面的私有 wrapper 标记它。
type orderedIncrementalBroadcaster interface {
	orderedIncrementalDelivery()
}

func (*hub) orderedIncrementalDelivery() {}

type explicitlyOrderedBroadcaster struct{ Broadcaster }

func (*explicitlyOrderedBroadcaster) orderedIncrementalDelivery() {}

type explicitlyOrderedVersionedBroadcaster struct{ VersionedBroadcaster }

func (*explicitlyOrderedVersionedBroadcaster) orderedIncrementalDelivery() {}

func wrapOrderedBroadcaster(b Broadcaster) Broadcaster {
	if versioned, ok := b.(VersionedBroadcaster); ok {
		return &explicitlyOrderedVersionedBroadcaster{VersionedBroadcaster: versioned}
	}
	return &explicitlyOrderedBroadcaster{Broadcaster: b}
}

func newHub() *hub {
	return &hub{
		subs:     make(map[string]map[chan Event]struct{}),
		versions: make(map[string]uint64),
	}
}

// Subscribe 实现 Broadcaster。
func (h *hub) Subscribe(user string) (<-chan Event, func()) {
	ch := make(chan Event, 16)
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		close(ch)
		return ch, func() {}
	}
	set := h.subs[user]
	if set == nil {
		set = make(map[chan Event]struct{})
		h.subs[user] = set
	}
	set[ch] = struct{}{}
	h.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			if set := h.subs[user]; set != nil {
				if _, ok := set[ch]; ok {
					delete(set, ch)
					close(ch)
				}
				if len(set) == 0 {
					delete(h.subs, user)
					delete(h.versions, user)
				}
			}
			h.mu.Unlock()
		})
	}
	return ch, cancel
}

// Publish 实现 Broadcaster。
func (h *hub) Publish(user string, ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	if len(h.subs[user]) == 0 {
		return // 无订阅者时不需要保留用户版本，避免键空间泄漏。
	}
	h.versions[user]++
	ev.Version = h.versions[user]
	for ch := range h.subs[user] {
		select {
		case ch <- ev:
		default:
			// 队列中已经存在缺口，保留任何增量都可能产生
			// 错误状态。清空后投递一条不可丢的全量刷新信号。
			draining := true
			for draining {
				select {
				case <-ch:
				default:
					draining = false
				}
			}
			ch <- Event{Type: "resync", Reason: "overflow", Version: ev.Version}
		}
	}
}

// CurrentVersion 返回当前用户事件版本。
func (h *hub) CurrentVersion(user string) uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.versions[user]
}

// close 关闭全部订阅(优雅停机): 订阅通道关闭后 SSE 循环自然退出。
func (h *hub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for user, set := range h.subs {
		for ch := range set {
			close(ch)
		}
		delete(h.subs, user)
		delete(h.versions, user)
	}
}
