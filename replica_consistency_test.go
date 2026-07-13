package ihtml

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

// 两个 Handler 共享同一个 Store, 模拟"没有 sticky 路由的两个副本":
// 进程内 userLock 不再提供互斥, 正确性只能依赖 Store 条件写 + 重试。
func TestCrossReplicaConcurrentItemWritesDoNotLoseUpdates(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	replicaA, replicaB := New(store), New(store)
	defer replicaA.Close()
	defer replicaB.Close()

	const perReplica = 30
	var wg sync.WaitGroup
	errs := make(chan error, perReplica*2)
	for i := range perReplica {
		wg.Add(2)
		go func() {
			defer wg.Done()
			item := Item{ID: fmt.Sprintf("a-%02d", i), Type: ItemHTML, Content: "<div>a</div>", Order: i}
			if err := replicaA.PutItems(ctx, "u", []Item{item}, "api", "replica-a"); err != nil {
				errs <- err
			}
		}()
		go func() {
			defer wg.Done()
			item := Item{ID: fmt.Sprintf("b-%02d", i), Type: ItemHTML, Content: "<div>b</div>", Order: i}
			if err := replicaB.PutItems(ctx, "u", []Item{item}, "api", "replica-b"); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent put failed: %v", err)
	}
	items, err := replicaA.ListItems(ctx, "u")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != perReplica*2 {
		t.Fatalf("lost updates across replicas: want %d items, got %d", perReplica*2, len(items))
	}
}

// 模拟跨副本的 Template TOCTOU: 副本 B 基于旧状态构造条件写, 必须被拒绝,
// 重试后旧 expected_revision 要转化为 TemplateConflictError 而不是静默覆盖。
func TestCrossReplicaTemplateCASRejectsStaleRevision(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	replicaA, replicaB := New(store), New(store)
	defer replicaA.Close()
	defer replicaB.Close()

	tpl := Template{
		ID: "tpl", Name: "布局",
		Draft: TemplateVersion{
			RootID: "root", Nodes: []TemplateNode{{ID: "root", Kind: NodeCanvas}},
			Slots: []Slot{}, Placements: []Placement{}, Breakpoints: []Breakpoint{},
		},
	}
	created, err := replicaA.PutTemplate(ctx, "u", tpl, 0, "api")
	if err != nil {
		t.Fatal(err)
	}

	// 两个副本都读到同一 revision。
	staleRevision := created.Revision

	// 副本 A 先完成一次更新。
	update := created
	update.Name = "布局 A"
	if _, err := replicaA.PutTemplate(ctx, "u", update, staleRevision, "api"); err != nil {
		t.Fatal(err)
	}

	// 副本 B 仍拿旧 revision 写入: 必须得到模板冲突, 且冲突体带最新模板。
	update.Name = "布局 B"
	_, err = replicaB.PutTemplate(ctx, "u", update, staleRevision, "api")
	var conflict *TemplateConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("stale template write must conflict, got err=%v", err)
	}
	if conflict.Current == nil || conflict.Current.Name != "布局 A" {
		t.Fatalf("conflict must carry the latest template: %+v", conflict)
	}

	final, err := replicaB.GetTemplate(ctx, "u", "tpl")
	if err != nil || final.Name != "布局 A" {
		t.Fatalf("replica B must observe A's write intact: %+v err=%v", final, err)
	}
}

// 并发页面注册与 Item 写交错: 页面注册表(KV)与 Item 集合都不允许丢失。
func TestCrossReplicaMixedWritesConverge(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	replicaA, replicaB := New(store), New(store)
	defer replicaA.Close()
	defer replicaB.Close()

	const rounds = 20
	var wg sync.WaitGroup
	errs := make(chan error, rounds*2)
	for i := range rounds {
		wg.Add(2)
		go func() {
			defer wg.Done()
			page := Page{Name: fmt.Sprintf("p%02d", i), Title: fmt.Sprintf("页 %d", i), Order: i}
			if err := replicaA.SetPage(ctx, "u", page); err != nil {
				errs <- err
			}
		}()
		go func() {
			defer wg.Done()
			item := Item{ID: fmt.Sprintf("it-%02d", i), Type: ItemCSS, Content: ".x{}", Order: i}
			if err := replicaB.PutItems(ctx, "u", []Item{item}, "api", ""); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent mixed write failed: %v", err)
	}
	pages, err := replicaA.ListPages(ctx, "u")
	if err != nil {
		t.Fatal(err)
	}
	items, err := replicaB.ListItems(ctx, "u")
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != rounds || len(items) != rounds {
		t.Fatalf("mixed writes lost state: pages=%d items=%d want %d/%d", len(pages), len(items), rounds, rounds)
	}
}

// Store 级条件写语义: 版本单调、过期 expected 拒绝、EraseUser 归零。
func TestVersionedStoreSemantics(t *testing.T) {
	ctx := context.Background()
	for name, factory := range map[string]func(t *testing.T) VersionedUserStateStore{
		"memory": func(t *testing.T) VersionedUserStateStore { return NewMemoryStore() },
		"file": func(t *testing.T) VersionedUserStateStore {
			fs, err := NewFileStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			return fs
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := factory(t)
			state, err := store.UserState(ctx, "u")
			if err != nil || state.Version != 0 {
				t.Fatalf("fresh user version: %d err=%v", state.Version, err)
			}

			// 模拟两个副本基于同一版本写入: 第二个必须冲突。
			state.Items = []Item{{ID: "a", Type: ItemHTML, Content: "<a/>"}}
			if err := store.ReplaceUserStateVersioned(ctx, "u", state, 0); err != nil {
				t.Fatal(err)
			}
			state.Items = []Item{{ID: "b", Type: ItemHTML, Content: "<b/>"}}
			if err := store.ReplaceUserStateVersioned(ctx, "u", state, 0); !errors.Is(err, ErrVersionConflict) {
				t.Fatalf("stale versioned replace must conflict, got %v", err)
			}
			after, err := store.UserState(ctx, "u")
			if err != nil || after.Version != 1 || len(after.Items) != 1 || after.Items[0].ID != "a" {
				t.Fatalf("conflicting write must not change state: %+v err=%v", after, err)
			}

			// 单方法写入也要推进版本, 否则整状态条件写检测不到交错的 KV/Item 写。
			if err := store.(Store).KVSet(ctx, "u", "k", []byte(`1`)); err != nil {
				t.Fatal(err)
			}
			bumped, err := store.UserState(ctx, "u")
			if err != nil || bumped.Version != 2 {
				t.Fatalf("kv write must bump version: %d err=%v", bumped.Version, err)
			}
			if err := store.ReplaceUserStateVersioned(ctx, "u", after, after.Version); !errors.Is(err, ErrVersionConflict) {
				t.Fatalf("replace after interleaved kv write must conflict, got %v", err)
			}

			// EraseUser 归零, expected=0 重新可用。
			if err := store.(Store).EraseUser(ctx, "u"); err != nil {
				t.Fatal(err)
			}
			erased, err := store.UserState(ctx, "u")
			if err != nil || erased.Version != 0 {
				t.Fatalf("erase must reset version: %d err=%v", erased.Version, err)
			}
			erased.Items = []Item{{ID: "c", Type: ItemHTML, Content: "<c/>"}}
			if err := store.ReplaceUserStateVersioned(ctx, "u", erased, 0); err != nil {
				t.Fatal(err)
			}
		})
	}
}
