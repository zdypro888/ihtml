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

func TestKVCompareAndSetIsAtomicAcrossHandlers(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	first := New(store)
	second := New(store)
	t.Cleanup(func() { _ = first.Close() })
	t.Cleanup(func() { _ = second.Close() })

	revision, err := first.KVCompareAndSet(ctx, "u", "layout", json.RawMessage(`{"value":0}`), MissingKVRevision)
	if err != nil || revision == "" || revision == MissingKVRevision {
		t.Fatalf("initial conditional create: revision=%q err=%v", revision, err)
	}
	if err := second.KVSet(ctx, "u", "unrelated", json.RawMessage(`true`)); err != nil {
		t.Fatal(err)
	}
	// An unrelated user-state version bump must not invalidate the per-key token.
	revision, err = first.KVCompareAndSet(ctx, "u", "layout", json.RawMessage(`{"value":1}`), revision)
	if err != nil {
		t.Fatalf("unrelated write caused a false conflict: %v", err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	writesBefore := first.Stats()["writes"] + second.Stats()["writes"]
	for i, handler := range []*Handler{first, second} {
		i, handler := i, handler
		go func() {
			<-start
			_, err := handler.KVCompareAndSet(ctx, "u", "layout", json.RawMessage(fmt.Sprintf(`{"writer":%d}`, i)), revision)
			results <- err
		}()
	}
	close(start)
	successes, conflicts := 0, 0
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrKVConflict):
			conflicts++
			var detail *KVConflictError
			if !errors.As(err, &detail) || !detail.Exists || detail.Actual == revision || !json.Valid(detail.Current) {
				t.Fatalf("conflict did not carry the latest value: %#v", detail)
			}
		default:
			t.Fatalf("unexpected conditional write error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("conditional results: successes=%d conflicts=%d", successes, conflicts)
	}
	if writesAfter := first.Stats()["writes"] + second.Stats()["writes"]; writesAfter != writesBefore+1 {
		t.Fatalf("failed CAS changed writes metric: before=%d after=%d", writesBefore, writesAfter)
	}
}

func TestDeleteItemsAtomicallyCleansPersistedItemLayouts(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	h := New(store)
	t.Cleanup(func() { _ = h.Close() })
	if err := h.PutItems(ctx, "u", []Item{
		{ID: "a", Type: ItemHTML, Content: "<p>A</p>"},
		{ID: "b", Type: ItemHTML, Content: "<p>B</p>"},
	}, "api", "seed"); err != nil {
		t.Fatal(err)
	}
	layout := json.RawMessage(`{"version":1,"page":"","mode":"grid","unknown":{"keep":true},"items":{"a":{"slot":"main"},"b":{"slot":"main"}},"orderBySlot":{"main":["a","b"]}}`)
	if err := h.KVSet(ctx, "u", ItemLayoutKVPrefix+"root", layout); err != nil {
		t.Fatal(err)
	}
	removed, err := h.DeleteItems(ctx, "u", []string{"a"}, "api", "delete a")
	if err != nil || len(removed) != 1 || removed[0] != "a" {
		t.Fatalf("delete result=%v err=%v", removed, err)
	}
	raw, err := h.KVGet(ctx, "u", ItemLayoutKVPrefix+"root")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Unknown map[string]bool            `json:"unknown"`
		Items   map[string]json.RawMessage `json:"items"`
		Order   map[string][]string        `json:"orderBySlot"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if _, exists := doc.Items["a"]; exists || doc.Items["b"] == nil || !doc.Unknown["keep"] || !reflect.DeepEqual(doc.Order["main"], []string{"b"}) {
		t.Fatalf("layout cleanup lost or retained the wrong data: %s", raw)
	}
}

func TestDeleteItemsCleansCaseInsensitiveItemLayoutKeys(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	h := New(store)
	t.Cleanup(func() { _ = h.Close() })
	if err := h.PutItems(ctx, "u", []Item{
		{ID: "a", Type: ItemHTML, Content: "<p>A</p>"},
		{ID: "b", Type: ItemHTML, Content: "<p>B</p>"},
	}, "api", "seed"); err != nil {
		t.Fatal(err)
	}
	key := "ITEM-LAYOUT.root"
	if err := h.KVSet(ctx, "u", key, json.RawMessage(`{"version":1,"page":"","items":{"a":{"slot":"main"},"b":{"slot":"main"}},"orderBySlot":{"main":["a","b"]}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.DeleteItems(ctx, "u", []string{"a"}, "api", "delete a"); err != nil {
		t.Fatal(err)
	}
	raw, err := h.KVGet(ctx, "u", key)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Items map[string]json.RawMessage `json:"items"`
		Order map[string][]string        `json:"orderBySlot"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if _, exists := doc.Items["a"]; exists || doc.Items["b"] == nil || !reflect.DeepEqual(doc.Order["main"], []string{"b"}) {
		t.Fatalf("case-insensitive layout cleanup retained the deleted Item: %s", raw)
	}
}

func TestDeleteItemsLayoutCleanupDoesNotExpandHTMLNearKVLimit(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	h := New(store)
	t.Cleanup(func() { _ = h.Close() })
	if err := h.PutItems(ctx, "u", []Item{
		{ID: "a", Type: ItemHTML, Content: "<p>A</p>"},
		{ID: "b", Type: ItemHTML, Content: "<p>B</p>"},
	}, "api", "seed"); err != nil {
		t.Fatal(err)
	}

	prefix := `{"version":1,"page":"","items":{"a":{"slot":"main"},"b":{"slot":"main"}},"orderBySlot":{"main":["a","b"]},"unknown":"`
	suffix := `"}`
	unknownLength := MaxKVValueBytes - len(prefix) - len(suffix)
	if unknownLength <= 0 {
		t.Fatal("invalid near-limit test fixture")
	}
	raw := json.RawMessage(prefix + strings.Repeat("<", unknownLength) + suffix)
	if len(raw) != MaxKVValueBytes || !json.Valid(raw) {
		t.Fatalf("invalid near-limit layout fixture: bytes=%d valid=%v", len(raw), json.Valid(raw))
	}
	key := ItemLayoutKVPrefix + "near-limit"
	if err := h.KVSet(ctx, "u", key, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := h.DeleteItems(ctx, "u", []string{"a"}, "api", "delete a"); err != nil {
		t.Fatalf("valid near-limit layout blocked Item deletion: %v", err)
	}
	cleaned, err := h.KVGet(ctx, "u", key)
	if err != nil {
		t.Fatal(err)
	}
	if len(cleaned) > MaxKVValueBytes || strings.Contains(string(cleaned), `\u003c`) {
		t.Fatalf("layout cleanup expanded opaque HTML: bytes=%d", len(cleaned))
	}
	var doc struct {
		Unknown string                     `json:"unknown"`
		Items   map[string]json.RawMessage `json:"items"`
		Order   map[string][]string        `json:"orderBySlot"`
	}
	if err := json.Unmarshal(cleaned, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Unknown) != unknownLength || strings.Trim(doc.Unknown, "<") != "" {
		t.Fatal("layout cleanup did not preserve the opaque HTML field")
	}
	if _, exists := doc.Items["a"]; exists || doc.Items["b"] == nil || !reflect.DeepEqual(doc.Order["main"], []string{"b"}) {
		t.Fatalf("near-limit layout cleanup retained the deleted Item: %s", cleaned[:min(len(cleaned), 512)])
	}
}

func TestPutItemsAggregateByteLimitIsAtomic(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	existing := make([]Item, 0, 16)
	for i := 0; i < 15; i++ {
		existing = append(existing, Item{ID: fmt.Sprintf("large-%d", i), Type: ItemCSS, Content: strings.Repeat("x", MaxItemContentBytes)})
	}
	existing = append(existing, Item{ID: "remainder", Type: ItemCSS, Content: strings.Repeat("y", 400<<10)})
	if err := store.ReplaceItems(ctx, "u", existing); err != nil {
		t.Fatal(err)
	}
	h := New(store)
	err := h.PutItems(ctx, "u", []Item{{ID: "overflow", Type: ItemCSS, Content: strings.Repeat("z", 200<<10)}}, "api", "overflow")
	if err == nil || !strings.Contains(err.Error(), "item data exceeds") {
		t.Fatalf("aggregate limit error = %v", err)
	}
	revisions, revErr := store.Revisions(ctx, "u")
	if revErr != nil {
		t.Fatal(revErr)
	}
	if len(revisions) != 0 {
		t.Fatalf("failed aggregate write created %d revisions", len(revisions))
	}
	items, itemsErr := store.Items(ctx, "u")
	if itemsErr != nil {
		t.Fatal(itemsErr)
	}
	if len(items) != len(existing) {
		t.Fatalf("failed aggregate write changed item count: %d", len(items))
	}
}

func TestListItemsRejectsOversizedOrUnboundedStoredMetadata(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	items := make([]Item, 17)
	for i := range items {
		items[i] = Item{ID: fmt.Sprintf("large-%d", i), Type: ItemHTML, Content: strings.Repeat("x", MaxItemContentBytes)}
	}
	if err := store.ReplaceItems(ctx, "u", items); err != nil {
		t.Fatal(err)
	}
	h := New(store)
	if _, err := h.ListItems(ctx, "u"); err == nil || !strings.Contains(err.Error(), "item data exceeds") {
		t.Fatalf("oversized stored item set error = %v", err)
	}

	badMeta := Item{ID: "meta", Type: ItemHTML, Meta: map[string]string{"slot": strings.Repeat("x", MaxItemMetaValueBytes+1)}}
	if err := badMeta.Validate(); err == nil {
		t.Fatal("oversized Item metadata value was accepted")
	}
}

func TestPrependRevisionEnforcesAggregateByteRetention(t *testing.T) {
	large := strings.Repeat("x", MaxItemContentBytes)
	items := make([]Item, 0, 16)
	for i := 0; i < 15; i++ {
		items = append(items, Item{ID: fmt.Sprintf("large-%d", i), Type: ItemHTML, Content: large})
	}
	items = append(items, Item{ID: "remainder", Type: ItemHTML, Content: strings.Repeat("y", 400<<10)})
	if err := validateItems(items); err != nil {
		t.Fatalf("test revision must itself remain valid: %v", err)
	}

	state := UserState{}
	for i := 0; i < 12; i++ {
		prependRevision(&state, Revision{ID: fmt.Sprintf("r-%d", i), Items: items}, 0)
	}
	total := 0
	for _, revision := range state.Revisions {
		total += revisionSourceBytes(revision)
	}
	if total > MaxUserRevisionBytes {
		t.Fatalf("retained revisions use %d bytes, limit is %d", total, MaxUserRevisionBytes)
	}
	if len(state.Revisions) == 0 || len(state.Revisions) >= 12 {
		t.Fatalf("byte retention kept %d revisions, want a non-empty bounded suffix", len(state.Revisions))
	}
	if state.Revisions[0].ID != "r-11" {
		t.Fatalf("newest revision was not retained: %+v", state.Revisions)
	}
}

func TestStoredStateAggregateGuardsBoundSharedPayloads(t *testing.T) {
	// Reuse the same backing bytes to keep the test allocation small while the
	// validator still accounts for the logical per-key payload copied by a Store.
	value := json.RawMessage(`"` + strings.Repeat("x", maxInternalKVValueSize-2) + `"`)
	kv := make(map[string]json.RawMessage, 40)
	for i := 0; i < 40; i++ {
		kv[fmt.Sprintf("__ihtml.test-%02d", i)] = value
	}
	if err := validateStoredKV(kv, 0); err == nil || !strings.Contains(err.Error(), "kv data exceeds") {
		t.Fatalf("aggregate kv guard error = %v", err)
	}

	large := strings.Repeat("x", MaxItemContentBytes)
	items := make([]Item, 0, 16)
	for i := 0; i < 15; i++ {
		items = append(items, Item{ID: fmt.Sprintf("large-%d", i), Type: ItemHTML, Content: large})
	}
	items = append(items, Item{ID: "remainder", Type: ItemHTML, Content: strings.Repeat("y", 400<<10)})
	revisions := make([]Revision, 9)
	for i := range revisions {
		revisions[i] = Revision{ID: fmt.Sprintf("r-%d", i), Items: items}
	}
	if err := validateStoredRevisions(revisions); err == nil || !strings.Contains(err.Error(), "revisions exceed") {
		t.Fatalf("aggregate revision guard error = %v", err)
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

func TestKVQuotaIsAtomicAcrossHandlers(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	replicaA := New(store, WithLimits(Limits{MaxKVKeysPerUser: 1}))
	replicaB := New(store, WithLimits(Limits{MaxKVKeysPerUser: 1}))
	defer replicaA.Close()
	defer replicaB.Close()

	const writers = 32
	var wg sync.WaitGroup
	results := make(chan error, writers)
	for i := range writers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			replica := replicaA
			if i%2 != 0 {
				replica = replicaB
			}
			results <- replica.KVSet(ctx, "u", fmt.Sprintf("cross%d", i), json.RawMessage(`1`))
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
		t.Fatalf("cross-handler quota race allowed %d successful new keys", successes)
	}
	keys, err := store.KVKeys(ctx, "u")
	if err != nil || len(keys) != 1 {
		t.Fatalf("stored keys = %v, err=%v; want exactly one", keys, err)
	}
}

func TestReportErrorsMergesAcrossHandlers(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	replicaA, replicaB := New(store), New(store)
	defer replicaA.Close()
	defer replicaB.Close()

	const reports = 40 // 低于 MaxPageErrors，所有并发记录都必须保留。
	var wg sync.WaitGroup
	errs := make(chan error, reports)
	for i := range reports {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			replica := replicaA
			if i%2 != 0 {
				replica = replicaB
			}
			errs <- replica.ReportErrors(ctx, "u", []PageError{{
				ItemID: "item", Message: fmt.Sprintf("error-%02d", i),
			}})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent ReportErrors: %v", err)
		}
	}

	got, err := replicaA.PageErrors(ctx, "u")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != reports {
		t.Fatalf("lost concurrent error reports: got %d, want %d", len(got), reports)
	}
	if metric := replicaA.Stats()["errors_reported"] + replicaB.Stats()["errors_reported"]; metric != reports {
		t.Fatalf("versioned errors_reported metric = %d, want %d", metric, reports)
	}
	if metric := replicaA.Stats()["writes"] + replicaB.Stats()["writes"]; metric != reports {
		t.Fatalf("versioned ReportErrors writes metric = %d, want %d", metric, reports)
	}
	seen := make(map[string]bool, reports)
	for _, pageErr := range got {
		seen[pageErr.Message] = true
	}
	for i := range reports {
		message := fmt.Sprintf("error-%02d", i)
		if !seen[message] {
			t.Fatalf("missing concurrent report %q", message)
		}
	}
}

type legacyStoreView struct{ Store }

type preflightLegacyStore struct {
	Store
	keys          []string
	revisions     []RevisionInfo
	kvGets        int
	revisionReads int
}

func (s *preflightLegacyStore) KVKeys(context.Context, string) ([]string, error) {
	return append([]string(nil), s.keys...), nil
}

func (s *preflightLegacyStore) KVGet(context.Context, string, string) (json.RawMessage, error) {
	s.kvGets++
	return json.RawMessage(`true`), nil
}

func (s *preflightLegacyStore) Revisions(context.Context, string) ([]RevisionInfo, error) {
	return append([]RevisionInfo(nil), s.revisions...), nil
}

func (s *preflightLegacyStore) RevisionItems(context.Context, string, string) ([]Item, error) {
	s.revisionReads++
	return nil, nil
}

func TestLegacyStoreMetadataIsValidatedBeforeSecondaryReads(t *testing.T) {
	ctx := context.Background()
	badKey := &preflightLegacyStore{
		Store: NewMemoryStore(),
		keys:  []string{strings.Repeat("k", 65)},
	}
	if _, err := New(badKey).ExportUser(ctx, "u"); err == nil || badKey.kvGets != 0 {
		t.Fatalf("invalid legacy key reached KVGet: calls=%d err=%v", badKey.kvGets, err)
	}

	badRevision := &preflightLegacyStore{
		Store:     NewMemoryStore(),
		revisions: []RevisionInfo{{ID: strings.Repeat("r", maxRevisionIDBytes+1)}},
	}
	if _, err := New(badRevision).ExportUser(ctx, "u"); err == nil || badRevision.revisionReads != 0 {
		t.Fatalf("invalid legacy revision reached RevisionItems: calls=%d err=%v", badRevision.revisionReads, err)
	}
}

func TestKVAndErrorsRemainCompatibleWithLegacyStore(t *testing.T) {
	ctx := context.Background()
	legacy := &legacyStoreView{Store: NewMemoryStore()}
	h := New(legacy, WithLimits(Limits{MaxKVKeysPerUser: 2}))
	defer h.Close()

	if err := h.KVSet(ctx, "u", "one", json.RawMessage(`1`)); err != nil {
		t.Fatal(err)
	}
	if err := h.ReportErrors(ctx, "u", []PageError{{Message: "legacy"}}); err != nil {
		t.Fatal(err)
	}
	got, err := h.PageErrors(ctx, "u")
	if err != nil || len(got) != 1 || got[0].Message != "legacy" {
		t.Fatalf("legacy ReportErrors roundtrip: %+v err=%v", got, err)
	}
}

func TestPublicKVRejectsReservedKeysAtServiceBoundary(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	if err := store.KVSet(ctx, "u", errorsKVKey, json.RawMessage(`[{"message":"internal"}]`)); err != nil {
		t.Fatal(err)
	}
	h := New(store)
	defer h.Close()

	for _, key := range []string{errorsKVKey, pagesKVKey, reservedKVPrefix + ".custom"} {
		if _, err := h.KVGet(ctx, "u", key); !errors.Is(err, ErrReservedKVKey) {
			t.Fatalf("KVGet(%q) did not reject reserved key: %v", key, err)
		}
		if err := h.KVSet(ctx, "u", key, json.RawMessage(`true`)); !errors.Is(err, ErrReservedKVKey) {
			t.Fatalf("KVSet(%q) did not reject reserved key: %v", key, err)
		}
		if err := h.KVDelete(ctx, "u", key); !errors.Is(err, ErrReservedKVKey) {
			t.Fatalf("KVDelete(%q) did not reject reserved key: %v", key, err)
		}
	}
	raw, err := store.KVGet(ctx, "u", errorsKVKey)
	if err != nil || !strings.Contains(string(raw), "internal") {
		t.Fatalf("reserved value changed through public KV API: %s, %v", raw, err)
	}
	if err := h.KVSet(ctx, "u", "public", json.RawMessage(`1`)); err != nil {
		t.Fatalf("ordinary KV key was rejected: %v", err)
	}
}

type captureBroadcaster struct {
	mu     sync.Mutex
	events []Event
}

type captureVersionedBroadcaster struct {
	*captureBroadcaster
	version uint64
}

func (b *captureVersionedBroadcaster) CurrentVersion(string) uint64 { return b.version }

func (b *captureBroadcaster) snapshot() []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]Event(nil), b.events...)
}

func (b *captureBroadcaster) reset() {
	b.mu.Lock()
	b.events = nil
	b.mu.Unlock()
}

func (b *captureBroadcaster) Publish(_ string, ev Event) {
	b.mu.Lock()
	b.events = append(b.events, ev)
	b.mu.Unlock()
}

func (*captureBroadcaster) Subscribe(string) (<-chan Event, func()) {
	ch := make(chan Event)
	return ch, func() { close(ch) }
}

func TestExternalPublishReorderingCannotCarryStalePayload(t *testing.T) {
	b := &captureBroadcaster{}
	h := New(NewMemoryStore(), WithBroadcaster(b))
	defer h.Close()

	// 模拟跨副本反序：较新的 delete 先发布，较旧的 put 后发布。
	// 外部通道若保留 put 载荷，客户端就会在最后恢复已删除内容。
	h.publish("u", Event{Type: EventItems, Op: EventOpDelete, IDs: []string{"old"}})
	h.publish("u", Event{Type: EventItems, Op: EventOpPut, Items: []Item{{ID: "old", Type: ItemHTML, Content: "stale"}}})

	b.mu.Lock()
	events := append([]Event(nil), b.events...)
	b.mu.Unlock()
	if len(events) != 2 {
		t.Fatalf("captured %d events, want 2", len(events))
	}
	for i, ev := range events {
		if ev.Type != EventResync || ev.Reason != ResyncExternal {
			t.Fatalf("external event %d retained incremental payload: %+v", i, ev)
		}
		if ev.Op != "" || len(ev.Items) != 0 || len(ev.IDs) != 0 {
			t.Fatalf("external resync %d leaked stale payload: %+v", i, ev)
		}
	}
}

func TestDefaultHubKeepsLowLatencyIncrementalPayload(t *testing.T) {
	h := New(NewMemoryStore())
	defer h.Close()
	ch, cancel := h.opts.broadcaster.Subscribe("u")
	defer cancel()

	want := Item{ID: "fresh", Type: ItemHTML, Content: "<p>fresh</p>"}
	h.publish("u", Event{Type: EventItems, Op: EventOpPut, Items: []Item{want}})
	ev := <-ch
	if ev.Type != EventItems || ev.Op != EventOpPut || len(ev.Items) != 1 || ev.Items[0].ID != want.ID {
		t.Fatalf("default hub lost incremental fast path: %+v", ev)
	}
}

func TestBusinessCommitCoalescesOnlyForSafeExternalBroadcaster(t *testing.T) {
	ctx := context.Background()

	external := &captureBroadcaster{}
	externalHandler := New(NewMemoryStore(), WithBroadcaster(external))
	defer externalHandler.Close()
	if err := externalHandler.PurgeUser(ctx, "u"); err != nil {
		t.Fatal(err)
	}
	events := external.snapshot()
	if len(events) != 1 || events[0].Type != EventResync || events[0].Reason != ResyncExternal {
		t.Fatalf("one external commit must emit one authoritative resync: %+v", events)
	}
	if got := externalHandler.Stats()["events_published"]; got != 1 {
		t.Fatalf("external delivered-event metric = %d, want 1", got)
	}

	ordered := &captureBroadcaster{}
	orderedHandler := New(NewMemoryStore(), WithOrderedBroadcaster(ordered))
	defer orderedHandler.Close()
	if err := orderedHandler.PurgeUser(ctx, "u"); err != nil {
		t.Fatal(err)
	}
	events = ordered.snapshot()
	if len(events) != 3 || events[0].Type != EventItems || events[0].Op != EventOpPurge ||
		events[1].Type != EventPages || events[2].Type != EventTemplates || events[2].Op != EventOpPurge {
		t.Fatalf("explicit ordered broadcaster lost incremental payloads: %+v", events)
	}
	if got := orderedHandler.Stats()["events_published"]; got != 3 {
		t.Fatalf("ordered delivered-event metric = %d, want 3", got)
	}
}

func TestWithOrderedBroadcasterPreservesVersionCapability(t *testing.T) {
	b := &captureVersionedBroadcaster{captureBroadcaster: &captureBroadcaster{}, version: 42}
	h := New(NewMemoryStore(), WithOrderedBroadcaster(b))
	defer h.Close()
	if _, ok := h.opts.broadcaster.(VersionedBroadcaster); !ok {
		t.Fatal("WithOrderedBroadcaster hid VersionedBroadcaster capability")
	}
	if ev := h.resyncEvent("u", ResyncSubscribe); ev.Version != 42 {
		t.Fatalf("wrapped CurrentVersion = %d, want 42", ev.Version)
	}
}

func TestTemplateCommitAlsoCoalescesExternalNotifications(t *testing.T) {
	ctx := context.Background()
	b := &captureBroadcaster{}
	h := New(NewMemoryStore(), WithBroadcaster(b))
	defer h.Close()
	if err := h.SetPage(ctx, "u", Page{Name: "dash", Title: "Dashboard"}); err != nil {
		t.Fatal(err)
	}
	current, err := h.GetCurrentTemplate(ctx, "u", "dash")
	if err != nil {
		t.Fatal(err)
	}
	input := editableTemplate(current.Template.ID)
	input.Name = current.Template.Name
	b.reset()
	saved, err := h.PutCurrentTemplate(ctx, "u", "dash", input, 0, "api")
	if err != nil {
		t.Fatal(err)
	}
	if events := b.snapshot(); len(events) != 1 || events[0].Type != EventResync {
		t.Fatalf("template+page binding commit emitted duplicate external refreshes: %+v", events)
	}
	b.reset()
	if err := h.DeleteTemplate(ctx, "u", saved.ID, saved.Revision, "api"); err != nil {
		t.Fatal(err)
	}
	if events := b.snapshot(); len(events) != 1 || events[0].Type != EventResync {
		t.Fatalf("template delete+unbind commit emitted duplicate external refreshes: %+v", events)
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

func TestPublishPageReplacesOnlyTargetPageInOneRevision(t *testing.T) {
	ctx := context.Background()
	h := New(NewMemoryStore())
	t.Cleanup(func() { _ = h.Close() })

	if err := h.SetPage(ctx, "u", Page{Name: "report", Title: "Old report", Order: 20}); err != nil {
		t.Fatal(err)
	}
	if err := h.SetPage(ctx, "u", Page{Name: "other", Title: "Other", Order: 10}); err != nil {
		t.Fatal(err)
	}
	if err := h.PutItems(ctx, "u", []Item{
		{ID: "global", Type: ItemCSS, Content: "body{}"},
		{ID: "old", Type: ItemHTML, Page: "report", Content: "old"},
		{ID: "keep", Type: ItemHTML, Page: "other", Content: "keep"},
	}, "api", "seed"); err != nil {
		t.Fatal(err)
	}
	beforeRevisions, err := h.ListRevisions(ctx, "u")
	if err != nil {
		t.Fatal(err)
	}

	page := Page{Name: "report", Title: "Weekly report", Order: 30}
	items := []Item{
		{ID: "report-style", Type: ItemCSS, Page: "report", Content: ".report{}"},
		{ID: "report-body", Type: ItemHTML, Page: "report", Content: "<main>ready</main>"},
	}
	if err := h.PublishPage(ctx, "u", page, items, "agent", "publish report"); err != nil {
		t.Fatal(err)
	}

	pages, err := h.ListPages(ctx, "u")
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 || pages[0].Name != "other" || pages[1] != page {
		t.Fatalf("pages = %+v", pages)
	}
	gotItems, err := h.ListItems(ctx, "u")
	if err != nil {
		t.Fatal(err)
	}
	gotByID := make(map[string]Item, len(gotItems))
	for _, item := range gotItems {
		gotByID[item.ID] = item
	}
	for _, id := range []string{"global", "keep", "report-style", "report-body"} {
		if _, ok := gotByID[id]; !ok {
			t.Fatalf("published state is missing %q: %+v", id, gotItems)
		}
	}
	if _, stale := gotByID["old"]; stale {
		t.Fatalf("stale page item survived publication: %+v", gotItems)
	}
	revisions, err := h.ListRevisions(ctx, "u")
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != len(beforeRevisions)+1 || revisions[0].Note != "publish report" {
		t.Fatalf("publication revisions = %+v, before=%d", revisions, len(beforeRevisions))
	}

	if err := h.PublishPage(ctx, "u", page, items, "agent", "same state"); err != nil {
		t.Fatal(err)
	}
	afterNoop, err := h.ListRevisions(ctx, "u")
	if err != nil || len(afterNoop) != len(revisions) {
		t.Fatalf("no-op publication created a revision: before=%d after=%d err=%v", len(revisions), len(afterNoop), err)
	}
}

func TestPublishPageRejectsCrossPageIDCollisionWithoutPartialState(t *testing.T) {
	ctx := context.Background()
	store := &rejectingStateStore{MemoryStore: NewMemoryStore()}
	h := New(store)
	t.Cleanup(func() { _ = h.Close() })
	if err := h.SetPage(ctx, "u", Page{Name: "other", Title: "Other"}); err != nil {
		t.Fatal(err)
	}
	if err := h.PutItems(ctx, "u", []Item{{ID: "shared", Type: ItemHTML, Page: "other", Content: "keep"}}, "api", "seed"); err != nil {
		t.Fatal(err)
	}
	before, err := store.UserState(ctx, "u")
	if err != nil {
		t.Fatal(err)
	}
	err = h.PublishPage(ctx, "u", Page{Name: "report", Title: "Report"}, []Item{
		{ID: "shared", Type: ItemHTML, Page: "report", Content: "collision"},
	}, "agent", "collision")
	if err == nil || !strings.Contains(err.Error(), "outside page") {
		t.Fatalf("collision error = %v", err)
	}
	afterCollision, _ := store.UserState(ctx, "u")
	if !reflect.DeepEqual(before, afterCollision) {
		t.Fatalf("collision changed state\nbefore=%+v\nafter=%+v", before, afterCollision)
	}

	store.reject = true
	err = h.PublishPage(ctx, "u", Page{Name: "report", Title: "Report"}, []Item{
		{ID: "report", Type: ItemHTML, Page: "report", Content: "new"},
	}, "agent", "injected failure")
	if err == nil {
		t.Fatal("expected injected atomic replace failure")
	}
	afterFailure, _ := store.UserState(ctx, "u")
	if !reflect.DeepEqual(before, afterFailure) {
		t.Fatalf("failed publication changed state\nbefore=%+v\nafter=%+v", before, afterFailure)
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

// 嵌入会把 ReplaceUserStateVersioned 一并提升出来, 包装 Store 必须两个替换
// 方法都拦截, 否则 Handler 会优先走版本化路径绕过注入(见 store.go 接口文档)。
func (s *rejectingStateStore) ReplaceUserStateVersioned(ctx context.Context, user string, state UserState, expected uint64) error {
	if s.reject {
		return errors.New("injected atomic replace failure")
	}
	return s.MemoryStore.ReplaceUserStateVersioned(ctx, user, state, expected)
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
	failDelete bool
	failErase  bool
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

func (s *faultLegacyStore) KVDelete(ctx context.Context, user, key string) error {
	if s.failDelete {
		return errors.New("injected kv delete failure")
	}
	return s.Store.KVDelete(ctx, user, key)
}

func (s *faultLegacyStore) EraseUser(ctx context.Context, user string) error {
	if s.failErase {
		return errors.New("injected erase failure")
	}
	return s.Store.EraseUser(ctx, user)
}

func (s *faultLegacyStore) RevisionItems(ctx context.Context, user, id string) ([]Item, error) {
	if s.revReadErr {
		return nil, errors.New("injected revision read failure")
	}
	return s.Store.RevisionItems(ctx, user, id)
}

func TestMutationMetricsCountOnlySuccessfulOperations(t *testing.T) {
	ctx := context.Background()
	legacy := &faultLegacyStore{Store: NewMemoryStore()}
	h := New(legacy)
	defer h.Close()

	if err := h.ReportErrors(ctx, "u", []PageError{{Message: "one"}, {Message: "two"}}); err != nil {
		t.Fatal(err)
	}
	if got := h.Stats()["errors_reported"]; got != 2 {
		t.Fatalf("errors_reported = %d, want 2", got)
	}
	if got := h.Stats()["writes"]; got != 1 {
		t.Fatalf("ReportErrors writes = %d, want 1", got)
	}
	if err := h.KVSet(ctx, "u", "delete-me", json.RawMessage(`1`)); err != nil {
		t.Fatal(err)
	}
	writesBeforeDelete := h.Stats()["writes"]
	if err := h.KVDelete(ctx, "u", "delete-me"); err != nil {
		t.Fatal(err)
	}
	if got := h.Stats()["writes"]; got != writesBeforeDelete+1 {
		t.Fatalf("successful KVDelete writes = %d, want %d", got, writesBeforeDelete+1)
	}
	writesBeforePurge := h.Stats()["writes"]
	if err := h.PurgeUser(ctx, "u"); err != nil {
		t.Fatal(err)
	}
	if got := h.Stats()["writes"]; got != writesBeforePurge+1 {
		t.Fatalf("successful PurgeUser writes = %d, want %d", got, writesBeforePurge+1)
	}

	legacy.failKVOnce = errorsKVKey
	writesBeforeFailures := h.Stats()["writes"]
	if err := h.ReportErrors(ctx, "u", []PageError{{Message: "failed"}}); err == nil {
		t.Fatal("expected ReportErrors failure")
	}
	if got := h.Stats()["errors_reported"]; got != 2 {
		t.Fatalf("failed ReportErrors changed metric to %d", got)
	}
	if got := h.Stats()["writes"]; got != writesBeforeFailures {
		t.Fatalf("failed ReportErrors changed writes metric: %d -> %d", writesBeforeFailures, got)
	}
	legacy.failDelete = true
	if err := h.KVDelete(ctx, "u", "missing"); err == nil {
		t.Fatal("expected KVDelete failure")
	}
	writesAfterDeleteFailure := h.Stats()["writes"]
	legacy.failErase = true
	if err := h.PurgeUser(ctx, "u"); err == nil {
		t.Fatal("expected PurgeUser failure")
	}
	if got := h.Stats()["writes"]; got != writesAfterDeleteFailure {
		t.Fatalf("failed mutations changed writes metric: %d -> %d", writesAfterDeleteFailure, got)
	}
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
