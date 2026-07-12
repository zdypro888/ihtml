package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/zdypro888/ihtml"
)

func TestNewRejectsNilDB(t *testing.T) {
	if _, err := New(nil, Postgres); !errors.Is(err, ErrNilDB) {
		t.Fatalf("New(nil) error = %v", err)
	}
}

// TestPostgres 需要真实 postgres: 设置 IHTML_TEST_PG_DSN 才会运行。
//
//	IHTML_TEST_PG_DSN='postgres://user:pass@localhost:5432/ihtml_test?sslmode=disable' go test ./sqlstore
func TestPostgres(t *testing.T) {
	dsn := os.Getenv("IHTML_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("IHTML_TEST_PG_DSN not set")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := New(db, Postgres)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	user := fmt.Sprintf("t-%d", time.Now().UnixNano()) // 每次跑用独立用户避免残留干扰

	// Items
	items, err := store.Items(ctx, user)
	if err != nil || len(items) != 0 {
		t.Fatalf("empty items: %v %v", items, err)
	}
	set := []ihtml.Item{
		{ID: "a", Type: ihtml.ItemHTML, Title: "甲", Page: "dash", Order: 1, Content: "<div/>", Meta: map[string]string{"slot": "header"}, UpdatedAt: time.Now()},
		{ID: "b", Type: ihtml.ItemJS, Order: 2, Content: "1+1", UpdatedAt: time.Now()},
	}
	if err := store.ReplaceItems(ctx, user, set); err != nil {
		t.Fatal(err)
	}
	items, err = store.Items(ctx, user)
	if err != nil || len(items) != 2 {
		t.Fatalf("items after replace: %v %v", items, err)
	}
	for _, it := range items {
		if it.ID == "a" && (it.Meta["slot"] != "header" || it.Page != "dash" || it.Title != "甲") {
			t.Fatalf("item a roundtrip: %+v", it)
		}
	}

	// KV
	if _, err := store.KVGet(ctx, user, "none"); !errors.Is(err, ihtml.ErrNotFound) {
		t.Fatalf("missing kv: %v", err)
	}
	if err := store.KVSet(ctx, user, "theme", json.RawMessage(`{"mode":"dark"}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.KVSet(ctx, user, "theme", json.RawMessage(`{"mode":"light"}`)); err != nil {
		t.Fatal(err) // 覆盖写
	}
	v, err := store.KVGet(ctx, user, "theme")
	if err != nil || string(v) != `{"mode":"light"}` {
		t.Fatalf("kv get: %s %v", v, err)
	}
	if err := store.KVDelete(ctx, user, "theme"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.KVGet(ctx, user, "theme"); !errors.Is(err, ihtml.ErrNotFound) {
		t.Fatalf("deleted kv: %v", err)
	}

	// Template catalog + published runtime document use the same SQL transaction
	// boundary as Items/Pages/Revisions.
	handler := ihtml.New(store)
	template, err := handler.PutCurrentTemplate(ctx, user, "", ihtml.Template{
		ID: "tpl-root", Name: "Root",
		Draft: ihtml.TemplateVersion{
			RootID: "root", Nodes: []ihtml.TemplateNode{{ID: "root", Kind: ihtml.NodeCanvas}},
			Slots: []ihtml.Slot{}, Placements: []ihtml.Placement{}, Breakpoints: []ihtml.Breakpoint{},
		},
	}, 0, "api")
	if err != nil {
		t.Fatal(err)
	}
	template, err = handler.PublishTemplate(ctx, user, template.ID, template.Revision, "api")
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := ihtml.New(store).GetCurrentTemplate(ctx, user, "")
	if err != nil || !reloaded.Persisted || reloaded.Template.Published == nil ||
		reloaded.Template.Published.RootID != "root" || reloaded.Template.Revision != template.Revision {
		t.Fatalf("published template SQL roundtrip: %+v %v", reloaded, err)
	}

	// Revisions + 裁剪
	for i := range 5 {
		rev := ihtml.Revision{
			ID:        fmt.Sprintf("r%d-%d", time.Now().UnixNano(), i),
			Note:      "n",
			By:        "api",
			CreatedAt: time.Now().Add(time.Duration(i) * time.Millisecond),
			Items:     set,
		}
		if err := store.AddRevision(ctx, user, rev, 3); err != nil {
			t.Fatal(err)
		}
	}
	revs, err := store.Revisions(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	if len(revs) != 3 {
		t.Fatalf("revisions pruned to 3, got %d", len(revs))
	}
	got, err := store.RevisionItems(ctx, user, revs[0].ID)
	if err != nil || len(got) != 2 {
		t.Fatalf("revision items: %v %v", got, err)
	}
	if _, err := store.RevisionItems(ctx, user, "nope"); !errors.Is(err, ihtml.ErrNotFound) {
		t.Fatalf("missing revision: %v", err)
	}
}

func TestRevisionPayloadBackwardCompatibility(t *testing.T) {
	legacyItems := []ihtml.Item{{ID: "old", Type: ihtml.ItemHTML, Content: "legacy"}}
	legacyRaw, err := json.Marshal(legacyItems)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := decodeRevisionPayload(string(legacyRaw))
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Version != 0 || len(legacy.Items) != 1 || legacy.Items[0].ID != "old" || legacy.Pages != nil {
		t.Fatalf("legacy payload: %+v", legacy)
	}

	version := ihtml.TemplateVersion{
		Version: 1, RootID: "root",
		Nodes:       []ihtml.TemplateNode{{ID: "root", Kind: ihtml.NodeCanvas}},
		Slots:       []ihtml.Slot{{ID: "main", NodeID: "root", Accepts: []ihtml.ItemType{ihtml.ItemHTML}}},
		Placements:  []ihtml.Placement{{ItemID: "new", SlotID: "main", Mode: ihtml.LayoutFlow, ColumnSpan: 12}},
		Breakpoints: []ihtml.Breakpoint{{ID: "desktop", Columns: 12, Gap: 16}},
	}
	rev := ihtml.Revision{
		Version: ihtml.CurrentDataVersion,
		ID:      "r2",
		Items:   []ihtml.Item{{ID: "new", Type: ihtml.ItemCSS, Content: ".x{}"}},
		Pages:   []ihtml.Page{{Name: "dash", Title: "Dashboard"}},
		Templates: []ihtml.Template{{
			ID: "tpl-root", Name: "Root", Revision: 1,
			Draft: version, Published: &version,
		}},
		RootTemplateID:        "tpl-root",
		TemplateRevisionClock: 1,
	}
	raw, err := encodeRevision(rev)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeRevisionPayload(raw)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Version != ihtml.CurrentDataVersion || len(decoded.Items) != 1 || len(decoded.Pages) != 1 || decoded.Pages[0].Name != "dash" ||
		len(decoded.Templates) != 1 || decoded.Templates[0].Published == nil ||
		len(decoded.Templates[0].Published.Placements) != 1 || decoded.Templates[0].Published.Placements[0].SlotID != "main" ||
		decoded.RootTemplateID != "tpl-root" || decoded.TemplateRevisionClock != 1 {
		t.Fatalf("v3 payload: %+v", decoded)
	}
}
