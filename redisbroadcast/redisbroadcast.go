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
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/internal/nilcheck"
)

var (
	// ErrNilClient 表示 New 未收到 Redis 客户端。
	ErrNilClient = errors.New("redisbroadcast: redis client is required")
	// ErrInvalidKeyPrefix 表示 KeyPrefix 含有会改变 Redis Cluster hash tag
	// 解析的花括号。
	ErrInvalidKeyPrefix = errors.New("redisbroadcast: key prefix must not contain braces")
)

// Options 是可选配置, 零值即可用。
type Options struct {
	// KeyPrefix 是版本键与频道名前缀, 默认 "ihtml"。
	// 用户标识会先做 SHA-256，不会以原文进入 Redis 键或频道；
	// 版本键和频道共用 Redis Cluster hash tag，始终位于同一 slot。
	// 不得包含 "{" 或 "}"，以免改变 Cluster hash tag 解析。
	KeyPrefix string
	// SubscriberBuffer 是每个订阅者的通道缓冲, 默认 16(与进程内 hub 一致)。
	// 积压溢出时该订阅者的增量队列会被清空并投递 resync, 不会静默丢事件。
	SubscriberBuffer int
	// PublishBuffer 是写路径到 Redis 后台发布器的有界队列，默认 256。
	// 满载时 Publish 仍立即返回，后台会合并成一条全局 overflow resync。
	PublishBuffer int
	// LegacyNamespaceMigration 显式开启旧 Redis 命名空间的滚动升级桥接。
	// 开启后，每个用户会同时订阅和发布到当前 SHA-256/Cluster-safe 命名与
	// 旧版 <prefix>:ver:<raw-user>、<prefix>:ev:<raw-user> 命名；从旧频道
	// 收到的任何增量都会先降级为不携带载荷的 resync。
	//
	// 默认必须保持关闭：旧命名会把原始用户标识写入 Redis 键和频道，可能
	// 暴露个人信息。此选项仅用于受控、短期滚动升级；所有旧实例退出并确认
	// 旧命名空间不再使用后应立即关闭。旧频道失败重试同样受 PublishBuffer
	// 限制；极端持续故障超过该边界时，旧实例最迟由自身的周期 resync 收敛。
	LegacyNamespaceMigration bool
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
	if o.PublishBuffer <= 0 {
		o.PublishBuffer = 256
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

// legacyPublishScript 只把旧版本键声明为 Redis Cluster 的 KEYS；旧频道名
// 作为参数传给 PUBLISH。这样既保持旧 wire namespace，又避免把两个不同 slot
// 的旧标识同时交给 ClusterClient 触发 CROSSSLOT。
var legacyPublishScript = redis.NewScript(`
local v = redis.call('INCR', KEYS[1])
redis.call('PUBLISH', ARGV[1], '{"v":' .. v .. ',"e":' .. ARGV[2] .. '}')
return v
`)

type envelope struct {
	V uint64          `json:"v"`
	E json.RawMessage `json:"e"`
}

type publishRequest struct {
	token string
	user  string // 仅 LegacyNamespaceMigration 开启时保留，用于旧 raw-user 命名。
	event ihtml.Event
}

type publishResult struct {
	current bool
	legacy  bool
}

type subscriptionState struct {
	token    string
	channels []string
	pending  map[string]struct{}
	ready    chan struct{}
	once     sync.Once
}

func (s *subscriptionState) complete() bool {
	completed := false
	s.once.Do(func() {
		close(s.ready)
		completed = true
	})
	return completed
}

// acknowledge 必须在 Broadcaster.mu 下调用。
func (s *subscriptionState) acknowledge(channel string) {
	delete(s.pending, channel)
	if len(s.pending) == 0 {
		s.complete()
	}
}

type channelBinding struct {
	token  string
	legacy bool
}

// Broadcaster 实现 ihtml.VersionedBroadcaster。
type Broadcaster struct {
	client redis.UniversalClient
	opts   Options
	ctx    context.Context
	cancel context.CancelFunc

	pubsub        *redis.PubSub
	pubsubMu      sync.Mutex
	publishMu     sync.RWMutex
	publishQ      chan publishRequest
	overflowQ     chan struct{} // 容量 1：任意多溢出合并为一次全局 resync
	drainCh       chan struct{}
	drained       chan struct{}
	legacyQ       chan publishRequest
	legacyDrainCh chan struct{}
	legacyDrained chan struct{}
	wg            sync.WaitGroup
	closed        atomic.Bool

	mu            sync.Mutex
	subs          map[string]map[chan ihtml.Event]struct{} // SHA-256 token -> local subscribers
	channels      map[string]channelBinding                // Redis channel -> token/namespace
	subscriptions map[string]*subscriptionState            // token -> subscription acknowledgement
}

var _ ihtml.VersionedBroadcaster = (*Broadcaster)(nil)

// New 创建 Broadcaster 并启动共享的订阅泵。client 由宿主创建与关闭;
// Broadcaster 自身用 Close 释放订阅连接。
func New(client redis.UniversalClient, opts Options) (*Broadcaster, error) {
	if nilcheck.IsNil(client) {
		return nil, ErrNilClient
	}
	opts.fill()
	if strings.ContainsAny(opts.KeyPrefix, "{}") {
		return nil, fmt.Errorf("%w: %q", ErrInvalidKeyPrefix, opts.KeyPrefix)
	}
	ctx, cancel := context.WithCancel(context.Background())
	b := &Broadcaster{
		client:        client,
		opts:          opts,
		ctx:           ctx,
		cancel:        cancel,
		publishQ:      make(chan publishRequest, opts.PublishBuffer),
		overflowQ:     make(chan struct{}, 1),
		drainCh:       make(chan struct{}),
		drained:       make(chan struct{}),
		subs:          make(map[string]map[chan ihtml.Event]struct{}),
		channels:      make(map[string]channelBinding),
		subscriptions: make(map[string]*subscriptionState),
	}
	// 全局频道只用于发布队列溢出：一条有界的 resync 可以覆盖任意多
	// 被合并的用户变更，避免为无限多用户保留无界 dirty map。
	initCtx, initCancel := context.WithTimeout(ctx, opts.OpTimeout)
	b.pubsub = client.Subscribe(initCtx, b.globalChannel())
	initCancel()
	if opts.LegacyNamespaceMigration {
		b.legacyQ = make(chan publishRequest, opts.PublishBuffer)
		b.legacyDrainCh = make(chan struct{})
		b.legacyDrained = make(chan struct{})
	}
	b.wg.Add(2)
	go b.pump()
	go b.publishLoop()
	if opts.LegacyNamespaceMigration {
		b.wg.Add(1)
		go b.legacyRetryLoop()
	}
	return b, nil
}

func (b *Broadcaster) subjectToken(user string) string {
	sum := sha256.Sum256([]byte(b.opts.KeyPrefix + "\x00" + user))
	return fmt.Sprintf("%x", sum[:])
}

func (b *Broadcaster) slotTag(token string) string { return "{ihtml:" + token + "}" }

func (b *Broadcaster) verKeyForToken(token string) string {
	return b.opts.KeyPrefix + ":" + b.slotTag(token) + ":ver"
}

func (b *Broadcaster) channelForToken(token string) string {
	return b.opts.KeyPrefix + ":" + b.slotTag(token) + ":ev"
}

func (b *Broadcaster) verKey(user string) string  { return b.verKeyForToken(b.subjectToken(user)) }
func (b *Broadcaster) channel(user string) string { return b.channelForToken(b.subjectToken(user)) }

func (b *Broadcaster) legacyVerKey(user string) string {
	return b.opts.KeyPrefix + ":ver:" + user
}

func (b *Broadcaster) legacyChannel(user string) string {
	return b.opts.KeyPrefix + ":ev:" + user
}

func (b *Broadcaster) userChannels(user, token string) []string {
	channels := []string{b.channelForToken(token)}
	if b.opts.LegacyNamespaceMigration {
		channels = append(channels, b.legacyChannel(user))
	}
	return channels
}

func (b *Broadcaster) globalChannel() string {
	return b.opts.KeyPrefix + ":" + b.slotTag(b.subjectToken("__all__")) + ":ev-all"
}

func subjectLabel(token string) string {
	if len(token) > 12 {
		return token[:12]
	}
	return token
}

// Publish 实现 ihtml.Broadcaster。它只做有界内存入队，不等待 Redis。
//
// 外部发布不能与 Store 提交共享事务：早提交的请求可能晚发布。
// 因此这里不会转发增量载荷，而是统一发布权威 resync；即使入队顺序
// 与持久化顺序相反，订阅者也只会重读已提交的最新状态。
func (b *Broadcaster) Publish(user string, ev ihtml.Event) {
	b.publishMu.RLock()
	defer b.publishMu.RUnlock()
	if b.closed.Load() {
		return
	}
	reason := ev.Reason
	if ev.Type != ihtml.EventResync || reason == "" {
		reason = ihtml.ResyncExternal
	}
	req := publishRequest{
		token: b.subjectToken(user),
		event: ihtml.Event{Type: ihtml.EventResync, Reason: reason},
	}
	if b.opts.LegacyNamespaceMigration {
		req.user = user
	}
	select {
	case b.publishQ <- req:
	default:
		// 不能把溢出用户放进无界 map。合并成一个全局 resync 位，
		// 单容量 overflowQ 会把任意多次溢出合并，由后台独立重试。
		b.signalOverflow()
		// 旧实例不订阅新全局频道；迁移期间另用同样有界的 per-user
		// 重试队列把这条权威 resync 送进旧频道。
		b.signalLegacyRetry(req)
	}
}

func (b *Broadcaster) signalOverflow() {
	select {
	case b.overflowQ <- struct{}{}:
	default:
	}
}

func (b *Broadcaster) signalLegacyRetry(req publishRequest) {
	if !b.opts.LegacyNamespaceMigration {
		return
	}
	select {
	case b.legacyQ <- req:
	default:
		b.opts.Logger.Error("redisbroadcast: legacy migration retry queue overflow",
			"subject", subjectLabel(req.token))
	}
}

func (b *Broadcaster) publishLoop() {
	defer b.wg.Done()
	defer close(b.drained)
	for {
		// Close 关闭 drainCh 后优先进入排空模式，不再在普通 select
		// 中随机选择新工作。
		select {
		case <-b.drainCh:
			b.drainPublishQueue()
			return
		default:
		}
		select {
		case <-b.ctx.Done():
			return
		case <-b.drainCh:
			b.drainPublishQueue()
			return
		case req := <-b.publishQ:
			result := b.publishOne(req)
			if !result.current {
				// 入队成功只表示变更已被后台接受；若实际 Lua/网络操作
				// 失败，也必须显式留下一个有界 dirty 位。Redis 恢复后
				// 全局 resync 会立即让所有本地订阅者重读权威状态。
				b.signalOverflow()
			}
			if !result.legacy {
				b.signalLegacyRetry(req)
			}
		case <-b.overflowQ:
			if !b.publishOverflowResync() {
				// Redis 故障时保留这一个有界 dirty 位并延迟重试；
				// 写请求从不等待重试。
				timer := time.NewTimer(min(b.opts.OpTimeout, 250*time.Millisecond))
				select {
				case <-b.ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
				b.signalOverflow()
			}
		}
	}
}

// legacyRetryLoop 是迁移桥专用的有界 per-user 重试 actor。旧实例并不
// 订阅新 globalChannel，因此 current namespace 的 overflow 通知不能替代
// 一次失败的旧频道发布。队列只在显式迁移模式下存在。
func (b *Broadcaster) legacyRetryLoop() {
	defer b.wg.Done()
	defer close(b.legacyDrained)
	retryDelay := min(b.opts.OpTimeout, 250*time.Millisecond)
	var pending *publishRequest
	var retryTimer *time.Timer
	stopTimer := func() {
		if retryTimer == nil || retryTimer.Stop() {
			return
		}
		select {
		case <-retryTimer.C:
		default:
		}
	}

	for {
		if pending == nil {
			select {
			case <-b.ctx.Done():
				return
			case <-b.legacyDrainCh:
				b.drainLegacyQueue(nil)
				return
			case req := <-b.legacyQ:
				if b.publishLegacy(req) {
					continue
				}
				pending = &req
				retryTimer = time.NewTimer(retryDelay)
			}
			continue
		}

		select {
		case <-b.ctx.Done():
			stopTimer()
			return
		case <-b.legacyDrainCh:
			stopTimer()
			b.drainLegacyQueue(pending)
			return
		case <-retryTimer.C:
			if b.publishLegacy(*pending) {
				pending = nil
				retryTimer = nil
				continue
			}
			retryTimer.Reset(retryDelay)
		}
	}
}

func (b *Broadcaster) drainLegacyQueue(pending *publishRequest) {
	if pending != nil && !b.publishLegacy(*pending) {
		return
	}
	for {
		select {
		case <-b.ctx.Done():
			return
		default:
		}
		select {
		case req := <-b.legacyQ:
			if !b.publishLegacy(req) {
				return
			}
		default:
			return
		}
	}
}

// drainPublishQueue 在 Close 的总 OpTimeout 内 best-effort 发完已接受的
// 请求。排空期间不对失败的 overflow 无限重试；超时后 b.ctx
// 被取消，本循环立即退出。
func (b *Broadcaster) drainPublishQueue() {
	for {
		select {
		case <-b.ctx.Done():
			return
		default:
		}
		select {
		case req := <-b.publishQ:
			result := b.publishOne(req)
			if !result.legacy {
				b.signalLegacyRetry(req)
			}
			if !result.current {
				b.signalOverflow()
				return
			}
		case <-b.overflowQ:
			if !b.publishOverflowResync() {
				return
			}
		default:
			return
		}
	}
}

func (b *Broadcaster) publishOne(req publishRequest) publishResult {
	payload, err := json.Marshal(req.event)
	if err != nil {
		b.opts.Logger.Error("redisbroadcast: marshal event", "err", err)
		return publishResult{}
	}
	currentCtx, currentCancel := context.WithTimeout(b.ctx, b.opts.OpTimeout)
	currentErr := publishScript.Run(currentCtx, b.client,
		[]string{b.verKeyForToken(req.token), b.channelForToken(req.token)}, string(payload)).Err()
	currentCancel()
	if currentErr != nil {
		b.opts.Logger.Error("redisbroadcast: publish", "subject", subjectLabel(req.token), "err", currentErr)
	}

	legacyOK := true
	if b.opts.LegacyNamespaceMigration {
		legacyOK = b.publishLegacyPayload(req, payload)
	}
	return publishResult{current: currentErr == nil, legacy: legacyOK}
}

func (b *Broadcaster) publishLegacy(req publishRequest) bool {
	payload, err := json.Marshal(req.event)
	if err != nil {
		b.opts.Logger.Error("redisbroadcast: marshal legacy event", "err", err)
		return false
	}
	return b.publishLegacyPayload(req, payload)
}

func (b *Broadcaster) publishLegacyPayload(req publishRequest, payload []byte) bool {
	legacyCtx, legacyCancel := context.WithTimeout(b.ctx, b.opts.OpTimeout)
	defer legacyCancel()
	if err := legacyPublishScript.Run(legacyCtx, b.client,
		[]string{b.legacyVerKey(req.user)}, b.legacyChannel(req.user), string(payload)).Err(); err != nil {
		b.opts.Logger.Error("redisbroadcast: publish legacy namespace",
			"subject", subjectLabel(req.token), "err", err)
		return false
	}
	return true
}

func (b *Broadcaster) publishOverflowResync() bool {
	eventRaw, err := json.Marshal(ihtml.Event{Type: ihtml.EventResync, Reason: ihtml.ResyncOverflow})
	if err != nil {
		return false
	}
	payload, err := json.Marshal(envelope{E: eventRaw})
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(b.ctx, b.opts.OpTimeout)
	defer cancel()
	if err := b.client.Publish(ctx, b.globalChannel(), payload).Err(); err != nil {
		b.opts.Logger.Error("redisbroadcast: publish overflow resync", "err", err)
		return false
	}
	return true
}

// Subscribe 实现 ihtml.Broadcaster。
func (b *Broadcaster) Subscribe(user string) (<-chan ihtml.Event, func()) {
	ch := make(chan ihtml.Event, b.opts.SubscriberBuffer)
	token := b.subjectToken(user)
	channels := b.userChannels(user, token)
	b.mu.Lock()
	if b.closed.Load() {
		b.mu.Unlock()
		close(ch)
		return ch, func() {}
	}
	set := b.subs[token]
	first := set == nil
	state := b.subscriptions[token]
	if set == nil {
		set = make(map[chan ihtml.Event]struct{})
		b.subs[token] = set
		pending := make(map[string]struct{}, len(channels))
		for i, channel := range channels {
			pending[channel] = struct{}{}
			b.channels[channel] = channelBinding{token: token, legacy: i > 0}
		}
		state = &subscriptionState{
			token: token, channels: append([]string(nil), channels...),
			pending: pending, ready: make(chan struct{}),
		}
		b.subscriptions[token] = state
	}
	set[ch] = struct{}{}
	b.mu.Unlock()
	if first {
		b.establishSubscription(state)
	}
	// 每个调用者（包括与第一个调用者并发加入的调用者）都必须
	// 等 Redis 确认 subscribe。失败时 establishSubscription 会关闭
	// 这个 token 的全部本地通道。
	<-state.ready

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			last := false
			b.mu.Lock()
			if set := b.subs[token]; set != nil {
				if _, ok := set[ch]; ok {
					delete(set, ch)
					close(ch)
				}
				if len(set) == 0 {
					delete(b.subs, token)
					for _, channel := range state.channels {
						delete(b.channels, channel)
					}
					delete(b.subscriptions, token)
					last = true
				}
			}
			b.mu.Unlock()
			if last {
				b.unsubscribeIfUnused(token, state.channels)
			}
		})
	}
	return ch, cancel
}

// establishSubscription 不仅写入 SUBSCRIBE 命令，还等 pump 读到 Redis
// 的 subscribe acknowledgement 后才返回。只等命令写入会留下
// “首帧 resync 已完成、但频道尚未生效”的丢事件窗口。
func (b *Broadcaster) establishSubscription(state *subscriptionState) {
	ctx, cancel := context.WithTimeout(b.ctx, b.opts.OpTimeout)
	defer cancel()

	b.pubsubMu.Lock()
	b.mu.Lock()
	needed := !b.closed.Load() && b.subscriptions[state.token] == state && len(b.subs[state.token]) > 0
	b.mu.Unlock()
	if !needed {
		b.pubsubMu.Unlock()
		b.failSubscription(state)
		return
	}
	err := b.pubsub.Subscribe(ctx, state.channels...)
	b.pubsubMu.Unlock()
	if err != nil {
		b.opts.Logger.Error("redisbroadcast: subscribe channel", "subject", subjectLabel(state.token), "err", err)
		b.failSubscription(state)
		return
	}

	select {
	case <-state.ready:
		return
	case <-ctx.Done():
		b.opts.Logger.Error("redisbroadcast: subscribe acknowledgement", "subject", subjectLabel(state.token), "err", ctx.Err())
		b.failSubscription(state)
	}
}

// failSubscription 将失败结果与 go-redis 内部的期望频道一起清理。
// pubsubMu 保证旧订阅的 Unsubscribe 不会反序撤销随后创建的新订阅。
func (b *Broadcaster) failSubscription(state *subscriptionState) {
	b.pubsubMu.Lock()
	b.mu.Lock()
	current := b.subscriptions[state.token] == state
	won := current && state.complete()
	if won {
		for ch := range b.subs[state.token] {
			close(ch)
		}
		delete(b.subs, state.token)
		for _, channel := range state.channels {
			delete(b.channels, channel)
		}
		delete(b.subscriptions, state.token)
	}
	b.mu.Unlock()
	if won {
		// Unsubscribe 即使写命令失败也会从 go-redis 的重连期望集合
		// 删除频道。已取消的 context 让这一清理不再等待网络。
		cleanupCtx, cleanupCancel := context.WithCancel(context.Background())
		cleanupCancel()
		_ = b.pubsub.Unsubscribe(cleanupCtx, state.channels...)
	}
	b.pubsubMu.Unlock()
}

// confirmSubscription 由 pump 按 Redis 连接上的真实 ack 顺序调用。
func (b *Broadcaster) confirmSubscription(channel string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	binding, ok := b.channels[channel]
	if !ok {
		return
	}
	if state := b.subscriptions[binding.token]; state != nil {
		state.acknowledge(channel)
	}
}

// Unsubscribe 在一条 PubSub 连接上串行，并在真正发网络命令前
// 重查期望状态，避免“最后一个 cancel”反序取消刚刚新增的订阅。

func (b *Broadcaster) unsubscribeIfUnused(token string, channels []string) {
	b.pubsubMu.Lock()
	defer b.pubsubMu.Unlock()
	b.mu.Lock()
	unused := !b.closed.Load() && len(b.subs[token]) == 0
	b.mu.Unlock()
	if !unused {
		return
	}
	ctx, cancel := context.WithTimeout(b.ctx, b.opts.OpTimeout)
	defer cancel()
	if err := b.pubsub.Unsubscribe(ctx, channels...); err != nil {
		b.opts.Logger.Error("redisbroadcast: unsubscribe channel", "subject", subjectLabel(token), "err", err)
	}
}

// CurrentVersion 实现 ihtml.VersionedBroadcaster: 读取用户级全局版本。
// Redis 不可用时返回 0, resync 事件将不带基线版本(客户端仍会全量刷新)。
func (b *Broadcaster) CurrentVersion(user string) uint64 {
	ctx, cancel := context.WithTimeout(b.ctx, b.opts.OpTimeout)
	defer cancel()
	version, err := b.client.Get(ctx, b.verKey(user)).Uint64()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			b.opts.Logger.Error("redisbroadcast: current version", "subject", subjectLabel(b.subjectToken(user)), "err", err)
		}
		return 0
	}
	return version
}

// pump 消费共享订阅连接并按 hub 相同的溢出语义分发给本地订阅者。
func (b *Broadcaster) pump() {
	defer b.wg.Done()
	messages := b.pubsub.ChannelWithSubscriptions(redis.WithChannelSize(512))
	for {
		var next any
		select {
		case <-b.ctx.Done():
			return
		case received, ok := <-messages:
			if !ok {
				return
			}
			next = received
		}

		var msg *redis.Message
		switch received := next.(type) {
		case *redis.Subscription:
			if received.Kind == "subscribe" {
				b.confirmSubscription(received.Channel)
			}
			continue
		case *redis.Message:
			msg = received
		default:
			continue
		}
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
		if msg.Channel == b.globalChannel() {
			for _, set := range b.subs {
				b.dispatchLocked(set, ev)
			}
		} else if binding, ok := b.channels[msg.Channel]; ok {
			if binding.legacy {
				// 旧实例可能仍发送 items/templates/kv/pages 增量。其提交与
				// Redis 发布不共享顺序边界，迁移桥必须剥离全部载荷；旧
				// namespace 的版本也不能污染新 namespace 的版本基线。
				reason := ihtml.ResyncExternal
				if ev.Type == ihtml.EventResync && ev.Reason != "" {
					reason = ev.Reason
				}
				ev = ihtml.Event{Type: ihtml.EventResync, Reason: reason}
			}
			b.dispatchLocked(b.subs[binding.token], ev)
		}
		b.mu.Unlock()
	}
}

func (b *Broadcaster) dispatchLocked(set map[chan ihtml.Event]struct{}, ev ihtml.Event) {
	for ch := range set {
		select {
		case ch <- ev:
		default:
			// 队列出现缺口后任何已排队事件都不再权威；清空并投递
			// 一条不可丢的 resync。在 b.mu 保护下，cancel 不会并发关闭 ch。
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
}

// Close 释放订阅连接并关闭全部本地订阅通道(优雅停机)。
// 不关闭宿主传入的 Redis 客户端。
func (b *Broadcaster) Close() error {
	if !b.closed.CompareAndSwap(false, true) {
		return nil
	}
	// 等已经读到 closed=false 的 Publish 完成内存入队，之后不会
	// 再有新请求进入。这个 RWMutex 屏障消除“排空已结束，旧
	// Publish 才把请求放入队列”的关闭竞态。
	b.publishMu.Lock()
	close(b.drainCh)
	b.publishMu.Unlock()

	// 健康 Redis 上尽量发完 Publish 已接受的有界队列；整个排空
	// 共享一个 OpTimeout 关闭预算，而不是让每个队列元素各自再获得
	// 一份关闭时间。预算到期后取消 b.ctx，立即打断当前 Redis 操作
	// 和后续排空。
	timer := time.NewTimer(b.opts.OpTimeout)
	drained := false
	select {
	case <-b.drained:
		drained = true
	case <-timer.C:
		b.cancel()
	}
	if b.opts.LegacyNamespaceMigration {
		// 主发布 actor 先停止并把所有部分失败放进 legacyQ，迁移 actor
		// 才开始最终排空；否则后者可能先看见空队列退出，遗漏随后产生
		// 的旧频道重试。
		close(b.legacyDrainCh)
		if drained {
			select {
			case <-b.legacyDrained:
			case <-timer.C:
				b.cancel()
			}
		}
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	b.cancel()
	b.mu.Lock()
	for token, set := range b.subs {
		for ch := range set {
			close(ch)
		}
		delete(b.subs, token)
	}
	for token, state := range b.subscriptions {
		state.complete()
		delete(b.subscriptions, token)
	}
	clear(b.channels)
	b.mu.Unlock()
	b.pubsubMu.Lock()
	err := b.pubsub.Close()
	b.pubsubMu.Unlock()
	b.wg.Wait()
	return err
}
