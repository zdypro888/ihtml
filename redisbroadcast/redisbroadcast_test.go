package redisbroadcast

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/zdypro888/ihtml"
)

func newTestBroadcaster(t *testing.T, mr *miniredis.Miniredis, opts Options) *Broadcaster {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })
	b, err := New(client, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	return b
}

func TestNewRejectsTypedNilClient(t *testing.T) {
	var client *redis.Client
	b, err := New(client, Options{})
	if b != nil || !errors.Is(err, ErrNilClient) {
		t.Fatalf("broadcaster=%v error=%v", b, err)
	}
}

// waitSubscribed 等待 Redis 侧确认频道已有订阅者, 消除订阅与发布的竞态。
func waitSubscribed(t *testing.T, mr *miniredis.Miniredis, b *Broadcaster, user string, want int) {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		counts, err := client.PubSubNumSub(context.Background(), b.channel(user)).Result()
		if err == nil && counts[b.channel(user)] >= int64(want) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("channel %q never reached %d subscribers", b.channel(user), want)
}

func recvEvent(t *testing.T, ch <-chan ihtml.Event) ihtml.Event {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("subscription channel closed unexpectedly")
		}
		return ev
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for event")
	}
	return ihtml.Event{}
}

func waitVersion(t *testing.T, b *Broadcaster, user string, want uint64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b.CurrentVersion(user) >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("version for %q never reached %d (got %d)", user, want, b.CurrentVersion(user))
}

func TestPublishAssignsMonotonicVersions(t *testing.T) {
	mr := miniredis.RunT(t)
	b := newTestBroadcaster(t, mr, Options{})

	ch, cancel := b.Subscribe("alice")
	defer cancel()
	waitSubscribed(t, mr, b, "alice", 1)

	for range 3 {
		b.Publish("alice", ihtml.Event{Type: ihtml.EventItems, Op: ihtml.EventOpPut})
	}
	waitVersion(t, b, "alice", 3)
	for want := uint64(1); want <= 3; want++ {
		ev := recvEvent(t, ch)
		if ev.Version != want || ev.Type != ihtml.EventResync || ev.Reason != ihtml.ResyncExternal {
			t.Fatalf("event %d: %+v", want, ev)
		}
	}
	if v := b.CurrentVersion("alice"); v != 3 {
		t.Fatalf("CurrentVersion = %d, want 3", v)
	}
	if v := b.CurrentVersion("nobody"); v != 0 {
		t.Fatalf("CurrentVersion for unknown user = %d, want 0", v)
	}
}

// 两个 Broadcaster 实例模拟两个副本: 副本 B 的写事件必须到达副本 A 的订阅者,
// 且版本全局单调(跨副本发布交错也不重复)。
func TestCrossReplicaDelivery(t *testing.T) {
	mr := miniredis.RunT(t)
	replicaA := newTestBroadcaster(t, mr, Options{})
	replicaB := newTestBroadcaster(t, mr, Options{})

	ch, cancel := replicaA.Subscribe("alice")
	defer cancel()
	waitSubscribed(t, mr, replicaA, "alice", 1)

	replicaB.Publish("alice", ihtml.Event{Type: ihtml.EventPages})
	replicaA.Publish("alice", ihtml.Event{Type: ihtml.EventKV, Key: "k"})
	waitVersion(t, replicaA, "alice", 2)

	first := recvEvent(t, ch)
	second := recvEvent(t, ch)
	if first.Type != ihtml.EventResync || first.Reason != ihtml.ResyncExternal || first.Version != 1 {
		t.Fatalf("first event: %+v", first)
	}
	if second.Type != ihtml.EventResync || second.Reason != ihtml.ResyncExternal || second.Version != 2 {
		t.Fatalf("second event: %+v", second)
	}
}

func TestOverflowInjectsResync(t *testing.T) {
	mr := miniredis.RunT(t)
	b := newTestBroadcaster(t, mr, Options{SubscriberBuffer: 2})

	ch, cancel := b.Subscribe("alice")
	defer cancel()
	waitSubscribed(t, mr, b, "alice", 1)

	const total = 6
	for range total {
		b.Publish("alice", ihtml.Event{Type: ihtml.EventItems, Op: ihtml.EventOpPut})
	}
	// 等待泵吃完全部消息(版本推进到 total 且积压处理完), 再开始消费。
	waitVersion(t, b, "alice", total)
	time.Sleep(50 * time.Millisecond) // 泵是异步的, 给分发一点时间

	sawResync := false
	for done := false; !done; {
		select {
		case ev := <-ch:
			if ev.Type == ihtml.EventResync && ev.Reason == ihtml.ResyncOverflow {
				if ev.Version == 0 {
					t.Fatalf("overflow resync has no version: %+v", ev)
				}
				sawResync = true
			}
		default:
			done = true
		}
	}
	if !sawResync {
		t.Fatal("overflow must inject a resync event instead of silently dropping")
	}
}

func TestUserIsolationAndCancel(t *testing.T) {
	mr := miniredis.RunT(t)
	b := newTestBroadcaster(t, mr, Options{})

	chA, cancelA := b.Subscribe("alice")
	chB, cancelB := b.Subscribe("bob")
	waitSubscribed(t, mr, b, "alice", 1)
	waitSubscribed(t, mr, b, "bob", 1)

	b.Publish("bob", ihtml.Event{Type: ihtml.EventPages})
	waitVersion(t, b, "bob", 1)
	if ev := recvEvent(t, chB); ev.Type != ihtml.EventResync || ev.Reason != ihtml.ResyncExternal {
		t.Fatalf("bob event: %+v", ev)
	}
	select {
	case ev := <-chA:
		t.Fatalf("alice must not receive bob's event: %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}

	cancelA()
	cancelA() // 幂等
	if _, ok := <-chA; ok {
		t.Fatal("cancel must close the channel")
	}
	cancelB()

	// 最后一个订阅者退订后, Redis 频道应被退订。
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		counts, err := client.PubSubNumSub(context.Background(), b.channel("alice"), b.channel("bob")).Result()
		if err == nil && counts[b.channel("alice")] == 0 && counts[b.channel("bob")] == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("redis channels were not unsubscribed after the last local subscriber left")
}

func TestCloseClosesSubscribers(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	b, err := New(client, Options{})
	if err != nil {
		t.Fatal(err)
	}
	ch, _ := b.Subscribe("alice")
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := <-ch; ok {
		t.Fatal("Close must close subscriber channels")
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close must be idempotent: %v", err)
	}
	// 关闭后的订阅立即得到已关闭通道。
	closedCh, cancel := b.Subscribe("alice")
	cancel()
	if _, ok := <-closedCh; ok {
		t.Fatal("Subscribe after Close must return a closed channel")
	}
}

func TestSubscribeReturnsOnlyAfterRedisAcknowledgement(t *testing.T) {
	mr := miniredis.RunT(t)
	b := newTestBroadcaster(t, mr, Options{})

	ch, cancel := b.Subscribe("ack-user")
	defer cancel()
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	counts, err := client.PubSubNumSub(context.Background(), b.channel("ack-user")).Result()
	if err != nil {
		t.Fatal(err)
	}
	if counts[b.channel("ack-user")] != 1 {
		t.Fatalf("Subscribe returned before Redis acknowledgement: %v", counts)
	}
	b.Publish("ack-user", ihtml.Event{Type: ihtml.EventItems})
	if ev := recvEvent(t, ch); ev.Type != ihtml.EventResync {
		t.Fatalf("acknowledged subscription did not receive event: %+v", ev)
	}
}

func TestConcurrentSubscribersShareOneAcknowledgedRedisChannel(t *testing.T) {
	mr := miniredis.RunT(t)
	b := newTestBroadcaster(t, mr, Options{})
	const total = 32
	type subscription struct {
		ch     <-chan ihtml.Event
		cancel func()
	}
	results := make(chan subscription, total)
	var wg sync.WaitGroup
	for range total {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch, cancel := b.Subscribe("shared-user")
			results <- subscription{ch: ch, cancel: cancel}
		}()
	}
	wg.Wait()
	close(results)

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	counts, err := client.PubSubNumSub(context.Background(), b.channel("shared-user")).Result()
	if err != nil {
		t.Fatal(err)
	}
	if counts[b.channel("shared-user")] != 1 {
		t.Fatalf("concurrent subscribers created an unacknowledged/duplicate Redis subscription: %v", counts)
	}

	subs := make([]subscription, 0, total)
	for sub := range results {
		subs = append(subs, sub)
	}
	b.Publish("shared-user", ihtml.Event{Type: ihtml.EventItems})
	for i, sub := range subs {
		if ev := recvEvent(t, sub.ch); ev.Type != ihtml.EventResync {
			t.Fatalf("subscriber %d event: %+v", i, ev)
		}
	}
	for _, sub := range subs {
		sub.cancel()
	}
}

func TestSubscribeFailureReturnsClosedChannel(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
	defer client.Close()
	b, err := New(client, Options{OpTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	mr.Close()

	start := time.Now()
	ch, cancel := b.Subscribe("offline-user")
	defer cancel()
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("failed Subscribe exceeded bounded acknowledgement wait: %s", elapsed)
	}
	if _, ok := <-ch; ok {
		t.Fatal("failed Subscribe must return a closed channel")
	}
}

func TestNewRejectsNilClient(t *testing.T) {
	if _, err := New(nil, Options{}); err != ErrNilClient {
		t.Fatalf("New(nil) = %v, want ErrNilClient", err)
	}
}

func TestNewRejectsKeyPrefixWithClusterBraces(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	for _, prefix := range []string{"tenant{bad", "tenant}bad", "{tenant}"} {
		if _, err := New(client, Options{KeyPrefix: prefix}); !errors.Is(err, ErrInvalidKeyPrefix) {
			t.Fatalf("New with KeyPrefix %q = %v, want ErrInvalidKeyPrefix", prefix, err)
		}
	}
}

func TestRedisIdentifiersAreOpaqueAndClusterColocated(t *testing.T) {
	mr := miniredis.RunT(t)
	b := newTestBroadcaster(t, mr, Options{KeyPrefix: "tenant-a"})
	user := "alice@example.com/测试"
	key, channel := b.verKey(user), b.channel(user)
	if !strings.HasPrefix(key, "tenant-a:") || !strings.HasPrefix(channel, "tenant-a:") ||
		!strings.HasPrefix(b.globalChannel(), "tenant-a:") {
		t.Fatalf("KeyPrefix is not the actual Redis prefix: key=%q channel=%q global=%q", key, channel, b.globalChannel())
	}
	if strings.Contains(key, user) || strings.Contains(channel, user) || strings.Contains(key, "alice") || strings.Contains(channel, "alice") {
		t.Fatalf("Redis identifiers leaked raw user: key=%q channel=%q", key, channel)
	}
	keyEnd := strings.IndexByte(key, '}')
	channelEnd := strings.IndexByte(channel, '}')
	if keyEnd < 0 || channelEnd < 0 || key[:keyEnd+1] != channel[:channelEnd+1] {
		t.Fatalf("version key and channel do not share a cluster hash tag: %q / %q", key, channel)
	}
}

func TestLegacyNamespaceMigrationIsExplicit(t *testing.T) {
	mr := miniredis.RunT(t)
	b := newTestBroadcaster(t, mr, Options{KeyPrefix: "privacy"})
	user := "alice@example.com"

	b.Publish(user, ihtml.Event{Type: ihtml.EventItems})
	waitVersion(t, b, user, 1)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	exists, err := client.Exists(context.Background(), b.legacyVerKey(user)).Result()
	if err != nil {
		t.Fatal(err)
	}
	if exists != 0 {
		t.Fatalf("default mode wrote raw-user legacy key %q", b.legacyVerKey(user))
	}
}

func TestLegacyNamespaceMigrationPublishesToOldSubscriber(t *testing.T) {
	mr := miniredis.RunT(t)
	b := newTestBroadcaster(t, mr, Options{KeyPrefix: "rolling", LegacyNamespaceMigration: true})
	user := "legacy-reader@example.com"
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	oldSub := client.Subscribe(context.Background(), b.legacyChannel(user))
	defer oldSub.Close()
	if _, err := oldSub.Receive(context.Background()); err != nil {
		t.Fatal(err)
	}

	b.Publish(user, ihtml.Event{
		Type: ihtml.EventItems, Op: ihtml.EventOpPut,
		Items: []ihtml.Item{{ID: "must-not-cross", Type: ihtml.ItemHTML}},
	})
	receiveCtx, receiveCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer receiveCancel()
	msg, err := oldSub.ReceiveMessage(receiveCtx)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal([]byte(msg.Payload), &env); err != nil {
		t.Fatal(err)
	}
	var ev ihtml.Event
	if err := json.Unmarshal(env.E, &ev); err != nil {
		t.Fatal(err)
	}
	if env.V != 1 || ev.Type != ihtml.EventResync || ev.Reason != ihtml.ResyncExternal ||
		ev.Op != "" || len(ev.Items) != 0 {
		t.Fatalf("migration publish was not old-wire-compatible authoritative resync: env=%+v ev=%+v", env, ev)
	}
	legacyVersion, err := client.Get(context.Background(), b.legacyVerKey(user)).Uint64()
	if err != nil || legacyVersion != 1 {
		t.Fatalf("legacy version = %d, err=%v; want 1", legacyVersion, err)
	}
	waitVersion(t, b, user, 1) // 当前 SHA namespace 同样必须推进。
}

func TestLegacyNamespaceMigrationConsumesOldIncrementAsUnversionedResync(t *testing.T) {
	mr := miniredis.RunT(t)
	b := newTestBroadcaster(t, mr, Options{KeyPrefix: "rolling", LegacyNamespaceMigration: true})
	user := "legacy-writer@example.com"
	ch, cancel := b.Subscribe(user)
	defer cancel()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	counts, err := client.PubSubNumSub(context.Background(), b.channel(user), b.legacyChannel(user)).Result()
	if err != nil {
		t.Fatal(err)
	}
	if counts[b.channel(user)] != 1 || counts[b.legacyChannel(user)] != 1 {
		t.Fatalf("migration Subscribe returned before both namespace acknowledgements: %v", counts)
	}
	legacyIncrement, err := json.Marshal(ihtml.Event{
		Type: ihtml.EventItems, Op: ihtml.EventOpPut,
		Items: []ihtml.Item{{ID: "stale", Type: ihtml.ItemHTML, Content: "old payload"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 精确模拟旧版本 broadcaster 的双 KEYS Lua wire format。
	if err := publishScript.Run(context.Background(), client,
		[]string{b.legacyVerKey(user), b.legacyChannel(user)}, string(legacyIncrement)).Err(); err != nil {
		t.Fatal(err)
	}

	ev := recvEvent(t, ch)
	if ev.Type != ihtml.EventResync || ev.Reason != ihtml.ResyncExternal || ev.Version != 0 ||
		ev.Op != "" || len(ev.Items) != 0 || len(ev.IDs) != 0 {
		t.Fatalf("legacy incremental payload was not stripped before local delivery: %+v", ev)
	}
	if current := b.CurrentVersion(user); current != 0 {
		t.Fatalf("legacy version polluted current namespace baseline: %d", current)
	}
}

func TestClusterClientPublishesWithColocatedLuaKeys(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs: []string{mr.Addr()},
		ClusterSlots: func(context.Context) ([]redis.ClusterSlot, error) {
			return []redis.ClusterSlot{{
				Start: 0, End: 16383,
				Nodes: []redis.ClusterNode{{Addr: mr.Addr()}},
			}}, nil
		},
	})
	defer client.Close()
	b, err := New(client, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	ch, cancel := b.Subscribe("cluster-user")
	defer cancel()
	waitSubscribed(t, mr, b, "cluster-user", 1)
	b.Publish("cluster-user", ihtml.Event{Type: ihtml.EventItems})
	waitVersion(t, b, "cluster-user", 1)
	if ev := recvEvent(t, ch); ev.Type != ihtml.EventResync || ev.Version != 1 {
		t.Fatalf("cluster delivery: %+v", ev)
	}
}

func TestClusterClientLegacyMigrationAvoidsCrossSlot(t *testing.T) {
	mr := miniredis.RunT(t)
	clusterClient := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs: []string{mr.Addr()},
		ClusterSlots: func(context.Context) ([]redis.ClusterSlot, error) {
			return []redis.ClusterSlot{{
				Start: 0, End: 16383,
				Nodes: []redis.ClusterNode{{Addr: mr.Addr()}},
			}}, nil
		},
	})
	defer clusterClient.Close()
	b, err := New(clusterClient, Options{LegacyNamespaceMigration: true})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	user := "cluster-legacy@example.com"
	oldClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer oldClient.Close()
	oldSub := oldClient.Subscribe(context.Background(), b.legacyChannel(user))
	defer oldSub.Close()
	if _, err := oldSub.Receive(context.Background()); err != nil {
		t.Fatal(err)
	}
	b.Publish(user, ihtml.Event{Type: ihtml.EventItems})
	receiveCtx, receiveCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer receiveCancel()
	msg, err := oldSub.ReceiveMessage(receiveCtx)
	if err != nil {
		t.Fatalf("legacy bridge publish failed through ClusterClient: %v", err)
	}
	var env envelope
	if err := json.Unmarshal([]byte(msg.Payload), &env); err != nil {
		t.Fatal(err)
	}
	if env.V != 1 {
		t.Fatalf("legacy Cluster migration version = %d, want 1", env.V)
	}
}

func TestPublishDoesNotWaitForRedis(t *testing.T) {
	// 使用必然拒绝连接的本地端口；后台工作协程可以等待 OpTimeout，
	// 但写路径 Publish 只允许做非阻塞入队。
	client := redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:1", DialTimeout: 500 * time.Millisecond,
		ReadTimeout: 500 * time.Millisecond, WriteTimeout: 500 * time.Millisecond,
		MaxRetries: -1,
	})
	defer client.Close()
	b, err := New(client, Options{PublishBuffer: 1, OpTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	for i := 0; i < 1000; i++ {
		b.Publish("offline", ihtml.Event{Type: ihtml.EventItems})
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("Publish waited for Redis: %s", elapsed)
	}
	closeStart := time.Now()
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(closeStart); elapsed > 1200*time.Millisecond {
		t.Fatalf("Close did not cancel background Redis work promptly: %s", elapsed)
	}
}

type failEvalOnceHook struct {
	failed atomic.Bool
}

type failKeyedEvalOnceHook struct {
	key    string
	failed atomic.Bool
}

func (*failKeyedEvalOnceHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (h *failKeyedEvalOnceHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if cmd.Name() == "evalsha" || cmd.Name() == "eval" {
			for _, arg := range cmd.Args() {
				if value, ok := arg.(string); ok && value == h.key && h.failed.CompareAndSwap(false, true) {
					return errors.New("injected one-shot keyed script failure")
				}
			}
		}
		return next(ctx, cmd)
	}
}

func (*failKeyedEvalOnceHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func (*failEvalOnceHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (h *failEvalOnceHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if (cmd.Name() == "evalsha" || cmd.Name() == "eval") && h.failed.CompareAndSwap(false, true) {
			return errors.New("injected one-shot script failure")
		}
		return next(ctx, cmd)
	}
}

func (*failEvalOnceHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func TestFailedAcceptedPublishSignalsOverflowAfterRecovery(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
	client.AddHook(&failEvalOnceHook{})
	defer client.Close()
	b, err := New(client, Options{OpTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	ch, cancel := b.Subscribe("recover-user")
	defer cancel()
	b.Publish("recover-user", ihtml.Event{Type: ihtml.EventItems, Op: ihtml.EventOpPut})

	// 第一次用户 Lua 发布被 hook 注入失败；后台必须留下有界 dirty 位，
	// 并在下一次正常 Redis 操作时主动发出全局 overflow resync。
	ev := recvEvent(t, ch)
	if ev.Type != ihtml.EventResync || ev.Reason != ihtml.ResyncOverflow || ev.Version != 0 {
		t.Fatalf("failed accepted publish did not converge through overflow resync: %+v", ev)
	}
	if version := b.CurrentVersion("recover-user"); version != 0 {
		t.Fatalf("failed user script unexpectedly advanced version to %d", version)
	}
	if len(b.overflowQ) != 0 {
		t.Fatalf("recovered overflow dirty bit was not consumed: %d", len(b.overflowQ))
	}
}

func TestLegacyPartialPublishFailureRetriesOldNamespace(t *testing.T) {
	mr := miniredis.RunT(t)
	const prefix = "partial-retry"
	const user = "old-reader@example.com"
	hook := &failKeyedEvalOnceHook{key: prefix + ":ver:" + user}
	client := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
	client.AddHook(hook)
	defer client.Close()
	b, err := New(client, Options{
		KeyPrefix: prefix, LegacyNamespaceMigration: true,
		PublishBuffer: 4, OpTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	oldClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer oldClient.Close()
	oldSub := oldClient.Subscribe(context.Background(), b.legacyChannel(user))
	defer oldSub.Close()
	if _, err := oldSub.Receive(context.Background()); err != nil {
		t.Fatal(err)
	}
	b.Publish(user, ihtml.Event{Type: ihtml.EventItems})

	receiveCtx, receiveCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer receiveCancel()
	msg, err := oldSub.ReceiveMessage(receiveCtx)
	if err != nil {
		t.Fatalf("legacy namespace did not recover from partial dual-publish failure: %v", err)
	}
	if !hook.failed.Load() {
		t.Fatal("test hook never exercised legacy publish failure")
	}
	var env envelope
	if err := json.Unmarshal([]byte(msg.Payload), &env); err != nil {
		t.Fatal(err)
	}
	if env.V != 1 {
		t.Fatalf("retried legacy version = %d, want 1", env.V)
	}
	waitVersion(t, b, user, 1)
	if got := len(b.legacyQ); got != 0 {
		t.Fatalf("successful legacy retry left %d queued entries", got)
	}
}

func TestCloseDrainsLegacyRetryAfterPartialPublishFailure(t *testing.T) {
	mr := miniredis.RunT(t)
	const prefix = "partial-close"
	const user = "old-reader@example.com"
	hook := &failKeyedEvalOnceHook{key: prefix + ":ver:" + user}
	client := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
	client.AddHook(hook)
	defer client.Close()
	b, err := New(client, Options{
		KeyPrefix: prefix, LegacyNamespaceMigration: true,
		PublishBuffer: 4, OpTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	oldClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer oldClient.Close()
	oldSub := oldClient.Subscribe(context.Background(), b.legacyChannel(user))
	defer oldSub.Close()
	if _, err := oldSub.Receive(context.Background()); err != nil {
		t.Fatal(err)
	}
	b.Publish(user, ihtml.Event{Type: ihtml.EventItems})
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if !hook.failed.Load() {
		t.Fatal("test hook never exercised legacy publish failure")
	}
	receiveCtx, receiveCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer receiveCancel()
	msg, err := oldSub.ReceiveMessage(receiveCtx)
	if err != nil {
		t.Fatalf("Close returned before draining the legacy retry: %v", err)
	}
	var env envelope
	if err := json.Unmarshal([]byte(msg.Payload), &env); err != nil {
		t.Fatal(err)
	}
	if env.V != 1 {
		t.Fatalf("Close-drained legacy version = %d, want 1", env.V)
	}
}

func TestCloseDrainsAcceptedPublishesOnHealthyRedis(t *testing.T) {
	mr := miniredis.RunT(t)
	subscriber := newTestBroadcaster(t, mr, Options{SubscriberBuffer: 64})
	publisher := newTestBroadcaster(t, mr, Options{PublishBuffer: 64, OpTimeout: time.Second})

	ch, cancel := subscriber.Subscribe("drain-user")
	defer cancel()
	const total = 32
	for range total {
		publisher.Publish("drain-user", ihtml.Event{Type: ihtml.EventItems})
	}
	if err := publisher.Close(); err != nil {
		t.Fatal(err)
	}
	waitVersion(t, subscriber, "drain-user", total)
	for want := uint64(1); want <= total; want++ {
		ev := recvEvent(t, ch)
		if ev.Version != want || ev.Type != ihtml.EventResync {
			t.Fatalf("drained event %d: %+v", want, ev)
		}
	}
}

func TestConcurrentPublishAndClose(t *testing.T) {
	mr := miniredis.RunT(t)
	b := newTestBroadcaster(t, mr, Options{PublishBuffer: 8, OpTimeout: time.Second})
	start := make(chan struct{})
	var wg sync.WaitGroup
	for writer := 0; writer < 8; writer++ {
		wg.Add(1)
		go func(writer int) {
			defer wg.Done()
			<-start
			for i := 0; i < 100; i++ {
				b.Publish("close-race", ihtml.Event{Type: ihtml.EventItems, By: string(rune('a' + writer))})
			}
		}(writer)
	}
	close(start)
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	// Publish after Close is explicitly a no-op and must never panic or enqueue.
	b.Publish("close-race", ihtml.Event{Type: ihtml.EventItems})
}

func TestPublishQueueOverflowEmitsGlobalResync(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	opts := Options{PublishBuffer: 1, OpTimeout: time.Second}
	opts.fill()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 只启动发布 actor，便可在启动前确定性地填满有界队列。
	b := &Broadcaster{
		client: client, opts: opts, ctx: ctx, cancel: cancel,
		publishQ: make(chan publishRequest, 1), overflowQ: make(chan struct{}, 1),
		drainCh: make(chan struct{}), drained: make(chan struct{}),
		subs: make(map[string]map[chan ihtml.Event]struct{}), channels: make(map[string]channelBinding),
		subscriptions: make(map[string]*subscriptionState),
	}
	b.publishQ <- publishRequest{
		token: b.subjectToken("queued"),
		event: ihtml.Event{Type: ihtml.EventResync, Reason: ihtml.ResyncExternal},
	}
	b.Publish("overflowed", ihtml.Event{Type: ihtml.EventItems})
	if len(b.publishQ) != 1 || len(b.overflowQ) != 1 {
		t.Fatalf("overflow was not bounded/coalesced: publish=%d overflow=%d", len(b.publishQ), len(b.overflowQ))
	}

	pubsub := client.Subscribe(ctx, b.globalChannel())
	defer pubsub.Close()
	if _, err := pubsub.Receive(ctx); err != nil { // 等待 Redis 确认全局频道订阅。
		t.Fatal(err)
	}
	b.wg.Add(1)
	go b.publishLoop()

	receiveCtx, receiveCancel := context.WithTimeout(ctx, 3*time.Second)
	defer receiveCancel()
	msg, err := pubsub.ReceiveMessage(receiveCtx)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal([]byte(msg.Payload), &env); err != nil {
		t.Fatal(err)
	}
	var ev ihtml.Event
	if err := json.Unmarshal(env.E, &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Type != ihtml.EventResync || ev.Reason != ihtml.ResyncOverflow {
		t.Fatalf("queue overflow did not emit explicit global resync: %+v", ev)
	}
	cancel()
	b.wg.Wait()
}

// 端到端: 两个 Handler(共享 Store)各接自己的 redisbroadcast 实例,
// 副本 A 的写入事件必须到达连接在副本 B 上的订阅者。
func TestHandlersAcrossReplicasShareEvents(t *testing.T) {
	mr := miniredis.RunT(t)
	replicaA := newTestBroadcaster(t, mr, Options{})
	replicaB := newTestBroadcaster(t, mr, Options{})

	store := ihtml.NewMemoryStore()
	handlerA := ihtml.New(store, ihtml.WithBroadcaster(replicaA))
	defer handlerA.Close()
	handlerB := ihtml.New(store, ihtml.WithBroadcaster(replicaB))
	defer handlerB.Close()

	ch, cancel := replicaB.Subscribe("alice")
	defer cancel()
	waitSubscribed(t, mr, replicaB, "alice", 1)

	item := ihtml.Item{ID: "card", Type: ihtml.ItemHTML, Content: "<div/>"}
	if err := handlerA.PutItems(context.Background(), "alice", []ihtml.Item{item}, "api", ""); err != nil {
		t.Fatal(err)
	}
	ev := recvEvent(t, ch)
	if ev.Type != ihtml.EventResync || ev.Reason != ihtml.ResyncExternal || ev.Version == 0 ||
		ev.Op != "" || len(ev.Items) != 0 {
		t.Fatalf("replica B must observe replica A's write: %+v", ev)
	}
	items, err := handlerB.ListItems(context.Background(), "alice")
	if err != nil || len(items) != 1 {
		t.Fatalf("replica B must read replica A's data: %v %v", items, err)
	}
}
