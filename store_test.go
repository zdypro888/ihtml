package ihtml

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type aliasingReadStore struct {
	Store
	items     []Item
	value     json.RawMessage
	revisions []RevisionInfo
}

func (s *aliasingReadStore) Items(context.Context, string) ([]Item, error) {
	return s.items, nil
}

func (s *aliasingReadStore) KVGet(context.Context, string, string) (json.RawMessage, error) {
	return s.value, nil
}

func (s *aliasingReadStore) Revisions(context.Context, string) ([]RevisionInfo, error) {
	return s.revisions, nil
}

func TestHandlerDefensivelyCopiesThirdPartyStoreReads(t *testing.T) {
	store := &aliasingReadStore{
		Store: NewMemoryStore(),
		items: []Item{
			{ID: "later", Type: ItemHTML, Order: 2, Content: "<div/>", Meta: map[string]string{"owner": "store"}},
			{ID: "first", Type: ItemHTML, Order: 1, Content: "<div/>"},
		},
		value:     json.RawMessage(`{"owned":true}`),
		revisions: []RevisionInfo{{ID: "revision-owned-by-store"}},
	}
	h := New(store)
	defer h.Close()

	items, err := h.ListItems(context.Background(), "user")
	if err != nil {
		t.Fatal(err)
	}
	items[1].Meta["owner"] = "caller"
	items[0].ID = "caller"
	if store.items[0].ID != "later" || store.items[0].Meta["owner"] != "store" {
		t.Fatalf("store item memory was mutated: %#v", store.items)
	}

	value, err := h.KVGet(context.Background(), "user", "key")
	if err != nil {
		t.Fatal(err)
	}
	value[2] = 'X'
	if string(store.value) != `{"owned":true}` {
		t.Fatalf("store raw message was mutated: %s", store.value)
	}

	revisions, err := h.ListRevisions(context.Background(), "user")
	if err != nil {
		t.Fatal(err)
	}
	revisions[0].ID = "caller"
	if store.revisions[0].ID != "revision-owned-by-store" {
		t.Fatal("store revision slice was mutated")
	}
}

// 两个内置 Store 跑同一套一致性用例。
func TestStores(t *testing.T) {
	t.Run("memory", func(t *testing.T) { storeConformance(t, NewMemoryStore()) })
	t.Run("file", func(t *testing.T) {
		fs, err := NewFileStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		storeConformance(t, fs)
	})
}

func storeConformance(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	user := "user@测试/1" // 故意用带特殊字符的用户名

	// 初始为空
	items, err := s.Items(ctx, user)
	if err != nil || len(items) != 0 {
		t.Fatalf("empty items: %v %v", items, err)
	}

	// 写入与读取
	set := []Item{
		{ID: "b", Type: ItemJS, Order: 2, Content: "console.log(1)", UpdatedAt: time.Now()},
		{ID: "a", Type: ItemHTML, Order: 1, Content: "<div/>", Meta: map[string]string{"k": "v"}},
	}
	if err := s.ReplaceItems(ctx, user, set); err != nil {
		t.Fatal(err)
	}
	items, err = s.Items(ctx, user)
	if err != nil || len(items) != 2 {
		t.Fatalf("items after replace: %v %v", items, err)
	}

	// KV
	if _, err := s.KVGet(ctx, user, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing kv should be ErrNotFound, got %v", err)
	}
	if err := s.KVSet(ctx, user, "theme", json.RawMessage(`{"mode":"dark"}`)); err != nil {
		t.Fatal(err)
	}
	v, err := s.KVGet(ctx, user, "theme")
	if err != nil || string(v) != `{"mode":"dark"}` {
		t.Fatalf("kv get: %s %v", v, err)
	}
	if err := s.KVDelete(ctx, user, "theme"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.KVGet(ctx, user, "theme"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted kv should be ErrNotFound, got %v", err)
	}

	// 快照与裁剪
	for i := range 5 {
		rev := Revision{
			Version: CurrentDataVersion, ID: newRevisionID() + string(rune('a'+i)), CreatedAt: time.Now(),
			Items: set, Pages: []Page{{Name: "dash", Title: "Dashboard"}}, Note: "n",
		}
		if err := s.AddRevision(ctx, user, rev, 3); err != nil {
			t.Fatal(err)
		}
	}
	revs, err := s.Revisions(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	if len(revs) != 3 {
		t.Fatalf("revisions should be pruned to 3, got %d", len(revs))
	}
	got, err := s.RevisionItems(ctx, user, revs[0].ID)
	if err != nil || len(got) != 2 {
		t.Fatalf("revision items: %v %v", got, err)
	}
	if _, err := s.RevisionItems(ctx, user, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing revision should be ErrNotFound, got %v", err)
	}
	fullStore, ok := s.(RevisionStore)
	if !ok {
		t.Fatal("built-in store should implement RevisionStore")
	}
	full, err := fullStore.Revision(ctx, user, revs[0].ID)
	if err != nil || full.Version != CurrentDataVersion || len(full.Pages) != 1 {
		t.Fatalf("full revision roundtrip: %+v %v", full, err)
	}

	stateStore, ok := s.(UserStateStore)
	if !ok {
		t.Fatal("built-in store should implement UserStateStore")
	}
	state, err := stateStore.UserState(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	state.Items = []Item{{ID: "state", Type: ItemCSS, Content: ".state{}"}}
	state.KV["state"] = json.RawMessage(`true`)
	if err := stateStore.ReplaceUserState(ctx, user, state); err != nil {
		t.Fatal(err)
	}
	items, _ = s.Items(ctx, user)
	value, valueErr := s.KVGet(ctx, user, "state")
	if len(items) != 1 || items[0].ID != "state" || valueErr != nil || string(value) != "true" {
		t.Fatalf("atomic state replace: items=%+v value=%s err=%v", items, value, valueErr)
	}

	// 用户隔离
	other, err := s.Items(ctx, "另一个用户")
	if err != nil || len(other) != 0 {
		t.Fatalf("user isolation broken: %v %v", other, err)
	}
}
