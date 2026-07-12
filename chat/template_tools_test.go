package chat

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	einoJSONSchema "github.com/eino-contrib/jsonschema"

	"github.com/zdypro888/ihtml"
)

func templateToolDocument() ihtml.Template {
	return ihtml.Template{
		ID: "tpl-root", Name: "结构模板",
		Draft: ihtml.TemplateVersion{
			Version: 0, RootID: "root",
			Nodes: []ihtml.TemplateNode{{
				ID: "root", Kind: ihtml.NodeGrid, Name: "Root",
				Props: map[string]json.RawMessage{
					"layouts": json.RawMessage(`{"desktop":{"mode":"grid","columns":12,"gap":12}}`),
				},
			}},
			Slots: []ihtml.Slot{{ID: "main", NodeID: "root", Name: "主区", Accepts: []ihtml.ItemType{ihtml.ItemHTML}}},
			Placements: []ihtml.Placement{{
				ItemID: "card", SlotID: "main", Mode: ihtml.LayoutFlow, ColumnSpan: 12, RowSpan: 1,
			}},
			Breakpoints: []ihtml.Breakpoint{{ID: "desktop", Name: "Desktop", Columns: 12, Gap: 12}},
		},
	}
}

func templatePutArguments(t *testing.T, expected uint64, template ihtml.Template) string {
	t.Helper()
	payload := map[string]any{
		"page":              "",
		"expected_revision": expected,
		"template": aiTemplateDraftPayload{
			ID: template.ID, Name: template.Name, Draft: template.Draft,
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func executeLatestConfirmation(t *testing.T, rt *runtime) *eventSink {
	t.Helper()
	pending := rt.timeline.pendingCalls()
	if len(pending) != 1 {
		t.Fatalf("pending confirmations = %d: %+v", len(pending), pending)
	}
	sink := &eventSink{}
	call := pending[0]
	if err := executeConfirmedTool(context.Background(), rt, &call, sink.emit); err != nil {
		t.Fatal(err)
	}
	return sink
}

func sinkSummaries(sink *eventSink) string {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	var out []string
	for _, event := range sink.events {
		out = append(out, event.ToolSummary, event.Message)
	}
	return strings.Join(out, "\n")
}

func resetTemplateToolTimeline(rt *runtime) *eventSink {
	sink := &eventSink{}
	rt.timeline = &timeline{emit: sink.emit, mode: rt.mode}
	return sink
}

func TestTemplateToolSchemaAndStrictPayloadValidation(t *testing.T) {
	def, ok := findToolDef("ui_template_put_current")
	if !ok {
		t.Fatal("ui_template_put_current not registered")
	}
	if def.Params.AdditionalProperties != einoJSONSchema.FalseSchema || !def.Write || !def.Confirm || def.Risk != "medium" {
		t.Fatalf("put tool policy/schema = write:%v confirm:%v risk:%q additional:%p", def.Write, def.Confirm, def.Risk, def.Params.AdditionalProperties)
	}
	if _, ok := def.Params.Properties.Get("expected_revision"); !ok {
		t.Fatal("expected_revision absent from tool schema")
	}
	templateProperty, ok := def.Params.Properties.Get("template")
	if !ok || templateProperty.AdditionalProperties != einoJSONSchema.FalseSchema {
		t.Fatal("template payload is not a closed object schema")
	}
	for _, required := range []string{"expected_revision", "template"} {
		if !containsString(def.Params.Required, required) {
			t.Fatalf("required schema field %q missing: %v", required, def.Params.Required)
		}
	}

	rt, _, _ := newTestRuntime(t)
	unknown := `{"expected_revision":0,"template":{"id":"tpl-root","name":"Root","draft":{"version":0,"root_id":"","nodes":[],"slots":[],"placements":[],"breakpoints":[]},"content":"<h1>业务正文</h1>"}}`
	out, err := findTool(t, rt, "ui_template_put_current").InvokableRun(context.Background(), unknown)
	if err != nil || !strings.Contains(out, `"status":"error"`) || len(rt.timeline.pendingCalls()) != 0 {
		t.Fatalf("unknown/content payload reached confirmation: %s %v %+v", out, err, rt.timeline.pendingCalls())
	}

	resetTemplateToolTimeline(rt)
	propsContent := `{"expected_revision":0,"template":{"id":"tpl-root","name":"Root","draft":{"version":0,"root_id":"root","nodes":[{"id":"root","kind":"grid","props":{"content":"<h1>正文</h1>"}}],"slots":[],"placements":[],"breakpoints":[]}}}`
	out, err = findTool(t, rt, "ui_template_put_current").InvokableRun(context.Background(), propsContent)
	if err != nil || !strings.Contains(out, `"status":"error"`) || !strings.Contains(out, "业务内容") || len(rt.timeline.pendingCalls()) != 0 {
		t.Fatalf("props content was not rejected before confirmation: %s %v", out, err)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestTemplateGetIsReadOnlyAndNeverReturnsItemContent(t *testing.T) {
	rt, _, svc := newTestRuntime(t)
	secret := "SECRET_ITEM_BODY_6c91"
	if err := svc.PutItems(context.Background(), "t1", []ihtml.Item{{
		ID: "card", Type: ihtml.ItemHTML, Content: "<article>" + secret + "</article>", Meta: map[string]string{"slot": "main"},
	}}, "test", "seed"); err != nil {
		t.Fatal(err)
	}
	out, err := findTool(t, rt, "ui_template_get").InvokableRun(context.Background(), `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, secret) || !strings.Contains(out, `"item_content_included":false`) || !strings.Contains(out, `"published_only":true`) {
		t.Fatalf("template read leaked content or missed runtime contract: %s", out)
	}

	config := readOnlyToolsNodeConfig(rt)
	seenGet := false
	for _, candidate := range config.Tools {
		info, infoErr := candidate.Info(context.Background())
		if infoErr != nil {
			t.Fatal(infoErr)
		}
		if info.Name == "ui_template_get" {
			seenGet = true
		}
		if strings.HasPrefix(info.Name, "ui_template_") && info.Name != "ui_template_get" {
			t.Fatalf("template write tool %q leaked into Deep read-only subagent", info.Name)
		}
	}
	if !seenGet {
		t.Fatal("Deep read-only subagent cannot inspect current template")
	}
}

func TestTemplateToolsConfirmationCASPublishAndSlotDelete(t *testing.T) {
	rt, _, svc := newTestRuntime(t)
	ctx := context.Background()
	if err := svc.PutItems(ctx, "t1", []ihtml.Item{{ID: "card", Type: ihtml.ItemHTML, Content: "card"}}, "test", "seed"); err != nil {
		t.Fatal(err)
	}

	putOut, err := findTool(t, rt, "ui_template_put_current").InvokableRun(ctx, templatePutArguments(t, 0, templateToolDocument()))
	if err != nil || !strings.Contains(putOut, "pending_confirmation") {
		t.Fatalf("template put must wait for confirmation: %s %v", putOut, err)
	}
	pending := rt.timeline.pendingCalls()
	if len(pending) != 1 || pending[0].ConfirmationToken == "" || pending[0].Risk != "medium" || !pending[0].RequiresConfirmation {
		t.Fatalf("template put confirmation policy = %+v", pending)
	}
	executeLatestConfirmation(t, rt)
	current, err := svc.GetCurrentTemplate(ctx, "t1", "")
	if err != nil || !current.Persisted || current.Template.Revision != 1 || current.Template.Published != nil {
		t.Fatalf("draft save = %+v %v", current, err)
	}

	resetTemplateToolTimeline(rt)
	publishOut, err := findTool(t, rt, "ui_template_publish").InvokableRun(ctx, `{"id":"tpl-root","expected_revision":1}`)
	if err != nil || !strings.Contains(publishOut, "pending_confirmation") {
		t.Fatalf("publish must wait for confirmation: %s %v", publishOut, err)
	}
	executeLatestConfirmation(t, rt)
	current, err = svc.GetCurrentTemplate(ctx, "t1", "")
	if err != nil || current.Template.Revision != 2 || current.Template.Published == nil {
		t.Fatalf("publish result = %+v %v", current, err)
	}

	resetTemplateToolTimeline(rt)
	deleteOut, err := findTool(t, rt, "ui_template_slot_delete").InvokableRun(ctx, `{"id":"tpl-root","slot_id":"main","expected_revision":2}`)
	if err != nil || !strings.Contains(deleteOut, "pending_confirmation") {
		t.Fatalf("slot delete must wait for confirmation: %s %v", deleteOut, err)
	}
	executeLatestConfirmation(t, rt)
	current, err = svc.GetCurrentTemplate(ctx, "t1", "")
	items, itemsErr := svc.ListItems(ctx, "t1")
	if err != nil || itemsErr != nil || current.Template.Revision != 3 || len(current.Template.Draft.Slots) != 0 ||
		len(current.Template.Draft.Placements) != 1 || !current.Template.Draft.Placements[0].Unassigned ||
		current.Template.Draft.Placements[0].SlotID != "" || len(items) != 1 || items[0].ID != "card" {
		t.Fatalf("slot delete contract: current=%+v items=%+v errors=%v/%v", current, items, err, itemsErr)
	}
	// Draft-only deletion must not mutate the already-published runtime snapshot.
	if current.Template.Published == nil || len(current.Template.Published.Slots) != 1 {
		t.Fatalf("slot delete unexpectedly changed Published: %+v", current.Template.Published)
	}

	// A syntactically valid but stale CAS request is still confirmed, then fails
	// atomically at the service boundary without changing the current revision.
	resetTemplateToolTimeline(rt)
	staleOut, err := findTool(t, rt, "ui_template_publish").InvokableRun(ctx, `{"id":"tpl-root","expected_revision":2}`)
	if err != nil || !strings.Contains(staleOut, "pending_confirmation") {
		t.Fatalf("stale publish confirmation: %s %v", staleOut, err)
	}
	staleSink := executeLatestConfirmation(t, rt)
	if !strings.Contains(sinkSummaries(staleSink), "revision conflict") {
		t.Fatalf("stale CAS did not report conflict: %s", sinkSummaries(staleSink))
	}
	after, err := svc.GetCurrentTemplate(ctx, "t1", "")
	if err != nil || after.Template.Revision != 3 {
		t.Fatalf("stale CAS changed template: %+v %v", after, err)
	}
}

func TestTemplateWritesRespectReadOnlyAccessBeforeConfirmation(t *testing.T) {
	rt, _, _ := newTestRuntime(t)
	rt.access.ReadOnly = true
	out, err := findTool(t, rt, "ui_template_put_current").InvokableRun(context.Background(), templatePutArguments(t, 0, templateToolDocument()))
	if err != nil || !strings.Contains(out, `"status":"forbidden"`) || len(rt.timeline.pendingCalls()) != 0 {
		t.Fatalf("read-only template write was not blocked before signing: %s %v %+v", out, err, rt.timeline.pendingCalls())
	}
}
