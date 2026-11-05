package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/zdypro888/ihtml/agent"
)

// OutboxFixture 的 Append 只准备隔离测试事实；真实事务原子性需由适配器另行集成验收。
type OutboxFixture struct {
	Store           agent.TaskOutboxStore
	Append          func(context.Context, agent.TaskOutboxEntry) error
	Publisher       agent.TaskEventPublisher
	Scope, SourceID string
}

// RunOutbox 可供任意持久提供者复用，验证租约/修订隔离、重投不改事实和 ACK 去重。
func RunOutbox(t *testing.T, factory func(*testing.T) OutboxFixture) {
	t.Helper()
	t.Run("OutboxOwnershipAndReplay", func(t *testing.T) {
		f := factory(t)
		ctx := t.Context()
		entry, err := agent.NewTaskOutboxEntry(f.Scope, f.SourceID, "stable-key", "fact", json.RawMessage(`{"private":"body"}`), time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if err = f.Append(ctx, entry); err != nil {
			t.Fatal(err)
		}
		claimed, ok, err := f.Store.ClaimOutbox(ctx, "worker", time.Minute)
		if err != nil || !ok {
			t.Fatal(ok, err)
		}
		if _, ok, err = f.Store.ClaimOutbox(ctx, "other", time.Minute); err != nil || ok {
			t.Fatal("duplicate lease", ok, err)
		}
		wrong := claimed
		wrong.Owner = "other"
		if err = f.Store.AckOutbox(ctx, wrong); !errors.Is(err, agent.ErrTaskConflict) {
			t.Fatal("wrong owner acknowledged", err)
		}
		if err = f.Store.RetryOutbox(ctx, claimed, time.Time{}); err != nil {
			t.Fatal(err)
		}
		again, ok, err := f.Store.ClaimOutbox(ctx, "other", time.Minute)
		if err != nil || !ok || again.ID != claimed.ID || again.Revision <= claimed.Revision || string(again.Event.Payload) != string(claimed.Event.Payload) {
			t.Fatal(again, ok, err)
		}
		if err = f.Store.AckOutbox(ctx, claimed); !errors.Is(err, agent.ErrTaskConflict) {
			t.Fatal("old revision acknowledged", err)
		}
		// 发布应答丢失时重发同一键，不改变序号或再产生一次事实。
		first, err := f.Publisher.Publish(ctx, again.Scope, again.SourceID, again.Event)
		if err != nil {
			t.Fatal(err)
		}
		if err = f.Store.RetryOutbox(ctx, again, time.Time{}); err != nil {
			t.Fatal(err)
		}
		if ok, err = agent.DeliverTaskOutbox(ctx, f.Store, f.Publisher, "retry"); err != nil || !ok {
			t.Fatal(ok, err)
		}
		source, err := f.Publisher.Store.GetEventSource(ctx, f.Scope, f.SourceID)
		if err != nil || source.Head != first.Seq {
			t.Fatal("ACK loss duplicated fact", source.Head, err)
		}
		if ok, err = agent.DeliverTaskOutbox(ctx, f.Store, f.Publisher, "retry"); err != nil || ok {
			t.Fatal(ok, err)
		}
	})
}
