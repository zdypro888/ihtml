// Package redisbroadcast 提供基于 Redis Pub/Sub 的 ihtml.Broadcaster 实现,
// 用于多副本部署: 任一副本的写事件会推送到连接在其他副本上的页面, 并以
// Redis INCR 维护用户级全局单调版本(实现 ihtml.VersionedBroadcaster)。
//
//	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
//	rb, _ := redisbroadcast.New(client, redisbroadcast.Options{})
//	defer rb.Close()
//	handler, _ := ihtml.NewHandler(store, ihtml.WithBroadcaster(rb), ...)
//
// 与 database/sql 一样, *redis.Client 的生命周期由宿主拥有。
//
// 一致性说明: 版本递增与事件发布在一个 Lua 脚本内原子完成, 因此订阅者观察
// 到的版本序与发布序严格一致。Redis 短暂不可用或重连期间丢失的事件由内核
// 的周期性 resync(默认 60s)与版本缺口检测兜底, 不会静默错过任何变更。
// 本地副本自身的事件同样经 Redis 往返投递, 保证所有副本看到同一事件顺序。
package redisbroadcast

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/zdypro888/ihtml"
)

// ErrNilClient 表示 New 未收到 Redis 客户端。
var ErrNilClient = errors.New("redisbroadcast: redis client is required")

// Options 是可选配置, 零值即可用。
type Options struct {
	// KeyPrefix 是版本键与频道名前缀, 默认 "ihtml"。
	// 版本键: <prefix>:ver:<user>; 频道: <prefix>:ev:<user>。
	KeyPrefix string
	// SubscriberBuffer 是每个订阅者的通道缓冲, 默认 16(与进程内 hub 一致)。
	// 积压溢出时该订阅者的增量队列会被清空并投递 resync, 不会静默丢事件。
	SubscriberBuffer int
	// OpTimeout 是单次 Redis 操作(发布/查版本)的超时, 默认 3s。
	OpTimeout time.Duration
	// Logger 记录后台投递错误, 默认 slog.Default()。
	Logger *slog.Logger
}

func (o *Options) fill() {
	if o.KeyPrefix == "" {
		o.KeyPrefix = "ihtml"
	}
	if o.SubscriberBuffer <= 0 {
		o.SubscriberBuffer = 16
	}
	if o.OpTimeout <= 0 {
		o.OpTimeout = 3 * time.Second
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
}

// publishScript 原子地推进用户版本并发布事件, 保证版本序与发布序一致。
// 载荷是 {"v":<version>,"e":<event>} 信封。
var publishScript = redis.NewScript(`
local v = redis.call('INCR', KEYS[1])
redis.call('PUBLISH', KEYS[2], '{"v":' .. v .. ',"e":' .. ARGV[1] .. '}')
return v
`)

type envelope struct {
	V uint64          `json:"v"`
	E json.RawMessage `json:"e"`
}

// Broadcaster 实现 ihtml.VersionedBroadcaster。
type Broadcaster struct {
	client redis.UniversalClient
	opts   Options

	pubsub *redis.PubSub

	mu     sync.Mutex
	subs   map[string]map[chan ihtml.Event]struct{}
	closed bool
}

var _ ihtml.VersionedBroadcaster = (*Broadcaster)(nil)

// New 创建 Broadcaster 并启动共享的订阅泵。client 由宿主创建与关闭;
// Broadcaster 自身用 Close 释放订阅连接。
func New(client redis.UniversalClient, opts Options) (*Broadcaster, error) {
	if client == nil {
		return nil, ErrNilClient
	}
	opts.fill()
	b := &Broadcaster{
		client: client,
		opts:   opts,
		subs:   make(map[string]map[chan ihtml.Event]struct{}),
	}
	// 一个进程共享一条订阅连接, 按需 Subscribe/Unsubscribe 用户频道。
	b.pubsub = client.Subscribe(context.Background())
	go b.pump()
	return b, nil
}

func (b *Broadcaster) verKey(user string) string  { return b.opts.KeyPrefix + ":ver:" + user }
func (b *Broadcaster) channel(user string) string { return b.opts.KeyPrefix + ":ev:" + user }

// Publish 实现 ihtml.Broadcaster。版本推进与发布对全部副本原子生效;
// Redis 不可用时记录错误并放弃本条(由内核周期 resync 兜底), 不阻塞写路径。
func (b *Broadcaster) Publish(user string, ev ihtml.Event) {
	ctx, cancel := context.WithTimeout(context.Background(), b.opts.OpTimeout)
	defer cancel()
	ev.Version = 0 // 版本由脚本注入信封, 事件体内不再携带。
	payload, err := json.Marshal(ev)
	if err != nil {
		b.opts.Logger.Error("redisbroadcast: marshal event", "err", err)
		return
	}
	if err := publishScript.Run(ctx, b.client, []string{b.verKey(user), b.channel(user)}, string(payload)).Err(); err != nil {
		b.opts.Logger.Error("redisbroadcast: publish", "user", user, "err", err)
	}
}

// Subscribe 实现 ihtml.Broadcaster。
func (b *Broadcaster) Subscribe(user string) (<-chan ihtml.Event, func()) {
	ch := make(chan ihtml.Event, b.opts.SubscriberBuffer)
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		close(ch)
		return ch, func() {}
	}
	set := b.subs[user]
	if set == nil {
		set = make(map[chan ihtml.Event]struct{})
		b.subs[user] = set
		// 该用户的第一个本地订阅者: 挂上 Redis 频道。
		if err := b.pubsub.Subscribe(context.Background(), b.channel(user)); err != nil {
			b.opts.Logger.Error("redisbroadcast: subscribe channel", "user", user, "err", err)
		}
	}
	set[ch] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			if set := b.subs[user]; set != nil {
				if _, ok := set[ch]; ok {
					delete(set, ch)
					close(ch)
				}
				if len(set) == 0 {
					delete(b.subs, user)
					if !b.closed {
						if err := b.pubsub.Unsubscribe(context.Background(), b.channel(user)); err != nil {
							b.opts.Logger.Error("redisbroadcast: unsubscribe channel", "user", user, "err", err)
						}
					}
				}
			}
			b.mu.Unlock()
		})
	}
	return ch, cancel
}

// CurrentVersion 实现 ihtml.VersionedBroadcaster: 读取用户级全局版本。
// Redis 不可用时返回 0, resync 事件将不带基线版本(客户端仍会全量刷新)。
func (b *Broadcaster) CurrentVersion(user string) uint64 {
	ctx, cancel := context.WithTimeout(context.Background(), b.opts.OpTimeout)
	defer cancel()
	version, err := b.client.Get(ctx, b.verKey(user)).Uint64()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			b.opts.Logger.Error("redisbroadcast: current version", "user", user, "err", err)
		}
		return 0
	}
	return version
}

// pump 消费共享订阅连接并按 hub 相同的溢出语义分发给本地订阅者。
func (b *Broadcaster) pump() {
	prefix := b.opts.KeyPrefix + ":ev:"
	for msg := range b.pubsub.Channel(redis.WithChannelSize(512)) {
		user := strings.TrimPrefix(msg.Channel, prefix)
		var env envelope
		if err := json.Unmarshal([]byte(msg.Payload), &env); err != nil {
			b.opts.Logger.Error("redisbroadcast: corrupt payload", "channel", msg.Channel, "err", err)
			continue
		}
		var ev ihtml.Event
		if err := json.Unmarshal(env.E, &ev); err != nil {
			b.opts.Logger.Error("redisbroadcast: corrupt event", "channel", msg.Channel, "err", err)
			continue
		}
		ev.Version = env.V

		b.mu.Lock()
		for ch := range b.subs[user] {
			select {
			case ch <- ev:
			default:
				// 与进程内 hub 一致: 队列出现缺口时保留增量只会造成错误状态,
				// 清空后投递一条不可丢的全量刷新信号。
				draining := true
				for draining {
					select {
					case <-ch:
					default:
						draining = false
					}
				}
				ch <- ihtml.Event{Type: ihtml.EventResync, Reason: ihtml.ResyncOverflow, Version: ev.Version}
			}
		}
		b.mu.Unlock()
	}
}

// Close 释放订阅连接并关闭全部本地订阅通道(优雅停机)。
// 不关闭宿主传入的 Redis 客户端。
func (b *Broadcaster) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	for user, set := range b.subs {
		for ch := range set {
			close(ch)
		}
		delete(b.subs, user)
	}
	b.mu.Unlock()
	return b.pubsub.Close()
}
