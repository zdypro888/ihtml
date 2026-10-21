package uitools_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/uitools"
)

func runtimeFor(t *testing.T) *uitools.Runtime {
	t.Helper()
	svc, err := ihtml.ScopeService(ihtml.New(ihtml.NewMemoryStore()), "alice")
	if err != nil {
		t.Fatal(err)
	}
	return &uitools.Runtime{Service: svc, CheckItems: func([]ihtml.Item) error { return nil }}
}
func tool(t *testing.T, name string) uitools.Tool {
	t.Helper()
	for _, def := range append(uitools.ReadTools(), uitools.WriteTools()...) {
		if def.Name == name {
			return def
		}
	}
	t.Fatal(name)
	return uitools.Tool{}
}
func TestPrepareDoesNotWriteAndApplyRechecksCode(t *testing.T) {
	ctx := context.Background()
	rt := runtimeFor(t)
	put := tool(t, "ui_apply_items")
	raw := json.RawMessage(`{"items":[{"id":"card","type":"html","content":"<b>ok</b>"}]}`)
	count := 0
	rt.CheckItems = func([]ihtml.Item) error { count++; return nil }
	draft, err := put.Prepare(ctx, rt, raw)
	if err != nil {
		t.Fatal(err)
	}
	items, _ := rt.Service.ListItems(ctx)
	if len(items) != 0 || count != 1 {
		t.Fatalf("prepare mutated workspace: %v checks=%d", items, count)
	}
	denied := errors.New("host revoked code policy")
	rt.CheckItems = func([]ihtml.Item) error { count++; return denied }
	if _, err = put.Apply(ctx, rt, draft.Args); !errors.Is(err, denied) {
		t.Fatalf("expected current policy: %v", err)
	}
	items, _ = rt.Service.ListItems(ctx)
	if len(items) != 0 || count != 2 {
		t.Fatalf("write escaped current policy: %v checks=%d", items, count)
	}
	rt.CheckItems = nil
	if _, err = put.Apply(ctx, rt, draft.Args); !errors.Is(err, uitools.ErrCodePolicyRequired) {
		t.Fatal(err)
	}
}
func TestPublicationKeepsOtherPageAndRejectsMixedItems(t *testing.T) {
	ctx := context.Background()
	rt := runtimeFor(t)
	if err := rt.Service.PutItems(ctx, []ihtml.Item{{ID: "other", Type: "html", Page: "elsewhere", Content: "ok"}}, "test", ""); err != nil {
		t.Fatal(err)
	}
	publish := tool(t, "ui_publish_page")
	if _, err := publish.Prepare(ctx, rt, json.RawMessage(`{"page":{"name":"home","title":"首页"},"items":[{"id":"bad","type":"html","page":"elsewhere","content":"ok"}]}`)); err == nil {
		t.Fatal("mixed pages accepted")
	}
	draft, err := publish.Prepare(ctx, rt, json.RawMessage(`{"page":{"name":"home","title":"首页"},"items":[{"id":"home-card","type":"html","content":"ok"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = publish.Apply(ctx, rt, draft.Args); err != nil {
		t.Fatal(err)
	}
	items, _ := rt.Service.ListItems(ctx)
	if len(items) != 2 {
		t.Fatalf("publication altered another page: %v", items)
	}
	for _, it := range items {
		if it.ID == "home-card" && it.Page != "home" {
			t.Fatal(it)
		}
	}
}
func TestReservedDataKeysAndCancelledApplyNeverWrite(t *testing.T) {
	rt := runtimeFor(t)
	ctx := context.Background()
	put := tool(t, "ui_put_data")
	for _, key := range []string{ihtml.ReservedKVPrefix + "theme", "layout.home", "item-layout.card", "invalid/key"} {
		raw, _ := json.Marshal(map[string]any{"key": key, "value_json": "{}"})
		if _, err := put.Prepare(ctx, rt, raw); err == nil {
			t.Fatalf("reserved key accepted: %s", key)
		}
		if _, err := put.Apply(ctx, rt, raw); err == nil {
			t.Fatalf("reserved write accepted: %s", key)
		}
	}
	draft, err := put.Prepare(ctx, rt, json.RawMessage(`{"key":"settings","value_json":"{\"label\":\"中文\"}"}`))
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = put.Apply(cancelled, rt, draft.Args); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err = rt.Service.KVGet(ctx, "settings"); !errors.Is(err, ihtml.ErrNotFound) {
		t.Fatalf("cancelled write happened: %v", err)
	}
}
func TestReadPaginationHasNoSourceAndRuneChunks(t *testing.T) {
	ctx := context.Background()
	rt := runtimeFor(t)
	if err := rt.Service.PutItems(ctx, []ihtml.Item{{ID: "card", Type: "html", Content: "中文内容"}}, "test", ""); err != nil {
		t.Fatal(err)
	}
	got, err := tool(t, "ui_list_state").Read(ctx, rt, json.RawMessage(`{"offset":0,"limit":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.ForModel, "中文内容") || !strings.Contains(got.ForModel, `"content_bytes":12`) {
		t.Fatal(got.ForModel)
	}
	got, err = tool(t, "ui_get_item").Read(ctx, rt, json.RawMessage(`{"id":"card","offset":1,"max_chars":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.ForModel, `"content":"文内"`) || !strings.Contains(got.ForModel, `"next_offset":3`) {
		t.Fatal(got.ForModel)
	}
}
func TestRollbackRevalidatesHistoricalCode(t *testing.T) {
	ctx := context.Background()
	rt := runtimeFor(t)
	for _, content := range []string{"before", "after"} {
		if err := rt.Service.PutItems(ctx, []ihtml.Item{{ID: "card", Type: "html", Content: content}}, "test", ""); err != nil {
			t.Fatal(err)
		}
	}
	revisions, err := rt.Service.ListRevisions(ctx)
	if err != nil || len(revisions) == 0 {
		t.Fatalf("revisions: %v %v", revisions, err)
	}
	raw, _ := json.Marshal(map[string]any{"revision_id": revisions[0].ID})
	rollback := tool(t, "ui_rollback")
	before, err := rt.Service.ListItems(ctx)
	if err != nil {
		t.Fatal(err)
	}
	checks := 0
	rt.CheckItems = func(items []ihtml.Item) error {
		checks++
		if len(items) != 1 {
			t.Fatalf("unexpected historical items: %v", items)
		}
		return nil
	}
	draft, err := rollback.Prepare(ctx, rt, raw)
	if err != nil || checks != 1 {
		t.Fatalf("prepare: checks=%d err=%v", checks, err)
	}
	after, err := rt.Service.ListItems(ctx)
	if err != nil || len(after) != len(before) || after[0].Content != before[0].Content {
		t.Fatalf("prepare changed items: %v %v %v", before, after, err)
	}
	denied := errors.New("historical code denied")
	rt.CheckItems = func([]ihtml.Item) error { return denied }
	if _, err = rollback.Prepare(ctx, rt, raw); !errors.Is(err, denied) {
		t.Fatalf("prepare skipped historical policy: %v", err)
	}
	if _, err = rollback.Apply(ctx, rt, draft.Args); !errors.Is(err, denied) {
		t.Fatalf("rollback skipped current policy: %v", err)
	}
	rt.CheckItems = nil
	if _, err = rollback.Prepare(ctx, rt, raw); !errors.Is(err, uitools.ErrCodePolicyRequired) {
		t.Fatalf("prepare without policy: %v", err)
	}
	if _, err = rollback.Apply(ctx, rt, draft.Args); !errors.Is(err, uitools.ErrCodePolicyRequired) {
		t.Fatalf("apply without policy: %v", err)
	}
}
func TestMetadataIsFreshAndInvalidRuntimeRejected(t *testing.T) {
	defs := uitools.ReadTools()
	defs[0].Parameters["type"] = "changed"
	if uitools.ReadTools()[0].Parameters["type"] != "object" {
		t.Fatal("shared mutable schema")
	}
	if _, err := defs[0].Read(context.Background(), nil, nil); !errors.Is(err, uitools.ErrInvalidRuntime) {
		t.Fatal(err)
	}
}

type noPublication struct{ ihtml.ScopedService }

func TestPublicationNeverFallsBackToPartialWrites(t *testing.T) {
	ctx := context.Background()
	rt := runtimeFor(t)
	rt.Service = noPublication{rt.Service}
	publish := tool(t, "ui_publish_page")
	draft, err := publish.Prepare(ctx, rt, json.RawMessage(`{"page":{"name":"home","title":"首页"},"items":[{"id":"card","type":"html","content":"ok"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = publish.Apply(ctx, rt, draft.Args); err == nil {
		t.Fatal("missing atomic capability accepted")
	}
	items, _ := rt.Service.ListItems(ctx)
	pages, _ := rt.Service.ListPages(ctx)
	if len(items) != 0 || len(pages) != 0 {
		t.Fatalf("partial publication: %v %v", items, pages)
	}
}
