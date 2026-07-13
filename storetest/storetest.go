// Package storetest 是 ihtml.Store 实现的官方一致性验收套件。
// 第三方 Store(数据库、对象存储等)在接入生产前应通过 Run:
//
//	func TestMyStore(t *testing.T) {
//	    storetest.Run(t, func(t *testing.T) ihtml.Store {
//	        return newMyStore(t) // 每次调用返回一个全新的空 Store
//	    })
//	}
//
// 套件覆盖基础 Store 契约(Items/KV/Revisions/用户隔离/值快照所有权)，并在
// 实现了可选能力接口(UserStateStore / VersionedUserStateStore / RevisionStore)
// 时一并验收对应语义。内置 MemoryStore、FileStore 与 sqlstore 均由本套件验证。
package storetest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/zdypro888/ihtml"
)

// Run 用 factory 生产的全新 Store 跑完整一致性套件。factory 每次调用都必须
// 返回一个空的、相互独立的 Store；套件会并行使用多个实例。
func Run(t *testing.T, factory func(t *testing.T) ihtml.Store) {
	t.Helper()
	t.Run("items", func(t *testing.T) { testItems(t, factory(t)) })
	t.Run("kv", func(t *testing.T) { testKV(t, factory(t)) })
	t.Run("revisions", func(t *testing.T) { testRevisions(t, factory(t)) })
	t.Run("isolation", func(t *testing.T) { testIsolation(t, factory(t)) })
	t.Run("ownership", func(t *testing.T) { testOwnership(t, factory(t)) })
	t.Run("erase", func(t *testing.T) { testErase(t, factory(t)) })

	if _, ok := factory(t).(ihtml.UserStateStore); ok {
		t.Run("user_state", func(t *testing.T) {
			testUserState(t, factory(t).(ihtml.UserStateStore))
		})
	}
	if _, ok := factory(t).(ihtml.RevisionStore); ok {
		t.Run("full_revision", func(t *testing.T) {
			testFullRevision(t, factory(t))
		})
	}
	if _, ok := factory(t).(ihtml.VersionedUserStateStore); ok {
		t.Run("versioning", func(t *testing.T) {
			testVersioning(t, factory(t).(ihtml.VersionedUserStateStore))
		})
	}
}

// user 故意带特殊字符与大小写差异敏感的形态，检验实现按完整字节隔离用户。
const user = "user@测试/1"

func seedItems() []ihtml.Item {
	return []ihtml.Item{
		{ID: "b", Type: ihtml.ItemJS, Order: 2, Content: "console.log(1)", UpdatedAt: time.Unix(1700000002, 0).UTC()},
		{ID: "a", Type: ihtml.ItemHTML, Order: 1, Content: "<div/>", Title: "甲",
			Page: "dash", Meta: map[string]string{"slot": "header"}, UpdatedAt: time.Unix(1700000001, 0).UTC()},
	}
}

func testItems(t *testing.T, s ihtml.Store) {
	t.Helper()
	ctx := context.Background()
	items, err := s.Items(ctx, user)
	if err != nil || len(items) != 0 {
		t.Fatalf("fresh user must have zero items: %v %v", items, err)
	}
	if err := s.ReplaceItems(ctx, user, seedItems()); err != nil {
		t.Fatal(err)
	}
	items, err = s.Items(ctx, user)
	if err != nil || len(items) != 2 {
		t.Fatalf("items after replace: %v %v", items, err)
	}
	for _, it := range items {
		if it.ID == "a" {
			if it.Meta["slot"] != "header" || it.Page != "dash" || it.Title != "甲" || it.Type != ihtml.ItemHTML {
				t.Fatalf("item roundtrip lost fields: %+v", it)
			}
			if !it.UpdatedAt.Equal(time.Unix(1700000001, 0).UTC()) {
				t.Fatalf("item timestamp not preserved: %v", it.UpdatedAt)
			}
		}
	}
	// 整体替换是全集语义: 旧集合不残留。
	if err := s.ReplaceItems(ctx, user, []ihtml.Item{{ID: "c", Type: ihtml.ItemCSS, Content: ".c{}", UpdatedAt: time.Unix(1700000003, 0).UTC()}}); err != nil {
		t.Fatal(err)
	}
	items, err = s.Items(ctx, user)
	if err != nil || len(items) != 1 || items[0].ID != "c" {
		t.Fatalf("replace must be total: %v %v", items, err)
	}
	if err := s.ReplaceItems(ctx, user, nil); err != nil {
		t.Fatal(err)
	}
	items, err = s.Items(ctx, user)
	if err != nil || len(items) != 0 {
		t.Fatalf("replace with empty must clear: %v %v", items, err)
	}
}

func testKV(t *testing.T, s ihtml.Store) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.KVGet(ctx, user, "missing"); !errors.Is(err, ihtml.ErrNotFound) {
		t.Fatalf("missing kv must be ErrNotFound, got %v", err)
	}
	if err := s.KVSet(ctx, user, "theme", json.RawMessage(`{"mode":"dark"}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.KVSet(ctx, user, "theme", json.RawMessage(`{"mode":"light"}`)); err != nil {
		t.Fatal(err) // 覆盖写
	}
	v, err := s.KVGet(ctx, user, "theme")
	if err != nil || string(v) != `{"mode":"light"}` {
		t.Fatalf("kv get must preserve exact bytes: %s %v", v, err)
	}
	if err := s.KVSet(ctx, user, "other", json.RawMessage(`1`)); err != nil {
		t.Fatal(err)
	}
	exact := json.RawMessage(" { \"html\" : \"<script>&\" , \"values\" : [ 1, 2 ] } \n")
	if err := s.KVSet(ctx, user, "exact", exact); err != nil {
		t.Fatal(err)
	}
	exactRead, err := s.KVGet(ctx, user, "exact")
	if err != nil || !bytes.Equal(exactRead, exact) {
		t.Fatalf("kv get must preserve whitespace and HTML bytes exactly: %q %v", exactRead, err)
	}
	keys, err := s.KVKeys(ctx, user)
	if err != nil || len(keys) != 3 {
		t.Fatalf("kv keys: %v %v", keys, err)
	}
	if err := s.KVDelete(ctx, user, "theme"); err != nil {
		t.Fatal(err)
	}
	if err := s.KVDelete(ctx, user, "theme"); err != nil {
		t.Fatalf("deleting a missing key must not error: %v", err)
	}
	if _, err := s.KVGet(ctx, user, "theme"); !errors.Is(err, ihtml.ErrNotFound) {
		t.Fatalf("deleted kv must be ErrNotFound, got %v", err)
	}
}

func revisionAt(i int) ihtml.Revision {
	return ihtml.Revision{
		Version:   ihtml.CurrentDataVersion,
		ID:        fmt.Sprintf("r-%03d", i),
		Note:      "n",
		By:        "api",
		CreatedAt: time.Unix(1700000000+int64(i), 0).UTC(),
		Items:     seedItems(),
		Pages:     []ihtml.Page{{Name: "dash", Title: "Dashboard"}},
	}
}

func testRevisions(t *testing.T, s ihtml.Store) {
	t.Helper()
	ctx := context.Background()
	for i := range 5 {
		if err := s.AddRevision(ctx, user, revisionAt(i), 3); err != nil {
			t.Fatal(err)
		}
	}
	revs, err := s.Revisions(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	if len(revs) != 3 {
		t.Fatalf("revisions must be pruned to keep=3, got %d", len(revs))
	}
	if revs[0].ID != "r-004" || revs[2].ID != "r-002" {
		t.Fatalf("revisions must be newest-first and prune the oldest: %+v", revs)
	}
	got, err := s.RevisionItems(ctx, user, revs[0].ID)
	if err != nil || len(got) != 2 {
		t.Fatalf("revision items: %v %v", got, err)
	}
	if _, err := s.RevisionItems(ctx, user, "nope"); !errors.Is(err, ihtml.ErrNotFound) {
		t.Fatalf("missing revision must be ErrNotFound, got %v", err)
	}
}

func testIsolation(t *testing.T, s ihtml.Store) {
	t.Helper()
	ctx := context.Background()
	if err := s.ReplaceItems(ctx, user, seedItems()); err != nil {
		t.Fatal(err)
	}
	if err := s.KVSet(ctx, user, "k", json.RawMessage(`1`)); err != nil {
		t.Fatal(err)
	}
	// 大小写不同或后缀不同的用户是不同用户。
	for _, other := range []string{"USER@测试/1", user + "x", "另一个用户"} {
		items, err := s.Items(ctx, other)
		if err != nil || len(items) != 0 {
			t.Fatalf("user isolation broken for %q: %v %v", other, items, err)
		}
		if _, err := s.KVGet(ctx, other, "k"); !errors.Is(err, ihtml.ErrNotFound) {
			t.Fatalf("kv isolation broken for %q: %v", other, err)
		}
	}
}

func testOwnership(t *testing.T, s ihtml.Store) {
	t.Helper()
	ctx := context.Background()
	inputItems := seedItems()
	inputItems[0].Meta = map[string]string{}
	if err := s.ReplaceItems(ctx, user, inputItems); err != nil {
		t.Fatal(err)
	}
	inputValue := json.RawMessage(`{"a":1}`)
	if err := s.KVSet(ctx, user, "own", inputValue); err != nil {
		t.Fatal(err)
	}
	inputItems[0].Content = "mutated input"
	inputItems[0].Meta["empty-input"] = "mutated input"
	inputItems[1].Meta["slot"] = "mutated input"
	inputValue[0] = 'X'

	rev := revisionAt(42)
	rev.Items[0].Meta = map[string]string{}
	rev.Templates = []ihtml.Template{{
		ID: "ownership", Draft: ihtml.TemplateVersion{Nodes: []ihtml.TemplateNode{{
			ID: "root", Kind: ihtml.NodeCanvas,
			Props: map[string]json.RawMessage{"custom": json.RawMessage(`{"v":1}`)},
		}}},
	}}
	if err := s.AddRevision(ctx, user, rev, 0); err != nil {
		t.Fatal(err)
	}
	rev.Items[0].Content = "mutated revision input"
	rev.Items[0].Meta["empty-input"] = "mutated revision input"
	rev.Items[1].Meta["slot"] = "mutated revision input"
	rev.Templates[0].Draft.Nodes[0].Props["custom"][0] = 'X'

	items, err := s.Items(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	// 返回值必须是调用方可随意修改的快照: 改动不得写穿回存储。
	for i := range items {
		items[i].Content = "mutated"
		if items[i].Meta != nil {
			items[i].Meta["slot"] = "mutated"
			items[i].Meta["empty-output"] = "mutated"
		}
	}
	value, err := s.KVGet(ctx, user, "own")
	if err != nil {
		t.Fatal(err)
	}
	for i := range value {
		value[i] = 'X'
	}
	reread, err := s.Items(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range reread {
		if it.Content == "mutated" || it.Content == "mutated input" ||
			it.Meta["slot"] == "mutated" || it.Meta["slot"] == "mutated input" ||
			it.Meta["empty-input"] != "" || it.Meta["empty-output"] != "" {
			t.Fatalf("mutating returned items leaked into store: %+v", it)
		}
	}
	rereadValue, err := s.KVGet(ctx, user, "own")
	if err != nil || string(rereadValue) != `{"a":1}` {
		t.Fatalf("mutating returned kv leaked into store: %s %v", rereadValue, err)
	}
	revisionItems, err := s.RevisionItems(ctx, user, rev.ID)
	if err != nil || len(revisionItems) != len(rev.Items) {
		t.Fatalf("read back AddRevision input: %+v %v", revisionItems, err)
	}
	if revisionItems[0].Content == "mutated revision input" ||
		revisionItems[0].Meta["empty-input"] != "" ||
		revisionItems[1].Meta["slot"] == "mutated revision input" {
		t.Fatalf("mutating AddRevision input leaked into store: %+v %v", revisionItems, err)
	}
	if full, ok := s.(ihtml.RevisionStore); ok {
		stored, err := full.Revision(ctx, user, rev.ID)
		if err != nil || len(stored.Templates) != 1 || len(stored.Templates[0].Draft.Nodes) != 1 {
			t.Fatalf("read back full AddRevision input: %+v %v", stored, err)
		}
		if string(stored.Templates[0].Draft.Nodes[0].Props["custom"]) != `{"v":1}` {
			t.Fatalf("mutating revision template input leaked into store: %+v %v", stored, err)
		}
	}
}

func testErase(t *testing.T, s ihtml.Store) {
	t.Helper()
	ctx := context.Background()
	if err := s.ReplaceItems(ctx, user, seedItems()); err != nil {
		t.Fatal(err)
	}
	if err := s.KVSet(ctx, user, "k", json.RawMessage(`1`)); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRevision(ctx, user, revisionAt(0), 0); err != nil {
		t.Fatal(err)
	}
	if err := s.EraseUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	items, err := s.Items(ctx, user)
	if err != nil || len(items) != 0 {
		t.Fatalf("erase must remove items: %v %v", items, err)
	}
	if _, err := s.KVGet(ctx, user, "k"); !errors.Is(err, ihtml.ErrNotFound) {
		t.Fatalf("erase must remove kv: %v", err)
	}
	revs, err := s.Revisions(ctx, user)
	if err != nil || len(revs) != 0 {
		t.Fatalf("erase must remove revisions: %v %v", revs, err)
	}
	if err := s.EraseUser(ctx, user); err != nil {
		t.Fatalf("erasing an absent user must not error: %v", err)
	}
}

func testUserState(t *testing.T, s ihtml.UserStateStore) {
	t.Helper()
	ctx := context.Background()
	state, err := s.UserState(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	state.Items = seedItems()
	if state.KV == nil {
		state.KV = map[string]json.RawMessage{}
	}
	state.KV["k"] = json.RawMessage(`true`)
	state.Revisions = []ihtml.Revision{revisionAt(1), revisionAt(0)}
	if err := s.ReplaceUserState(ctx, user, state); err != nil {
		t.Fatal(err)
	}
	state.Items[0].Content = "mutated state input"
	state.Items[1].Meta["slot"] = "mutated state input"
	state.KV["k"][0] = 'X'
	state.Revisions[0].Items[0].Content = "mutated state revision input"
	reread, err := s.UserState(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	if len(reread.Items) != 2 || string(reread.KV["k"]) != "true" || len(reread.Revisions) != 2 {
		t.Fatalf("user state roundtrip: %+v", reread)
	}
	if reread.Items[0].Content == "mutated state input" || reread.Items[1].Meta["slot"] == "mutated state input" ||
		reread.Revisions[0].Items[0].Content == "mutated state revision input" {
		t.Fatalf("mutating ReplaceUserState input leaked into store: %+v", reread)
	}
	if reread.Revisions[0].ID != "r-001" {
		t.Fatalf("user state revisions must stay newest-first: %+v", reread.Revisions)
	}
	base := s.(ihtml.Store)
	items, err := base.Items(ctx, user)
	if err != nil || len(items) != 2 {
		t.Fatalf("state replace must be visible via base reads: %v %v", items, err)
	}
}

func testFullRevision(t *testing.T, s ihtml.Store) {
	t.Helper()
	ctx := context.Background()
	full := s.(ihtml.RevisionStore)
	rev := revisionAt(7)
	if err := s.AddRevision(ctx, user, rev, 0); err != nil {
		t.Fatal(err)
	}
	got, err := full.Revision(ctx, user, rev.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != ihtml.CurrentDataVersion || len(got.Pages) != 1 || got.Pages[0].Name != "dash" {
		t.Fatalf("full revision must preserve pages and version: %+v", got)
	}
	if _, err := full.Revision(ctx, user, "nope"); !errors.Is(err, ihtml.ErrNotFound) {
		t.Fatalf("missing full revision must be ErrNotFound, got %v", err)
	}
}

func testVersioning(t *testing.T, s ihtml.VersionedUserStateStore) {
	t.Helper()
	ctx := context.Background()
	state, err := s.UserState(ctx, user)
	if err != nil || state.Version != 0 {
		t.Fatalf("fresh user version must be 0: %d %v", state.Version, err)
	}

	// 多个写者基于同一 expected 并发条件写，只允许一个成功。
	const writers = 16
	start := make(chan struct{})
	results := make(chan error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			candidate := ihtml.UserState{
				Items: []ihtml.Item{{ID: fmt.Sprintf("winner-%02d", i), Type: ihtml.ItemHTML, Content: "<p/>"}},
				KV:    map[string]json.RawMessage{},
			}
			results <- s.ReplaceUserStateVersioned(ctx, user+"-concurrent", candidate, 0)
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ihtml.ErrVersionConflict):
			conflicts++
		default:
			t.Fatalf("concurrent conditional write returned unexpected error: %v", err)
		}
	}
	if successes != 1 || conflicts != writers-1 {
		t.Fatalf("same expected version admitted successes=%d conflicts=%d", successes, conflicts)
	}

	// 条件写: 基于同一版本的第二次写必须冲突且不产生任何修改。
	state.Items = []ihtml.Item{{ID: "a", Type: ihtml.ItemHTML, Content: "<a/>", UpdatedAt: time.Unix(1700000000, 0).UTC()}}
	if state.KV == nil {
		state.KV = map[string]json.RawMessage{}
	}
	if err := s.ReplaceUserStateVersioned(ctx, user, state, 0); err != nil {
		t.Fatal(err)
	}
	stale := state
	stale.Items = []ihtml.Item{{ID: "b", Type: ihtml.ItemHTML, Content: "<b/>", UpdatedAt: time.Unix(1700000000, 0).UTC()}}
	if err := s.ReplaceUserStateVersioned(ctx, user, stale, 0); !errors.Is(err, ihtml.ErrVersionConflict) {
		t.Fatalf("stale conditional replace must return ErrVersionConflict, got %v", err)
	}
	after, err := s.UserState(ctx, user)
	if err != nil || after.Version == 0 || len(after.Items) != 1 || after.Items[0].ID != "a" {
		t.Fatalf("conflicting write must not change state: %+v %v", after, err)
	}

	// 每个基础写方法都要推进版本, 否则整状态条件写检测不到交错写。
	base := s.(ihtml.Store)
	checkBump := func(op string, write func() error) {
		beforeState, err := s.UserState(ctx, user)
		if err != nil {
			t.Fatal(err)
		}
		if err := write(); err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		afterState, err := s.UserState(ctx, user)
		if err != nil {
			t.Fatal(err)
		}
		if afterState.Version <= beforeState.Version {
			t.Fatalf("%s must advance the user version: %d -> %d", op, beforeState.Version, afterState.Version)
		}
		if err := s.ReplaceUserStateVersioned(ctx, user, beforeState, beforeState.Version); !errors.Is(err, ihtml.ErrVersionConflict) {
			t.Fatalf("conditional replace after %s must conflict, got %v", op, err)
		}
	}
	checkBump("ReplaceItems", func() error { return base.ReplaceItems(ctx, user, seedItems()) })
	checkBump("KVSet", func() error { return base.KVSet(ctx, user, "vk", json.RawMessage(`1`)) })
	checkBump("KVDelete", func() error { return base.KVDelete(ctx, user, "vk") })
	checkBump("KVDelete missing", func() error { return base.KVDelete(ctx, user, "vk-missing") })
	checkBump("AddRevision", func() error { return base.AddRevision(ctx, user, revisionAt(9), 0) })
	checkBump("ReplaceUserState", func() error {
		st, err := s.UserState(ctx, user)
		if err != nil {
			return err
		}
		return s.ReplaceUserState(ctx, user, st)
	})

	// EraseUser 清空数据但版本继续递增，旧 expected 永不复用。
	beforeErase, err := s.UserState(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	if err := base.EraseUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	erased, err := s.UserState(ctx, user)
	if err != nil || erased.Version <= beforeErase.Version || len(erased.Items) != 0 || len(erased.KV) != 0 || len(erased.Revisions) != 0 {
		t.Fatalf("erase must leave an empty, newer tombstone: %+v %v", erased, err)
	}
	if err := s.ReplaceUserStateVersioned(ctx, user, beforeErase, beforeErase.Version); !errors.Is(err, ihtml.ErrVersionConflict) {
		t.Fatalf("pre-erase expected version must never become valid again: %v", err)
	}
	erased.Items = []ihtml.Item{{ID: "c", Type: ihtml.ItemHTML, Content: "<c/>", UpdatedAt: time.Unix(1700000000, 0).UTC()}}
	if erased.KV == nil {
		erased.KV = map[string]json.RawMessage{}
	}
	if err := s.ReplaceUserStateVersioned(ctx, user, erased, erased.Version); err != nil {
		t.Fatalf("versioned replace after erase must accept the tombstone version: %v", err)
	}
}
