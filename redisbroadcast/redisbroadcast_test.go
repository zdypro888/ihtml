package redisbroadcast

import (
	"context"
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

func TestPublishAssignsMonotonicVersions(t *testing.T) {
	mr := miniredis.RunT(t)
	b := newTestBroadcaster(t, mr, Options{})

	ch, cancel := b.Subscribe("alice")
	defer cancel()
	waitSubscribed(t, mr, b, "alice", 1)

	for range 3 {
		b.Publish("alice", ihtml.Event{Type: ihtml.EventItems, Op: ihtml.EventOpPut})
	}
	for want := uint64(1); want <= 3; want++ {
		ev := recvEvent(t, ch)
		if ev.Version != want || ev.Type != ihtml.EventItems {
			t.Fatalf("event %d: got version=%d type=%q", want, ev.Version, ev.Type)
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

	first := recvEvent(t, ch)
	second := recvEvent(t, ch)
	if first.Type != ihtml.EventPages || first.Version != 1 {
		t.Fatalf("first event: %+v", first)
	}
	if second.Type != ihtml.EventKV || second.Version != 2 || second.Key != "k" {
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
	deadline := time.Now().Add(3 * time.Second)
	for b.CurrentVersion("alice") < total && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // 泵是异步的, 给分发一点时间

	sawResync := false
	for done := false; !done; {
		select {
		case ev := <-ch:
			if ev.Type == ihtml.EventResync {
				if ev.Reason != ihtml.ResyncOverflow || ev.Version == 0 {
					t.Fatalf("bad resync event: %+v", ev)
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
	if ev := recvEvent(t, chB); ev.Type != ihtml.EventPages {
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

func TestNewRejectsNilClient(t *testing.T) {
	if _, err := New(nil, Options{}); err != ErrNilClient {
		t.Fatalf("New(nil) = %v, want ErrNilClient", err)
	}
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
	if ev.Type != ihtml.EventItems || ev.Op != ihtml.EventOpPut || ev.Version == 0 ||
		len(ev.Items) != 1 || ev.Items[0].ID != "card" {
		t.Fatalf("replica B must observe replica A's write: %+v", ev)
	}
	items, err := handlerB.ListItems(context.Background(), "alice")
	if err != nil || len(items) != 1 {
		t.Fatalf("replica B must read replica A's data: %v %v", items, err)
	}
}
