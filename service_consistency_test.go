package ihtml

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestPutItemsLimitFailureDoesNotCreateRevision(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	existing := make([]Item, MaxItemsPerUser)
	for i := range existing {
		existing[i] = Item{ID: fmt.Sprintf("i%d", i), Type: ItemCSS, Content: ".x{}"}
	}
	if err := store.ReplaceItems(ctx, "u", existing); err != nil {
		t.Fatal(err)
	}
	h := New(store)
	if err := h.PutItems(ctx, "u", []Item{{ID: "overflow", Type: ItemCSS, Content: ".y{}"}}, "api", "overflow"); err == nil {
		t.Fatal("expected item quota error")
	}
	revisions, err := store.Revisions(ctx, "u")
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 0 {
		t.Fatalf("failed write created %d revisions", len(revisions))
	}
	items, _ := store.Items(ctx, "u")
	if len(items) != MaxItemsPerUser {
		t.Fatalf("failed write changed item set: %d", len(items))
	}
}

func TestNoopPutDoesNotRewriteOrSnapshot(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	h := New(store)
	item := Item{ID: "a", Type: ItemHTML, Content: "<p>x</p>"}
	if err := h.PutItems(ctx, "u", []Item{item}, "api", "first"); err != nil {
		t.Fatal(err)
	}
	beforeItems, _ := store.Items(ctx, "u")
	beforeRevisions, _ := store.Revisions(ctx, "u")
	if err := h.PutItems(ctx, "u", []Item{item}, "api", "same"); err != nil {
		t.Fatal(err)
	}
	afterItems, _ := store.Items(ctx, "u")
	afterRevisions, _ := store.Revisions(ctx, "u")
	if !reflect.DeepEqual(beforeItems, afterItems) || len(beforeRevisions) != len(afterRevisions) {
		t.Fatalf("no-op put rewrote state: revisions %d -> %d", len(beforeRevisions), len(afterRevisions))
	}
}

func TestKVQuotaIsAtomicWithinHandler(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	h := New(store, WithLimits(Limits{MaxKVKeysPerUser: 1}))
	const writers = 32
	var wg sync.WaitGroup
	results := make(chan error, writers)
	for i := range writers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results <- h.KVSet(ctx, "u", fmt.Sprintf("k%d", i), json.RawMessage(`1`))
		}(i)
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("quota race allowed %d successful new keys", successes)
	}
	keys, _ := store.KVKeys(ctx, "u")
	if len(keys) != 1 {
		t.Fatalf("stored key count = %d, want 1", len(keys))
	}
}

func TestPageSnapshotsRollbackSetAndDelete(t *testing.T) {
	ctx := context.Background()
	h := New(NewMemoryStore())
	if err := h.SetPage(ctx, "u", Page{Name: "dash", Title: "Dashboard"}); err != nil {
		t.Fatal(err)
	}
	revisions, _ := h.ListRevisions(ctx, "u")
	if len(revisions) != 1 {
		t.Fatalf("first page creation did not snapshot empty v2 state: %+v", revisions)
	}
	exported, err := h.ExportUser(ctx, "u")
	if err != nil || len(exported.Revisions) != 1 || exported.Revisions[0].Version != CurrentDataVersion {
		t.Fatalf("first page snapshot is not v2: export=%+v err=%v", exported, err)
	}
	if err := h.Rollback(ctx, "u", revisions[0].ID, "api"); err != nil {
		t.Fatal(err)
	}
	pages, _ := h.ListPages(ctx, "u")
	if len(pages) != 0 {
		t.Fatalf("rollback of first page should restore empty registry: %+v", pages)
	}

	if err := h.SetPage(ctx, "u", Page{Name: "dash", Title: "Dashboard"}); err != nil {
		t.Fatal(err)
	}
	if err := h.PutItems(ctx, "u", []Item{{ID: "dash-ui", Type: ItemHTML, Page: "dash", Content: "<main/>"}}, "api", "ui"); err != nil {
		t.Fatal(err)
	}
	removed, err := h.DeletePage(ctx, "u", "dash", "api")
	if err != nil || len(removed) != 1 || removed[0] != "dash-ui" {
		t.Fatalf("delete page: removed=%v err=%v", removed, err)
	}
	revisions, _ = h.ListRevisions(ctx, "u")
	if err := h.Rollback(ctx, "u", revisions[0].ID, "api"); err != nil {
		t.Fatal(err)
	}
	pages, _ = h.ListPages(ctx, "u")
	items, _ := h.ListItems(ctx, "u")
	if len(pages) != 1 || pages[0].Name != "dash" || len(items) != 1 || items[0].ID != "dash-ui" {
		t.Fatalf("delete rollback left orphaned state: pages=%+v items=%+v", pages, items)
	}
}

type rejectingStateStore struct {
	*MemoryStore
	reject bool
}

func (s *rejectingStateStore) ReplaceUserState(ctx context.Context, user string, state UserState) error {
	if s.reject {
		return errors.New("injected atomic replace failure")
	}
	return s.MemoryStore.ReplaceUserState(ctx, user, state)
}

func TestDeletePageFailureLeavesNoPartialState(t *testing.T) {
	ctx := context.Background()
	store := &rejectingStateStore{MemoryStore: NewMemoryStore()}
	h := New(store)
	if err := h.SetPage(ctx, "u", Page{Name: "p", Title: "P"}); err != nil {
		t.Fatal(err)
	}
	if err := h.PutItems(ctx, "u", []Item{{ID: "a", Type: ItemHTML, Page: "p", Content: "x"}}, "api", "put"); err != nil {
		t.Fatal(err)
	}
	if err := h.KVSet(ctx, "u", ItemLayoutKVPrefix+"a", json.RawMessage(`{"page":"p","x":12}`)); err != nil {
		t.Fatal(err)
	}
	before, _ := store.UserState(ctx, "u")
	store.reject = true
	if _, err := h.DeletePage(ctx, "u", "p", "api"); err == nil {
		t.Fatal("expected injected delete failure")
	}
	after, _ := store.UserState(ctx, "u")
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("failed delete changed state\nbefore=%+v\nafter=%+v", before, after)
	}
}

func TestDeletePageCleansOnlyMatchingItemLayouts(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	h := New(store)
	if err := h.SetPage(ctx, "u", Page{Name: "dash", Title: "Dashboard"}); err != nil {
		t.Fatal(err)
	}
	records := map[string]json.RawMessage{
		ItemLayoutKVPrefix + "card":   json.RawMessage(`{"page":"dash","x":12}`),
		"ITEM-LAYOUT.chart":           json.RawMessage(`{"page":"dash","x":24}`),
		ItemLayoutKVPrefix + "other":  json.RawMessage(`{"page":"other","x":36}`),
		ItemLayoutKVPrefix + "global": json.RawMessage(`{"page":"","x":48}`),
		ItemLayoutKVPrefix + "bad":    json.RawMessage(`{"page":`),
		ItemLayoutKVPrefix + "typed":  json.RawMessage(`{"page":7}`),
		"feature.card":                json.RawMessage(`{"page":"dash"}`),
	}
	for key, value := range records {
		// Seed through the Store so deliberately corrupt legacy data can be
		// verified without weakening KVSet's JSON validation.
		if err := store.KVSet(ctx, "u", key, value); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := h.DeletePage(ctx, "u", "dash", "api"); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{ItemLayoutKVPrefix + "card", "ITEM-LAYOUT.chart"} {
		if _, err := store.KVGet(ctx, "u", key); !errors.Is(err, ErrNotFound) {
			t.Fatalf("matching layout %q was not removed: %v", key, err)
		}
	}
	for _, key := range []string{
		ItemLayoutKVPrefix + "other",
		ItemLayoutKVPrefix + "global",
		ItemLayoutKVPrefix + "bad",
		ItemLayoutKVPrefix + "typed",
		"feature.card",
	} {
		if _, err := store.KVGet(ctx, "u", key); err != nil {
			t.Fatalf("unrelated or malformed layout %q was removed: %v", key, err)
		}
	}

	// Orphaned layout state is still lifecycle data for the named page and must
	// be cleaned even when its Page registry entry and Items are already gone.
	orphanKey := ItemLayoutKVPrefix + "orphan"
	if err := store.KVSet(ctx, "u", orphanKey, json.RawMessage(`{"page":"ghost"}`)); err != nil {
		t.Fatal(err)
	}
	removed, err := h.DeletePage(ctx, "u", "ghost", "api")
	if err != nil || len(removed) != 0 {
		t.Fatalf("delete orphaned layout: removed=%v err=%v", removed, err)
	}
	if _, err := store.KVGet(ctx, "u", orphanKey); !errors.Is(err, ErrNotFound) {
		t.Fatalf("orphaned layout was not removed: %v", err)
	}
}

// faultLegacyStore intentionally exposes only the original Store interface so the
// Service compensation path is exercised even though its backing store is modern.
type faultLegacyStore struct {
	Store
	failKVOnce string
	readKVFail string
	revReadErr bool
}

func (s *faultLegacyStore) KVSet(ctx context.Context, user, key string, value json.RawMessage) error {
	if key == s.failKVOnce {
		s.failKVOnce = ""
		return errors.New("injected kv write failure")
	}
	return s.Store.KVSet(ctx, user, key, value)
}

func (s *faultLegacyStore) KVGet(ctx context.Context, user, key string) (json.RawMessage, error) {
	if key == s.readKVFail {
		return nil, errors.New("injected kv read failure")
	}
	return s.Store.KVGet(ctx, user, key)
}

func (s *faultLegacyStore) RevisionItems(ctx context.Context, user, id string) ([]Item, error) {
	if s.revReadErr {
		return nil, errors.New("injected revision read failure")
	}
	return s.Store.RevisionItems(ctx, user, id)
}

func TestImportLegacyStoreCompensatesOnFailure(t *testing.T) {
	ctx := context.Background()
	legacy := &faultLegacyStore{Store: NewMemoryStore()}
	h := New(legacy)
	if err := h.SetPage(ctx, "u", Page{Name: "old", Title: "Old"}); err != nil {
		t.Fatal(err)
	}
	if err := h.PutItems(ctx, "u", []Item{{ID: "old", Type: ItemHTML, Page: "old", Content: "old"}}, "api", "old"); err != nil {
		t.Fatal(err)
	}
	if err := h.KVSet(ctx, "u", "keep", json.RawMessage(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	before, err := h.ExportUser(ctx, "u")
	if err != nil {
		t.Fatal(err)
	}
	legacy.failKVOnce = "boom"
	err = h.ImportUser(ctx, "u", &UserExport{
		Version: CurrentDataVersion,
		Items:   []Item{{ID: "new", Type: ItemHTML, Content: "new"}},
		KV:      map[string]json.RawMessage{"boom": json.RawMessage(`true`)},
	})
	if err == nil || !strings.Contains(err.Error(), "previous state restored") {
		t.Fatalf("expected compensated import error, got %v", err)
	}
	after, err := h.ExportUser(ctx, "u")
	if err != nil {
		t.Fatal(err)
	}
	beforeJSON, _ := json.Marshal(before)
	afterJSON, _ := json.Marshal(after)
	if string(beforeJSON) != string(afterJSON) {
		t.Fatalf("compensation did not restore export\nbefore=%s\nafter=%s", beforeJSON, afterJSON)
	}
}

func TestExportDoesNotSkipReadErrors(t *testing.T) {
	ctx := context.Background()
	base := NewMemoryStore()
	if err := base.KVSet(ctx, "u", "bad", json.RawMessage(`1`)); err != nil {
		t.Fatal(err)
	}
	legacy := &faultLegacyStore{Store: base, readKVFail: "bad"}
	if _, err := New(legacy).ExportUser(ctx, "u"); err == nil {
		t.Fatal("KV read error was silently skipped")
	}

	legacy.readKVFail = ""
	if err := base.AddRevision(ctx, "u", Revision{ID: "r1", CreatedAt: newRevision(nil, nil, "", "").CreatedAt}, 10); err != nil {
		t.Fatal(err)
	}
	legacy.revReadErr = true
	if _, err := New(legacy).ExportUser(ctx, "u"); err == nil {
		t.Fatal("revision read error was silently skipped")
	}
}

func TestImportValidationIsStrict(t *testing.T) {
	h := New(NewMemoryStore(), WithLimits(Limits{MaxKVKeysPerUser: 1}))
	tests := []struct {
		name   string
		export *UserExport
	}{
		{"future version", &UserExport{Version: CurrentDataVersion + 1}},
		{"invalid kv key", &UserExport{KV: map[string]json.RawMessage{"bad key": json.RawMessage(`1`)}}},
		{"invalid kv json", &UserExport{KV: map[string]json.RawMessage{"bad": json.RawMessage(`{`)}}},
		{"kv quota", &UserExport{KV: map[string]json.RawMessage{"a": json.RawMessage(`1`), "b": json.RawMessage(`2`)}}},
		{"duplicate pages", &UserExport{Pages: []Page{{Name: "p", Title: "P"}, {Name: "p", Title: "Again"}}}},
		{"duplicate revisions", &UserExport{Revisions: []Revision{{ID: "r"}, {ID: "r"}}}},
		{"reserved pages kv", &UserExport{KV: map[string]json.RawMessage{pagesKVKey: json.RawMessage(`[]`)}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := h.ImportUser(context.Background(), "u", tt.export); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestFileStoreUsesCaseSensitiveHashedUserNamesAndMigratesLegacy(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if store.fileName("Alice") == store.fileName("alice") {
		t.Fatal("case-distinct users mapped to same hash path")
	}
	if strings.Contains(store.fileName("Alice"), "Alice") {
		t.Fatalf("raw username leaked into %q", store.fileName("Alice"))
	}
	if err := store.ReplaceItems(ctx, "Alice", []Item{{ID: "upper", Type: ItemHTML, Content: "A"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceItems(ctx, "alice", []Item{{ID: "lower", Type: ItemHTML, Content: "a"}}); err != nil {
		t.Fatal(err)
	}
	upper, _ := store.Items(ctx, "Alice")
	lower, _ := store.Items(ctx, "alice")
	if len(upper) != 1 || upper[0].ID != "upper" || len(lower) != 1 || lower[0].ID != "lower" {
		t.Fatalf("case-distinct users collided: upper=%+v lower=%+v", upper, lower)
	}

	legacyUser := "LegacyUser"
	legacyRaw, _ := json.Marshal(fileUserData{Items: []Item{{ID: "legacy", Type: ItemHTML, Content: "x"}}})
	if err := os.WriteFile(store.legacyFileName(legacyUser), legacyRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	legacyItems, err := store.Items(ctx, legacyUser)
	if err != nil || len(legacyItems) != 1 {
		t.Fatalf("legacy file was not readable: %+v %v", legacyItems, err)
	}
	if err := store.KVSet(ctx, legacyUser, "migrated", json.RawMessage(`true`)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.fileName(legacyUser)); err != nil {
		t.Fatalf("hashed migrated file missing: %v", err)
	}
	if _, err := os.Stat(store.legacyFileName(legacyUser)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy path still exists after migration: %v", err)
	}
}
