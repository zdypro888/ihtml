package ihtml

import (
	"sync"
)

// Event 是推送给页面内核的实时事件(经 SSE 下发)。
type Event struct {
	// Type: "items" 表示 Item 集合变化; "kv" 表示某个 KV 键变化(如主题);
	// "pages" 表示页面注册表变化。
	Type string `json:"type"`
	// Op 是 items 事件的操作: put / delete / replace / rollback / import / purge。
	Op string `json:"op,omitempty"`
	// Items 携带 put 的新内容, 内核原地应用。
	Items []Item `json:"items,omitempty"`
	// IDs 携带 delete 的目标。
	IDs []string `json:"ids,omitempty"`
	// Key 是 kv 事件变化的键名。
	Key string `json:"key,omitempty"`
	// By 是变更来源(api / chat / import 等), 供宿主联动与前端展示。
	By string `json:"by,omitempty"`
}

// Broadcaster 把变更事件广播给某用户的全部页面订阅者。
// 默认实现是进程内 hub(单副本); 多副本部署时用 WithBroadcaster 接入
// Redis Pub/Sub / NATS 等跨节点通道, 否则副本 A 的写入无法推到
// 连接在副本 B 上的页面。
type Broadcaster interface {
	// Publish 向该用户的所有订阅者投递事件。实现应保证不阻塞调用方。
	Publish(user string, ev Event)
	// Subscribe 订阅该用户的事件流; cancel 幂等, 调用后通道会被关闭。
	Subscribe(user string) (ch <-chan Event, cancel func())
}

// hub 是进程内 Broadcaster 默认实现。订阅者写阻塞时丢弃该条
// (内核在 SSE 重连后会整体 refresh 兜底)。
type hub struct {
	mu     sync.Mutex
	subs   map[string]map[chan Event]struct{}
	closed bool
}

func newHub() *hub {
	return &hub{subs: make(map[string]map[chan Event]struct{})}
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
	for ch := range h.subs[user] {
		select {
		case ch <- ev:
		default: // 订阅者积压则丢弃, 依赖重连 refresh
		}
	}
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
	}
}
