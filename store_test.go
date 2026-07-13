package ihtml

import (
	"context"
	"encoding/json"
	"testing"
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

// 内置 Store 的契约验收统一走公开的 storetest 套件,
// 见 store_conformance_test.go(外部测试包, 避免 storetest→ihtml 循环导入)。
