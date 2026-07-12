package ihtml

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sync"
	"testing"
)

func editableTemplate(id string) Template {
	return Template{
		ID: id, Name: "编辑模板",
		Draft: TemplateVersion{
			RootID:      "root",
			Nodes:       []TemplateNode{{ID: "root", Kind: NodeGrid}},
			Slots:       []Slot{{ID: "main", NodeID: "root", Name: "主内容", Accepts: []ItemType{ItemHTML}}},
			Placements:  []Placement{{ItemID: "card", SlotID: "main", Mode: LayoutFlow, ColumnSpan: 6, RowSpan: 1}},
			Breakpoints: []Breakpoint{{ID: "desktop", Columns: 12, Gap: 16}},
		},
	}
}

func TestCurrentTemplateEmptyCASAndPageBinding(t *testing.T) {
	ctx := context.Background()
	h := New(NewMemoryStore())

	root, err := h.GetCurrentTemplate(ctx, "u", "")
	if err != nil {
		t.Fatal(err)
	}
	if root.Persisted || root.Template.ID != "tpl-root" || root.Template.Revision != 0 ||
		len(root.Template.Draft.Nodes) != 0 {
		t.Fatalf("unexpected virtual empty root template: %+v", root)
	}
	saved, err := h.PutCurrentTemplate(ctx, "u", "", root.Template, 0, "api")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Revision != 1 || saved.Draft.Version != 1 {
		t.Fatalf("server did not initialize versions: %+v", saved)
	}
	if _, err := h.PutCurrentTemplate(ctx, "u", "", saved, 0, "api"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale CAS should conflict, got %v", err)
	}

	if err := h.SetPage(ctx, "u", Page{Name: "dash", Title: "Dashboard"}); err != nil {
		t.Fatal(err)
	}
	pageView, err := h.GetCurrentTemplate(ctx, "u", "dash")
	if err != nil {
		t.Fatal(err)
	}
	pageTemplate := editableTemplate(pageView.Template.ID)
	pageTemplate.Name = pageView.Template.Name
	savedPage, err := h.PutCurrentTemplate(ctx, "u", "dash", pageTemplate, 0, "api")
	if err != nil {
		t.Fatal(err)
	}
	pages, err := h.ListPages(ctx, "u")
	if err != nil || len(pages) != 1 || pages[0].TemplateID != savedPage.ID {
		t.Fatalf("page did not receive stable template binding: %+v %v", pages, err)
	}
	if err := h.PutItems(ctx, "u", []Item{{ID: "dash-card", Type: ItemHTML, Page: "dash", Content: "<article/>"}}, "api", "seed page"); err != nil {
		t.Fatal(err)
	}
	if err := h.DeleteTemplate(ctx, "u", savedPage.ID, savedPage.Revision, "api"); err != nil {
		t.Fatal(err)
	}
	pages, err = h.ListPages(ctx, "u")
	items, itemsErr := h.ListItems(ctx, "u")
	if err != nil || itemsErr != nil || len(pages) != 1 || pages[0].TemplateID != "" || len(items) != 1 || items[0].ID != "dash-card" {
		t.Fatalf("template delete must clear page binding and preserve Items: pages=%+v items=%+v errors=%v/%v", pages, items, err, itemsErr)
	}
}

func TestDeleteTemplateSlotUnassignsPlacementAndPreservesItem(t *testing.T) {
	ctx := context.Background()
	h := New(NewMemoryStore())
	if err := h.PutItems(ctx, "u", []Item{{ID: "card", Type: ItemHTML, Content: "<article/>"}}, "api", "seed"); err != nil {
		t.Fatal(err)
	}
	template, err := h.PutCurrentTemplate(ctx, "u", "", editableTemplate("tpl-root"), 0, "api")
	if err != nil {
		t.Fatal(err)
	}
	updated, err := h.DeleteTemplateSlot(ctx, "u", template.ID, "main", template.Revision, "api")
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Draft.Slots) != 0 || len(updated.Draft.Placements) != 1 ||
		updated.Draft.Placements[0].SlotID != "" || !updated.Draft.Placements[0].Unassigned {
		t.Fatalf("slot binding was not moved to unassigned: %+v", updated.Draft)
	}
	items, err := h.ListItems(ctx, "u")
	if err != nil || len(items) != 1 || items[0].ID != "card" {
		t.Fatalf("slot deletion mutated Items: %+v %v", items, err)
	}
	published, err := h.PublishTemplate(ctx, "u", updated.ID, updated.Revision, "api")
	if err != nil {
		t.Fatal(err)
	}
	if published.Published == nil || published.Published.Version != published.Draft.Version || published.Revision != updated.Revision+1 {
		t.Fatalf("publish did not copy draft: %+v", published)
	}
	if err := h.DeleteTemplate(ctx, "u", published.ID, published.Revision, "api"); err != nil {
		t.Fatal(err)
	}
	items, err = h.ListItems(ctx, "u")
	if err != nil || len(items) != 1 || items[0].ID != "card" {
		t.Fatalf("template deletion mutated Items: %+v %v", items, err)
	}
	afterDelete, err := h.GetCurrentTemplate(ctx, "u", "")
	if err != nil || afterDelete.Persisted {
		t.Fatalf("deleted root binding did not fall back to empty template: %+v %v", afterDelete, err)
	}
}

func TestLegacyLayoutIsDerivedWithoutRewritingLegacyState(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	h := New(store)
	items := []Item{{ID: "card", Type: ItemHTML, Content: "<article/>", Meta: map[string]string{"slot": "sidebar"}}}
	if err := store.ReplaceItems(ctx, "u", items); err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"order":["card"],"items":{"card":{"w":4,"h":2}}}`)
	if err := store.KVSet(ctx, "u", "layout.root", raw); err != nil {
		t.Fatal(err)
	}

	current, err := h.GetCurrentTemplate(ctx, "u", "")
	if err != nil {
		t.Fatal(err)
	}
	if !current.Migrated || current.Persisted || len(current.Template.Draft.Placements) != 1 ||
		current.Template.Draft.Placements[0].ColumnSpan != 4 || current.Template.Draft.Placements[0].RowSpan != 2 ||
		current.Template.Draft.Placements[0].SlotID != "sidebar" || current.Template.Draft.Slots[0].ID != "sidebar" {
		t.Fatalf("legacy layout was not derived: %+v", current)
	}
	if _, err := store.KVGet(ctx, "u", templatesKVKey); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GET must not persist migration state, got %v", err)
	}
	stillLegacy, err := store.KVGet(ctx, "u", "layout.root")
	if err != nil || string(stillLegacy) != string(raw) {
		t.Fatalf("legacy value was rewritten: %s %v", stillLegacy, err)
	}
}

func TestLegacyMetaSlotDerivesReusableStructureWithoutItemPlacements(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	if err := store.ReplaceItems(ctx, "u", []Item{
		{ID: "a", Type: ItemHTML, Content: "<a/>", Meta: map[string]string{"slot": "main"}},
		{ID: "b", Type: ItemHTML, Content: "<b/>", Meta: map[string]string{"slot": "sidebar"}},
	}); err != nil {
		t.Fatal(err)
	}
	current, err := New(store).GetCurrentTemplate(ctx, "u", "")
	if err != nil {
		t.Fatal(err)
	}
	if !current.Migrated || len(current.Template.Draft.Slots) != 2 || len(current.Template.Draft.Placements) != 0 {
		t.Fatalf("meta.slot migration leaked business Item IDs into reusable template: %+v", current.Template.Draft)
	}
	ids := map[string]bool{}
	for _, slot := range current.Template.Draft.Slots {
		ids[slot.ID] = true
	}
	if !ids["main"] || !ids["sidebar"] {
		t.Fatalf("canonical meta.slot ids were not preserved: %+v", current.Template.Draft.Slots)
	}
}

func TestTemplateExportImportAndRevisionRollback(t *testing.T) {
	ctx := context.Background()
	h := New(NewMemoryStore())
	original, err := h.PutCurrentTemplate(ctx, "u", "", editableTemplate("tpl-root"), 0, "api")
	if err != nil {
		t.Fatal(err)
	}
	withoutSlot, err := h.DeleteTemplateSlot(ctx, "u", original.ID, "main", original.Revision, "api")
	if err != nil {
		t.Fatal(err)
	}
	revisions, err := h.ListRevisions(ctx, "u")
	if err != nil || len(revisions) < 1 {
		t.Fatalf("revisions: %+v %v", revisions, err)
	}
	if err := h.Rollback(ctx, "u", revisions[0].ID, "api"); err != nil {
		t.Fatal(err)
	}
	restored, err := h.GetTemplate(ctx, "u", original.ID)
	if err != nil || len(restored.Draft.Slots) != 1 {
		t.Fatalf("v3 rollback did not restore template: %+v %v", restored, err)
	}
	if restored.Revision <= withoutSlot.Revision {
		t.Fatalf("rollback must allocate a fresh CAS revision: before=%d deleted=%d restored=%d", original.Revision, withoutSlot.Revision, restored.Revision)
	}
	if _, err := h.PutTemplate(ctx, "u", withoutSlot, withoutSlot.Revision, "api"); !errors.Is(err, ErrConflict) {
		t.Fatalf("pre-rollback client must not pass CAS after revision rebase: %v", err)
	}

	exported, err := h.ExportUser(ctx, "u")
	if err != nil {
		t.Fatal(err)
	}
	if exported.Version != CurrentDataVersion || exported.RootTemplateID != original.ID || len(exported.Templates) != 1 ||
		exported.TemplateRevisionClock < exported.Templates[0].Revision {
		t.Fatalf("template missing from export: %+v", exported)
	}
	other := New(NewMemoryStore())
	if err := other.ImportUser(ctx, "v", exported); err != nil {
		t.Fatal(err)
	}
	imported, err := other.GetCurrentTemplate(ctx, "v", "")
	if err != nil || !imported.Persisted || imported.Template.ID != original.ID {
		t.Fatalf("template import failed: %+v %v", imported, err)
	}
}

func TestTemplateHTTPContractAndConflict(t *testing.T) {
	srv, _ := newTestServer(t)
	base := srv.URL + "/api/templates/current?uid=u&page="
	status, out := doJSON(t, http.MethodGet, base, "")
	if status != http.StatusOK || out["persisted"] != false {
		t.Fatalf("GET current contract: %d %+v", status, out)
	}
	template := out["template"].(map[string]any)
	body, _ := json.Marshal(map[string]any{"page": "", "expected_revision": 0, "template": template})
	status, out = doJSON(t, http.MethodPut, base, string(body))
	if status != http.StatusOK || out["template"].(map[string]any)["revision"] != float64(1) {
		t.Fatalf("PUT current contract: %d %+v", status, out)
	}
	status, out = doJSON(t, http.MethodPut, base, string(body))
	if status != http.StatusConflict || out["current_revision"] != float64(1) || out["current"] == nil {
		t.Fatalf("CAS response contract: %d %+v", status, out)
	}
	wrongWrapper, _ := json.Marshal(map[string]any{
		"page": "", "expected_revision": 1,
		"template": map[string]any{"template": template, "persisted": false},
	})
	status, out = doJSON(t, http.MethodPut, base, string(wrongWrapper))
	if status != http.StatusBadRequest {
		t.Fatalf("outer current wrapper must be rejected, not saved: %d %+v", status, out)
	}
}

func TestTemplateEntityHTTPCRUD(t *testing.T) {
	srv, _ := newTestServer(t)
	base := srv.URL + "/api/templates"
	create := map[string]any{
		"expected_revision": 0,
		"template": map[string]any{
			"id": "library", "name": "Library",
			"draft": map[string]any{"version": 0, "nodes": []any{}, "slots": []any{}, "placements": []any{}, "breakpoints": []any{}},
		},
	}
	body, _ := json.Marshal(create)
	status, out := doJSON(t, http.MethodPost, base+"?uid=u", string(body))
	if status != http.StatusCreated {
		t.Fatalf("create template: %d %+v", status, out)
	}
	created := out["template"].(map[string]any)
	status, out = doJSON(t, http.MethodGet, base+"/library?uid=u", "")
	if status != http.StatusOK || out["template"].(map[string]any)["name"] != "Library" {
		t.Fatalf("get template: %d %+v", status, out)
	}
	created["name"] = "Renamed"
	update, _ := json.Marshal(map[string]any{"expected_revision": created["revision"], "template": created})
	status, out = doJSON(t, http.MethodPut, base+"/library?uid=u", string(update))
	if status != http.StatusOK || out["template"].(map[string]any)["name"] != "Renamed" {
		t.Fatalf("update template: %d %+v request=%s", status, out, update)
	}
	revision := out["template"].(map[string]any)["revision"]
	status, out = doJSON(t, http.MethodDelete, base+"/library?uid=u&expected_revision="+jsonNumber(revision), "")
	if status != http.StatusOK {
		t.Fatalf("delete template: %d %+v", status, out)
	}
	status, out = doJSON(t, http.MethodGet, base+"/library?uid=u", "")
	if status != http.StatusNotFound {
		t.Fatalf("deleted template remains: %d %+v", status, out)
	}
}

func TestUnboundReservedTemplateDoesNotBecomeCurrentImplicitly(t *testing.T) {
	ctx := context.Background()
	h := New(NewMemoryStore())
	unbound := Template{
		ID: "tpl-root", Name: "Unbound",
		Draft: TemplateVersion{Nodes: []TemplateNode{}, Slots: []Slot{}, Placements: []Placement{}, Breakpoints: []Breakpoint{}},
	}
	created, err := h.PutTemplate(ctx, "u", unbound, 0, "api")
	if err != nil {
		t.Fatal(err)
	}
	current, err := h.GetCurrentTemplate(ctx, "u", "")
	if err != nil {
		t.Fatal(err)
	}
	if current.Persisted || current.Template.ID == created.ID || current.Template.ID != "tpl-root-2" {
		t.Fatalf("unbound template became current implicitly: created=%+v current=%+v", created, current)
	}
	bound, err := h.PutCurrentTemplate(ctx, "u", "", current.Template, 0, "api")
	if err != nil {
		t.Fatal(err)
	}
	current, err = h.GetCurrentTemplate(ctx, "u", "")
	if err != nil || !current.Persisted || current.Template.ID != bound.ID {
		t.Fatalf("explicit current binding failed: %+v %v", current, err)
	}
}

func jsonNumber(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func TestTemplateMutationPublishesEvent(t *testing.T) {
	h := New(NewMemoryStore())
	ch, cancel := h.opts.broadcaster.Subscribe("u")
	defer cancel()
	saved, err := h.PutCurrentTemplate(context.Background(), "u", "", editableTemplate("tpl-root"), 0, "api")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-ch:
		if event.Type != "templates" || event.Op != "put" || len(event.Templates) != 1 || event.Templates[0].Revision != saved.Revision {
			t.Fatalf("unexpected template SSE event: %+v", event)
		}
	default:
		t.Fatal("template mutation did not publish an event")
	}
	published, err := h.PublishTemplate(context.Background(), "u", saved.ID, saved.Revision, "api")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-ch:
		if event.Type != "templates" || event.Op != "publish" || len(event.Templates) != 1 ||
			event.Templates[0].Published == nil || event.Templates[0].Revision != published.Revision {
			t.Fatalf("unexpected publish SSE event: %+v", event)
		}
	default:
		t.Fatal("template publish did not publish an event")
	}
	if err := h.DeleteTemplate(context.Background(), "u", saved.ID, published.Revision, "api"); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-ch:
		if event.Type != "templates" || event.Op != "delete" || len(event.IDs) != 1 || event.IDs[0] != saved.ID {
			t.Fatalf("unexpected delete SSE event: %+v", event)
		}
	default:
		t.Fatal("template deletion did not publish an event")
	}
}

func TestPublishedTemplateIsCompleteAndIsolatedFromDraft(t *testing.T) {
	ctx := context.Background()
	h := New(NewMemoryStore())
	if err := h.PutItems(ctx, "u", []Item{{ID: "card", Type: ItemHTML, Content: "<article/>"}}, "api", "seed"); err != nil {
		t.Fatal(err)
	}
	input := editableTemplate("tpl-root")
	input.Draft.Nodes[0].Props = map[string]json.RawMessage{
		"layouts": json.RawMessage(`{"desktop":{"mode":"free","gap":12,"padding":16}}`),
		"frames":  json.RawMessage(`{"desktop":{"x":10,"y":20,"w":640,"h":480}}`),
	}
	draft, err := h.PutCurrentTemplate(ctx, "u", "", input, 0, "api")
	if err != nil {
		t.Fatal(err)
	}
	published, err := h.PublishTemplate(ctx, "u", draft.ID, draft.Revision, "api")
	if err != nil {
		t.Fatal(err)
	}
	if published.Published == nil || published.Published.RootID != "root" || len(published.Published.Nodes) != 1 ||
		len(published.Published.Slots) != 1 || len(published.Published.Placements) != 1 || len(published.Published.Breakpoints) != 1 {
		t.Fatalf("published runtime document is incomplete: %+v", published.Published)
	}
	if len(published.Published.Nodes[0].Props["frames"]) == 0 || len(published.Published.Nodes[0].Props["layouts"]) == 0 {
		t.Fatalf("published runtime document lost free-layout properties: %+v", published.Published.Nodes[0].Props)
	}
	changed, err := h.DeleteTemplateSlot(ctx, "u", published.ID, "main", published.Revision, "api")
	if err != nil {
		t.Fatal(err)
	}
	if len(changed.Draft.Slots) != 0 || !changed.Draft.Placements[0].Unassigned {
		t.Fatalf("draft slot deletion did not orphan placement: %+v", changed.Draft)
	}
	if changed.Published == nil || len(changed.Published.Slots) != 1 || changed.Published.Placements[0].SlotID != "main" {
		t.Fatalf("draft mutation leaked into published runtime state: %+v", changed.Published)
	}
	items, err := h.ListItems(ctx, "u")
	if err != nil || len(items) != 1 || items[0].ID != "card" {
		t.Fatalf("draft mutation changed Item data: %+v %v", items, err)
	}
}

func TestReusableTemplateUsesItemMetaSlotWithoutBusinessPlacements(t *testing.T) {
	ctx := context.Background()
	h := New(NewMemoryStore())
	item := Item{ID: "business-card", Type: ItemHTML, Content: "<article/>", Meta: map[string]string{"slot": "main"}}
	if err := h.PutItems(ctx, "u", []Item{item}, "api", "seed"); err != nil {
		t.Fatal(err)
	}
	structure := Template{
		ID: "tpl-root", Name: "Reusable",
		Draft: TemplateVersion{
			RootID: "root", Nodes: []TemplateNode{{ID: "root", Kind: NodeCanvas}},
			Slots:      []Slot{{ID: "main", NodeID: "root", Accepts: []ItemType{ItemHTML}}},
			Placements: []Placement{}, Breakpoints: []Breakpoint{{ID: "desktop", Columns: 12}},
		},
	}
	created, err := h.PutCurrentTemplate(ctx, "u", "", structure, 0, "api")
	if err != nil {
		t.Fatal(err)
	}
	published, err := h.PublishTemplate(ctx, "u", created.ID, created.Revision, "api")
	if err != nil {
		t.Fatal(err)
	}
	if published.Published == nil || len(published.Published.Placements) != 0 {
		t.Fatalf("reusable template acquired business Item bindings: %+v", published.Published)
	}
	withoutSlot, err := h.DeleteTemplateSlot(ctx, "u", published.ID, "main", published.Revision, "api")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.PublishTemplate(ctx, "u", withoutSlot.ID, withoutSlot.Revision, "api"); err != nil {
		t.Fatal(err)
	}
	items, err := h.ListItems(ctx, "u")
	if err != nil || len(items) != 1 || items[0].ID != item.ID || items[0].Content != item.Content || items[0].Meta["slot"] != "main" {
		t.Fatalf("structural template edit rewrote business Item binding/content: %+v %v", items, err)
	}
}

func TestTemplatePersistenceAcrossBuiltInStores(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		store := NewMemoryStore()
		templatePersistenceRoundTrip(t, New(store), New(store))
	})
	t.Run("file", func(t *testing.T) {
		dir := t.TempDir()
		first, err := NewFileStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		second, err := NewFileStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		templatePersistenceRoundTrip(t, New(first), New(second))
	})
}

func templatePersistenceRoundTrip(t *testing.T, writer, reader *Handler) {
	t.Helper()
	ctx := context.Background()
	created, err := writer.PutCurrentTemplate(ctx, "persist", "", editableTemplate("tpl-root"), 0, "api")
	if err != nil {
		t.Fatal(err)
	}
	published, err := writer.PublishTemplate(ctx, "persist", created.ID, created.Revision, "api")
	if err != nil {
		t.Fatal(err)
	}
	current, err := reader.GetCurrentTemplate(ctx, "persist", "")
	if err != nil || !current.Persisted || current.Template.Published == nil ||
		current.Template.Published.RootID != "root" || current.Template.Revision != published.Revision {
		t.Fatalf("template persistence roundtrip failed: %+v %v", current, err)
	}
	revisions, err := reader.ListRevisions(ctx, "persist")
	if err != nil || len(revisions) != 2 || revisions[0].TemplateCount != 1 {
		t.Fatalf("template revisions did not persist: %+v %v", revisions, err)
	}
}

func TestTemplateGeometryValidationRejectsHostileValues(t *testing.T) {
	base := editableTemplate("tpl-root")
	tests := []struct {
		name   string
		mutate func(*Template)
	}{
		{"nan coordinate", func(t *Template) { t.Draft.Placements[0].X = math.NaN() }},
		{"infinite size", func(t *Template) { t.Draft.Placements[0].Width = math.Inf(1) }},
		{"huge coordinate", func(t *Template) { t.Draft.Placements[0].Y = maxTemplateCoordinate + 1 }},
		{"huge z-index", func(t *Template) { t.Draft.Placements[0].ZIndex = maxTemplateZIndex + 1 }},
		{"huge grid line", func(t *Template) { t.Draft.Placements[0].Row = maxTemplateGridLine + 1 }},
		{"span exceeds breakpoint", func(t *Template) {
			t.Draft.Placements[0].Breakpoint = "desktop"
			t.Draft.Placements[0].ColumnSpan = 13
		}},
		{"nan gap", func(t *Template) { t.Draft.Breakpoints[0].Gap = math.NaN() }},
		{"huge breakpoint", func(t *Template) { t.Draft.Breakpoints[0].MinWidth = maxBreakpointWidth + 1 }},
		{"huge node order", func(t *Template) { t.Draft.Nodes[0].Order = maxTemplateOrder + 1 }},
		{"huge free frame", func(t *Template) {
			t.Draft.Nodes[0].Props = map[string]json.RawMessage{
				"frames": json.RawMessage(`{"desktop":{"x":1e308,"y":0,"w":240,"h":120}}`),
			}
		}},
		{"huge node layout gap", func(t *Template) {
			t.Draft.Nodes[0].Props = map[string]json.RawMessage{
				"layouts": json.RawMessage(`{"desktop":{"mode":"free","gap":1000000}}`),
			}
		}},
		{"huge slot free frame", func(t *Template) {
			t.Draft.Nodes[0].Props = map[string]json.RawMessage{
				"slot_layouts": json.RawMessage(`{"main":{"layouts":{"desktop":{"mode":"free"}},"frames":{"desktop":{"x":1e308,"y":0,"w":240,"h":120}}}}`),
			}
		}},
		{"too many slot breakpoints", func(t *Template) {
			frames := make(map[string]any, maxTemplateBreakpoints+1)
			for i := 0; i <= maxTemplateBreakpoints; i++ {
				frames[fmt.Sprintf("bp-%d", i)] = map[string]any{"x": 0, "y": 0, "w": 240, "h": 120}
			}
			raw, err := json.Marshal(map[string]any{"main": map[string]any{"frames": frames}})
			if err != nil {
				panic(err)
			}
			t.Draft.Nodes[0].Props = map[string]json.RawMessage{"slot_layouts": raw}
		}},
		{"unsafe draft version", func(t *Template) { t.Draft.Version = maxTemplateRevision + 1 }},
		{"unsafe CAS revision", func(t *Template) { t.Revision = maxTemplateRevision + 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneTemplate(base)
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatalf("hostile geometry was accepted: %+v", candidate.Draft)
			}
		})
	}
	h := New(NewMemoryStore())
	hostile := cloneTemplate(base)
	hostile.Draft.Placements[0].Width = 1e308
	if _, err := h.PutCurrentTemplate(context.Background(), "hostile", "", hostile, 0, "api"); err == nil {
		t.Fatal("service persisted hostile geometry")
	}
	current, err := h.GetCurrentTemplate(context.Background(), "hostile", "")
	if err != nil || current.Persisted {
		t.Fatalf("failed hostile write changed current template: %+v %v", current, err)
	}
	revisions, err := h.ListRevisions(context.Background(), "hostile")
	if err != nil || len(revisions) != 0 {
		t.Fatalf("failed hostile write created history: %+v %v", revisions, err)
	}
}

func TestTemplateCASAllowsSingleConcurrentWriter(t *testing.T) {
	ctx := context.Background()
	h := New(NewMemoryStore())
	current, err := h.PutCurrentTemplate(ctx, "u", "", editableTemplate("tpl-root"), 0, "api")
	if err != nil {
		t.Fatal(err)
	}
	const writers = 24
	var wg sync.WaitGroup
	results := make(chan error, writers)
	for i := range writers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			candidate := cloneTemplate(current)
			candidate.Name = "writer-" + string(rune('a'+i))
			_, err := h.PutTemplate(ctx, "u", candidate, current.Revision, "api")
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent result: %v", err)
		}
	}
	if successes != 1 || conflicts != writers-1 {
		t.Fatalf("CAS results: successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestV2RollbackPreservesCurrentTemplates(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	legacy := Revision{
		Version: 2, ID: "legacy-v2", CreatedAt: newRevision(nil, nil, "", "").CreatedAt,
		Items: []Item{}, Pages: []Page{},
	}
	if err := store.AddRevision(ctx, "u", legacy, 50); err != nil {
		t.Fatal(err)
	}
	h := New(store)
	current, err := h.PutCurrentTemplate(ctx, "u", "", editableTemplate("tpl-root"), 0, "api")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Rollback(ctx, "u", legacy.ID, "api"); err != nil {
		t.Fatal(err)
	}
	got, err := h.GetCurrentTemplate(ctx, "u", "")
	if err != nil || !got.Persisted || got.Template.ID != current.ID {
		t.Fatalf("v2 rollback erased v3 templates: %+v %v", got, err)
	}
}
