package chat

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/zdypro888/ihtml"
)

func TestToolArgumentGuardRejectsOversizeDepthKeysAndNodes(t *testing.T) {
	tooManyKeys := strings.Builder{}
	tooManyKeys.WriteByte('{')
	for index := 0; index <= maxToolArgumentKeys; index++ {
		if index > 0 {
			tooManyKeys.WriteByte(',')
		}
		fmt.Fprintf(&tooManyKeys, "%q:0", fmt.Sprintf("k%d", index))
	}
	tooManyKeys.WriteByte('}')

	tooManyNodes := "[" + strings.Repeat("0,", maxToolArgumentNodes) + "0]"
	tooDeep := strings.Repeat("[", maxToolArgumentDepth+1) + "0" + strings.Repeat("]", maxToolArgumentDepth+1)
	for name, raw := range map[string]string{
		"bytes": strings.Repeat("x", maxToolArgumentsBytes+1),
		"depth": tooDeep,
		"keys":  tooManyKeys.String(),
		"nodes": tooManyNodes,
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateToolArgumentsJSON(raw); err == nil {
				t.Fatalf("malicious %s payload was accepted", name)
			}
		})
	}
	if err := validateToolArgumentsJSON(`{"outer":{"items":[1,2,3]},"ok":true}`); err != nil {
		t.Fatalf("valid payload rejected: %v", err)
	}
}

func TestJSONPayloadEstimatorAccountsForEncodingEscapes(t *testing.T) {
	value := "quote=\" slash=\\ html=<>& control=\n separator=\u2028 中文"
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	size, ok := jsonEncodedStringSize(value, len(encoded))
	if !ok || size != len(encoded) {
		t.Fatalf("encoded string estimate = %d/%v, want %d", size, ok, len(encoded))
	}
	heavy := map[string]any{"value": strings.Repeat("<", maxExternalSessionStorePayloadBytes/6)}
	if jsonPayloadWithinLimits(heavy, maxExternalSessionStorePayloadBytes, maxExternalSessionStorePayloadNodes, maxExternalSessionStorePayloadDepth) {
		t.Fatal("HTML-escaped external-store payload bypassed encoded byte limit")
	}
}

func TestSemanticToolRejectsPayloadBeforeExecution(t *testing.T) {
	rt, _, _ := newTestRuntime(t)
	executed := false
	tool := &semanticTool{rt: rt, def: toolDef{
		Name: "guarded", Title: "guarded", Risk: ihtml.ChatRiskLow,
		Execute: func(context.Context, *runtime, map[string]any) (any, string, error) {
			executed = true
			return nil, "", nil
		},
	}}
	if _, err := tool.InvokableRun(context.Background(), strings.Repeat("x", maxToolArgumentsBytes+1)); err == nil {
		t.Fatal("oversized arguments did not fail")
	}
	if executed {
		t.Fatal("oversized arguments reached tool execution")
	}
}

func TestToolAndPlanIDsAreBounded(t *testing.T) {
	raw := strings.Repeat("id", maxToolCallIDBytes)
	first := normalizeToolCallID(raw, "test")
	second := normalizeToolCallID(raw, "test")
	if first != second || len(first) > maxToolCallIDBytes || !strings.HasPrefix(first, "tool_") {
		t.Fatalf("normalized tool id = %q / %q", first, second)
	}
	timeline := &timeline{emit: func(ihtml.ChatStreamEvent) error { return nil }}
	plan := &deepPlanTool{timeline: timeline}
	arguments, _ := json.Marshal(map[string]any{"todos": []map[string]any{{
		"id": strings.Repeat("p", maxPlanItemIDBytes+1), "content": "inspect", "status": "pending",
	}}})
	if _, err := plan.InvokableRun(context.Background(), string(arguments)); err == nil {
		t.Fatal("oversized plan id was accepted")
	}
}

func TestRunningAndResultEventsDoNotEchoToolArguments(t *testing.T) {
	rt, sink, svc := newTestRuntime(t)
	if err := svc.KVSet(context.Background(), rt.user, "visible-key", json.RawMessage(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := findTool(t, rt, "ui_kv_get").InvokableRun(context.Background(), `{"key":"visible-key"}`); err != nil {
		t.Fatal(err)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	found := false
	for _, event := range sink.events {
		if event.Type != ihtml.ChatStreamToolStart && event.Type != ihtml.ChatStreamToolResult {
			continue
		}
		found = true
		if event.ToolCall == nil || event.ToolCall.ID == "" {
			t.Fatalf("event lost correlation: %#v", event)
		}
		if event.ToolCall.Body != nil || event.ToolCall.Query != nil || event.ToolCall.ConfirmationToken != "" {
			t.Fatalf("event echoed full tool arguments/token: %#v", event.ToolCall)
		}
		if len(event.ToolInput) > maxToolInputPreviewBytes || len(event.ToolDebug) > maxToolDebugPreviewBytes {
			t.Fatalf("unbounded tool preview: input=%d debug=%d", len(event.ToolInput), len(event.ToolDebug))
		}
	}
	if !found {
		t.Fatal("tool emitted no running/result events")
	}
}

func TestToolEventReferenceAllowlistsAndBoundsDisplayMetadata(t *testing.T) {
	call := &ihtml.ProposedToolCall{
		ID: "correlation", Name: strings.Repeat("n", 1024), Title: strings.Repeat("t", 1024),
		Method: "TOOL", Path: strings.Repeat("/path", 1024), Risk: "medium",
		Query: map[string]any{"secret": "query"}, Body: map[string]any{"secret": "body"},
		Reason: strings.Repeat("private-reason", 64<<10), ConfirmationToken: "signed-secret",
		ConfirmationNonce: "nonce", ConfirmationExpires: time.Now().Add(time.Minute).Unix(),
	}
	ref := toolEventCallReference(call)
	if ref == nil || ref.ID != call.ID || ref.ConfirmationNonce != call.ConfirmationNonce {
		t.Fatalf("tool reference lost correlation: %#v", ref)
	}
	if ref.Query != nil || ref.Body != nil || ref.Reason != "" || ref.ConfirmationToken != "" ||
		len(ref.Name) > 128 || len(ref.Title) > 256 || len(ref.Path) > 512 {
		t.Fatalf("tool reference leaked or failed to bound metadata: %#v", ref)
	}
}

func TestOversizedConfirmationCardIsRejectedBeforeSigning(t *testing.T) {
	rt, sink, _ := newTestRuntime(t)
	rt.cfg.AllowUnconfirmedItemWrites = false
	store := &countingConfirmNonceStore{ConfirmNonceStore: newMemoryConfirmNonceStore()}
	rt.signer = newConfirmSigner([]byte("payload-guard-test"), store)
	content := strings.Repeat("<", 220<<10)
	arguments := `{"items":[{"id":"large-card","type":"html","content":"` + content + `"}]}`
	out, err := findTool(t, rt, "ui_item_put").InvokableRun(context.Background(), arguments)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"status":"payload_too_large"`) {
		t.Fatalf("oversized confirmation result = %s", out)
	}
	if pending := rt.timeline.pendingCalls(); len(pending) != 0 {
		t.Fatalf("oversized confirmation was signed: %+v", pending)
	}
	if store.puts != 0 {
		t.Fatalf("oversized confirmation reached signer: issued nonces=%d", store.puts)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	for _, event := range sink.events {
		if event.Type == ihtml.ChatStreamConfirmationRequired {
			t.Fatalf("oversized confirmation event escaped: encoded candidate=%#v", event)
		}
	}
}

type countingConfirmNonceStore struct {
	ConfirmNonceStore
	puts int
}

func (s *countingConfirmNonceStore) Put(ctx context.Context, nonce string, expiry time.Time) error {
	s.puts++
	return s.ConfirmNonceStore.Put(ctx, nonce, expiry)
}

func TestAcceptedConfirmationEventFitsInboundFrame(t *testing.T) {
	rt, sink, _ := newTestRuntime(t)
	rt.cfg.AllowUnconfirmedItemWrites = false
	if _, err := findTool(t, rt, "ui_item_put").InvokableRun(context.Background(), `{"items":[{"id":"small-card","type":"html","content":"<p>ok</p>"}]}`); err != nil {
		t.Fatal(err)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	for _, event := range sink.events {
		if event.Type != ihtml.ChatStreamConfirmationRequired {
			continue
		}
		encoded, err := json.Marshal(event)
		if err != nil || len(encoded) >= confirmationFrameByteLimit() || event.ToolCall == nil || event.ToolCall.ConfirmationToken == "" {
			t.Fatalf("accepted confirmation frame = bytes:%d event:%#v err:%v", len(encoded), event, err)
		}
		return
	}
	t.Fatal("small confirmation emitted no confirmation_required event")
}

func TestToolSummaryAndTimelineEvidenceAreHardBounded(t *testing.T) {
	rt, sink, _ := newTestRuntime(t)
	hugeSummary := strings.Repeat("summary-<>&-", 128<<10)
	tool := &semanticTool{rt: rt, def: toolDef{
		Name: "huge_summary", Title: strings.Repeat("title", 1024), Risk: ihtml.ChatRiskLow,
		Execute: func(context.Context, *runtime, map[string]any) (any, string, error) {
			return map[string]any{"ok": true}, hugeSummary, nil
		},
	}}
	out, err := tool.InvokableRun(context.Background(), `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > maxToolSummaryBytes*6+2048 || strings.Contains(out, hugeSummary) {
		t.Fatalf("model tool result retained giant summary: bytes=%d", len(out))
	}
	sink.mu.Lock()
	for _, event := range sink.events {
		if len(event.ToolSummary) > maxToolSummaryBytes || len(event.ToolName) > maxToolNameBytes || len(event.ToolTitle) > maxToolTitleBytes {
			sink.mu.Unlock()
			t.Fatalf("unbounded tool event: %#v", event)
		}
	}
	sink.mu.Unlock()

	largeBody := json.RawMessage(`{"value":"` + strings.Repeat("x", 64<<10) + `"}`)
	for index := 0; index < 40; index++ {
		rt.timeline.recordCompleted(strings.Repeat("tool", 100), hugeSummary, largeBody)
	}
	evidence := rt.timeline.completedEvidence()
	retained := 0
	for _, item := range evidence {
		retained += timelineEvidenceWeight(item)
		if len(item.Summary) > maxToolSummaryBytes || len(item.Tool) > maxToolNameBytes || len(item.Body) > maxTimelineEvidenceBody {
			t.Fatalf("unbounded retained evidence: %#v", item)
		}
	}
	if len(evidence) > maxTimelineEvidenceEntries || retained > maxTimelineEvidenceBytes || rt.timeline.completedBytes != retained {
		t.Fatalf("timeline evidence bounds = entries:%d retained:%d tracked:%d", len(evidence), retained, rt.timeline.completedBytes)
	}
	encoded, err := json.Marshal(evidence)
	if err != nil || len(encoded) > maxTimelineEvidenceBytes {
		t.Fatalf("repair evidence envelope = bytes:%d err:%v", len(encoded), err)
	}
}

type largeToolReadService struct {
	ihtml.Service
	items   []ihtml.Item
	current ihtml.CurrentTemplate
}

func (s *largeToolReadService) ListItems(context.Context, string) ([]ihtml.Item, error) {
	return s.items, nil
}

func (s *largeToolReadService) GetCurrentTemplate(context.Context, string, string) (ihtml.CurrentTemplate, error) {
	return s.current, nil
}

func TestLargeItemAndTemplateReadsAreBoundedAtSource(t *testing.T) {
	content := strings.Repeat("x", ihtml.MaxItemContentBytes)
	items := make([]ihtml.Item, 200)
	for index := range items {
		items[index] = ihtml.Item{ID: fmt.Sprintf("item-%03d", index), Type: ihtml.ItemHTML, Content: content}
	}
	props := make(map[string]json.RawMessage, 64)
	for index := 0; index < 64; index++ {
		props[fmt.Sprintf("p%d", index)] = json.RawMessage(`"` + strings.Repeat("z", 8<<10) + `"`)
	}
	nodes := make([]ihtml.TemplateNode, 300)
	for index := range nodes {
		nodes[index] = ihtml.TemplateNode{ID: fmt.Sprintf("node-%03d", index), Kind: ihtml.NodeGrid, Props: props}
	}
	service := &largeToolReadService{
		Service: ihtml.New(ihtml.NewMemoryStore()), items: items,
		current: ihtml.CurrentTemplate{Persisted: true, Template: ihtml.Template{
			ID: "large-template", Name: "Large", Draft: ihtml.TemplateVersion{Nodes: nodes},
		}},
	}
	rt, sink, _ := newTestRuntime(t)
	rt.svc = service
	listDef, _ := findToolDef("ui_item_list")
	result, _, err := executeToolDef(context.Background(), listDef, rt, map[string]any{
		"include_content": true, "offset": float64(0), "limit": float64(50),
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := result.(map[string]any)
	views := payload["items"].([]toolItemView)
	totalContent := 0
	for _, view := range views {
		totalContent += len(view.Content)
	}
	if len(views) != maxToolItemListLimit || totalContent > maxToolItemListContentBytes || payload["truncated"] != true {
		t.Fatalf("bounded item page = views:%d content:%d truncated:%v", len(views), totalContent, payload["truncated"])
	}
	getDef, _ := findToolDef("ui_item_get")
	itemResult, _, err := executeToolDef(context.Background(), getDef, rt, map[string]any{"id": "item-000"})
	if err != nil || len(itemResult.(toolItemView).Content) > maxToolSingleItemContentBytes || !itemResult.(toolItemView).ContentTruncated {
		t.Fatalf("bounded item get = %+v, %v", itemResult, err)
	}

	if _, err := findTool(t, rt, "ui_template_get").InvokableRun(context.Background(), `{}`); err != nil {
		t.Fatal(err)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	for _, event := range sink.events {
		if event.Type == ihtml.ChatStreamToolResult && event.ToolName == "ui_template_get" {
			if len(event.ToolResult) > 32002 || !strings.Contains(string(event.ToolResult), "truncated") {
				t.Fatalf("template result was not bounded: bytes=%d result=%s", len(event.ToolResult), event.ToolResult)
			}
			return
		}
	}
	t.Fatal("template tool emitted no result")
}

func TestRepeatedArgumentBudgetStoresOnlyFixedDigest(t *testing.T) {
	budget := newTurnBudget(Config{MaxToolCallsPerTurn: -1, MaxToolCallsPerTool: -1, MaxRepeatedToolCalls: 2}, AgentModeFast, nil)
	if err := budget.takeToolCall(context.Background(), "read", map[string]any{"large": strings.Repeat("x", 64<<10)}); err != nil {
		t.Fatal(err)
	}
	if len(budget.repeated) != 1 {
		t.Fatalf("repeat entries = %d", len(budget.repeated))
	}
	for digest := range budget.repeated {
		if len(digest) != sha256.Size {
			t.Fatalf("repeat digest length = %d", len(digest))
		}
	}
}

func TestToolArgumentDigestStreamsHTMLEscapingPayloadAndIsCanonical(t *testing.T) {
	htmlHeavy := strings.Repeat("<>&", 70<<10)
	args := map[string]any{"html": htmlHeavy, "nested": map[string]any{"b": float64(1), "a": true}}
	legacyCanonical, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	if len(legacyCanonical) <= maxToolArgumentHashBytes {
		t.Fatalf("test payload did not exercise HTML escaping expansion: %d", len(legacyCanonical))
	}
	first, err := canonicalToolArgumentDigest("render", args)
	if err != nil {
		t.Fatalf("streaming digest rejected bounded raw values: %v", err)
	}
	second, err := canonicalToolArgumentDigest("render", map[string]any{
		"nested": map[string]any{"a": true, "b": int64(1)}, "html": htmlHeavy,
	})
	if err != nil || first != second {
		t.Fatalf("canonical digest mismatch: first=%x second=%x err=%v", first, second, err)
	}
	budget := newTurnBudget(Config{MaxToolCallsPerTurn: -1, MaxToolCallsPerTool: -1, MaxRepeatedToolCalls: 2}, AgentModeFast, nil)
	if err := budget.takeToolCall(context.Background(), "render", args); err != nil {
		t.Fatal(err)
	}
	if len(budget.repeated) != 1 {
		t.Fatalf("repeat digest entries = %d", len(budget.repeated))
	}
}

func TestToolArgumentDigestFailsClosedOnCyclesAndNonJSONValues(t *testing.T) {
	cycle := map[string]any{}
	cycle["self"] = cycle
	for name, args := range map[string]map[string]any{
		"cycle": cycle,
		"nan":   {"value": math.NaN()},
		"host":  {"value": panickingToolResultStringer{}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := canonicalToolArgumentDigest("unsafe", args); err == nil || strings.Contains(err.Error(), "super-secret") {
				t.Fatalf("unsafe argument digest error = %v", err)
			}
		})
	}
}

func TestToolArgumentDigestPreservesExactJSONNumbers(t *testing.T) {
	first, err := canonicalToolArgumentDigest("number", map[string]any{"value": json.Number("9007199254740992")})
	if err != nil {
		t.Fatal(err)
	}
	second, err := canonicalToolArgumentDigest("number", map[string]any{"value": json.Number("9007199254740993")})
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("distinct large JSON integers collided")
	}
	integer, err := canonicalToolArgumentDigest("number", map[string]any{"value": int64(1)})
	if err != nil {
		t.Fatal(err)
	}
	number, err := canonicalToolArgumentDigest("number", map[string]any{"value": json.Number(" 1 ")})
	if err != nil || integer != number {
		t.Fatalf("stable integer/json.Number digest mismatch: int=%x number=%x err=%v", integer, number, err)
	}
	for _, invalid := range []json.Number{"01", "true", "NaN"} {
		if _, err := canonicalToolArgumentDigest("number", map[string]any{"value": invalid}); err == nil {
			t.Fatalf("invalid JSON number %q was accepted", invalid)
		}
	}
}

type panickingToolResultStringer struct{}

func (panickingToolResultStringer) String() string { panic("result-super-secret") }

func TestMaliciousToolResultProjectionCannotPanicOrLeak(t *testing.T) {
	raw, truncated, err := marshalBoundedToolResult(map[panickingToolResultStringer]string{{}: "secret-value"})
	if err != nil || !truncated || strings.Contains(string(raw), "super-secret") || strings.Contains(string(raw), "secret-value") {
		t.Fatalf("malicious result projection = raw:%s truncated:%v err:%v", raw, truncated, err)
	}
}
