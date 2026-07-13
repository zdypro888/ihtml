package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zdypro888/ihtml"
)

func inspectRuntime(emitted *[]ihtml.ChatStreamEvent, mu *sync.Mutex) *runtime {
	broker := newInspectBroker()
	return &runtime{
		inspect: broker,
		timeline: &timeline{emit: func(ev ihtml.ChatStreamEvent) error {
			mu.Lock()
			*emitted = append(*emitted, ev)
			mu.Unlock()
			return nil
		}},
	}
}

func TestInspectRoundTrip(t *testing.T) {
	var mu sync.Mutex
	var emitted []ihtml.ChatStreamEvent
	rt := inspectRuntime(&emitted, &mu)

	// 模拟内核: 看到 inspect_request 就应答同 ID 的 inspect_result。
	done := make(chan struct{})
	go func() {
		defer close(done)
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			var id string
			for _, ev := range emitted {
				if ev.Type == ihtml.ChatStreamInspectRequest {
					id = ev.InspectID
				}
			}
			mu.Unlock()
			if id != "" {
				rt.inspect.deliver(id, &ihtml.ChatClientInspection{
					Page:     "dash",
					Viewport: ihtml.ChatClientViewport{Width: 1280, Height: 800, Mode: "pc"},
					Items: []ihtml.ChatClientInspectionItem{
						{ID: "card", Type: "html", Present: true, Visible: true, Width: 600, Height: 240, Text: strings.Repeat("长", 500)},
						{ID: "ghost", Type: "js", Present: false, Error: "ReferenceError: x is not defined"},
					},
					Errors:      []string{"boom"},
					VisibleText: strings.Repeat("A", 5000),
				})
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()

	result, summary, err := executeInspect(context.Background(), rt, nil)
	<-done
	if err != nil {
		t.Fatal(err)
	}
	view, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("result type: %T", result)
	}
	if view["page"] != "dash" || view["items_not_rendered"] != 1 {
		t.Fatalf("view: %+v", view)
	}
	items := view["items"].([]map[string]any)
	if len(items) != 2 || items[0]["id"] != "card" || items[0]["size"] != "600x240" {
		t.Fatalf("items: %+v", items)
	}
	if text := items[0]["text"].(string); len(text) > maxInspectionText {
		t.Fatalf("item text must be clipped, got %d bytes", len(text))
	}
	if items[1]["error"].(string) == "" || items[1]["present"] != false {
		t.Fatalf("missing item view: %+v", items[1])
	}
	if full := view["visible_text"].(string); len(full) > maxInspectionFullText {
		t.Fatalf("visible_text must be clipped, got %d bytes", len(full))
	}
	if !strings.Contains(summary, "1 个已渲染") || !strings.Contains(summary, "1 个未渲染") {
		t.Fatalf("summary: %q", summary)
	}
}

func TestInspectTimesOutWhenPageOffline(t *testing.T) {
	var mu sync.Mutex
	var emitted []ihtml.ChatStreamEvent
	rt := inspectRuntime(&emitted, &mu)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	result, summary, err := executeInspect(ctx, rt, nil)
	if err != nil {
		// ctx 到期先于 inspectTimeout 时返回 ctx 错误也可接受, 但不能 panic/挂死。
		return
	}
	view := result.(map[string]any)
	if view["online"] != false || summary == "" {
		t.Fatalf("offline view: %+v %q", view, summary)
	}
}

func TestInspectBrokerLateDeliveryIsDropped(t *testing.T) {
	broker := newInspectBroker()
	id, ch := broker.register()
	broker.drop(id)
	broker.deliver(id, &ihtml.ChatClientInspection{Page: "x"}) // 不得 panic 或阻塞
	select {
	case <-ch:
		t.Fatal("dropped inspect id must not receive delivery")
	default:
	}
}

func TestSessionRoutesInspectResult(t *testing.T) {
	s := &session{
		emit:    func(ihtml.ChatStreamEvent) error { return nil },
		inspect: newInspectBroker(),
	}
	id, ch := s.inspect.register()
	s.HandleEvent(context.Background(), &ihtml.ChatClientEvent{
		Type:      ihtml.ChatClientInspectResult,
		InspectID: " " + id + " ", // 容忍空白
		Inspect:   &ihtml.ChatClientInspection{Page: "dash"},
	})
	select {
	case payload := <-ch:
		if payload.Page != "dash" {
			t.Fatalf("payload: %+v", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("inspect_result was not routed to the waiting tool")
	}
}

func TestInspectPaginatesAll512ItemsWithinStructuredBudget(t *testing.T) {
	all := make([]ihtml.ChatClientInspectionItem, 512)
	for i := range all {
		all[i] = ihtml.ChatClientInspectionItem{
			ID: fmt.Sprintf("item-%03d", i), Type: "html", Page: "dash", Slot: "main",
			Present: true, Visible: true, Width: 400, Height: 120,
			Text: strings.Repeat("\x00", 200), Error: strings.Repeat("\x00", 300),
		}
	}
	broker := newInspectBroker()
	rt := &runtime{inspect: broker}
	rt.timeline = &timeline{emit: func(ev ihtml.ChatStreamEvent) error {
		if ev.Type != ihtml.ChatStreamInspectRequest {
			return nil
		}
		start := min(len(all), max(0, ev.InspectOffset))
		limit := min(inspectMaxLimit, max(1, ev.InspectLimit))
		end := min(len(all), start+limit)
		nextOffset := 0
		if end < len(all) {
			nextOffset = end
		}
		broker.deliver(ev.InspectID, &ihtml.ChatClientInspection{
			Page: "dash", ThemeMode: "dark", Total: len(all), Offset: start, Limit: limit,
			NextOffset: nextOffset, Truncated: end < len(all), Items: append([]ihtml.ChatClientInspectionItem(nil), all[start:end]...),
		})
		return nil
	}}

	seen := make(map[string]struct{}, len(all))
	offset := 0
	for page := 0; page < 600 && offset < len(all); page++ {
		result, _, err := executeInspect(context.Background(), rt, map[string]any{"offset": offset, "limit": inspectMaxLimit})
		if err != nil {
			t.Fatal(err)
		}
		view := result.(map[string]any)
		raw, err := json.Marshal(view)
		if err != nil || len(raw) > maxInspectionResultBytes {
			t.Fatalf("page %d is not valid budgeted JSON: bytes=%d err=%v", page, len(raw), err)
		}
		for _, item := range view["items"].([]map[string]any) {
			id := item["id"].(string)
			if _, duplicate := seen[id]; duplicate {
				t.Fatalf("duplicate item %s", id)
			}
			seen[id] = struct{}{}
		}
		next, more := view["next_offset"].(int)
		if !more {
			break
		}
		if next <= offset {
			t.Fatalf("pagination did not advance: offset=%d next=%d", offset, next)
		}
		offset = next
	}
	if len(seen) != len(all) {
		t.Fatalf("pagination returned %d/%d items", len(seen), len(all))
	}
}

func TestInspectUsesClampedClientOffsetAndIDFilter(t *testing.T) {
	broker := newInspectBroker()
	rt := &runtime{inspect: broker}
	rt.timeline = &timeline{emit: func(ev ihtml.ChatStreamEvent) error {
		if len(ev.InspectIDs) != 1 || ev.InspectIDs[0] != "ai-note" || ev.InspectOffset != 100 {
			t.Fatalf("unexpected inspect request: %+v", ev)
		}
		broker.deliver(ev.InspectID, &ihtml.ChatClientInspection{Total: 1, Offset: 1, Limit: 1})
		return nil
	}}
	result, summary, err := executeInspect(context.Background(), rt, map[string]any{
		"offset": 100, "limit": 1, "ids": []any{"ai-note"},
	})
	if err != nil {
		t.Fatal(err)
	}
	view := result.(map[string]any)
	if view["offset"] != 1 || !strings.Contains(summary, "offset=1") {
		t.Fatalf("view=%+v summary=%q", view, summary)
	}
}

func TestInspectPaginatesLegacyFullResponseLocally(t *testing.T) {
	legacy := &ihtml.ChatClientInspection{}
	for i := 0; i < 45; i++ {
		legacy.Items = append(legacy.Items, ihtml.ChatClientInspectionItem{ID: fmt.Sprintf("legacy-%02d", i), Present: true})
	}
	first := sanitizeInspection(legacy, inspectQuery{limit: 20})
	second := sanitizeInspection(legacy, inspectQuery{offset: 20, limit: 20})
	third := sanitizeInspection(legacy, inspectQuery{offset: 40, limit: 20})
	if first["next_offset"] != 20 || second["next_offset"] != 40 {
		t.Fatalf("legacy cursors did not advance: first=%+v second=%+v", first, second)
	}
	secondItems := second["items"].([]map[string]any)
	thirdItems := third["items"].([]map[string]any)
	if len(secondItems) != 20 || secondItems[0]["id"] != "legacy-20" || len(thirdItems) != 5 || thirdItems[0]["id"] != "legacy-40" {
		t.Fatalf("legacy local pages are wrong: second=%+v third=%+v", secondItems, thirdItems)
	}
	if _, more := third["next_offset"]; more {
		t.Fatalf("last legacy page must terminate: %+v", third)
	}
}

func TestInspectFiltersLegacyFullResponseByRequestedIDs(t *testing.T) {
	legacy := &ihtml.ChatClientInspection{Items: []ihtml.ChatClientInspectionItem{
		{ID: "other", Present: true},
		{ID: "target", Present: true, Visible: true, Text: "right item"},
	}}
	view := sanitizeInspection(legacy, inspectQuery{limit: 20, ids: []string{"target", "missing"}})
	items := view["items"].([]map[string]any)
	if len(items) != 2 || items[0]["id"] != "target" || items[0]["text"] != "right item" || items[1]["id"] != "missing" || items[1]["present"] != false {
		t.Fatalf("legacy ids were not filtered exactly: %+v", items)
	}
}

func TestInspectNewResponseUsesServerCursorAndStopsAtHardLimit(t *testing.T) {
	clientItems := make([]ihtml.ChatClientInspectionItem, 20)
	for i := range clientItems {
		clientItems[i] = ihtml.ChatClientInspectionItem{ID: fmt.Sprintf("item-%03d", 500+i), Present: true}
	}

	// 客户端谎报/陈旧 Offset=0 时，游标仍必须从服务端请求的 500
	// 开始；512 之后的条目被丢弃，且不能沿用客户端 truncated 继续翻。
	view := sanitizeInspection(&ihtml.ChatClientInspection{
		Total: 1000, Offset: 0, Limit: 20, Truncated: true, Items: clientItems,
	}, inspectQuery{offset: 500, limit: 20})
	items := view["items"].([]map[string]any)
	if view["offset"] != 500 || view["total"] != maxInspectionTotal || len(items) != 12 {
		t.Fatalf("hard-limit page is wrong: %+v", view)
	}
	if _, more := view["next_offset"]; more {
		t.Fatalf("512-item hard limit must terminate even when client says truncated: %+v", view)
	}

	last := sanitizeInspection(&ihtml.ChatClientInspection{
		Total: 1000, Offset: 0, Limit: 20, Truncated: true, Items: clientItems,
	}, inspectQuery{offset: maxInspectionTotal, limit: 20})
	if last["offset"] != maxInspectionTotal || len(last["items"].([]map[string]any)) != 0 {
		t.Fatalf("offset=512 must be an empty terminal page: %+v", last)
	}
	if _, more := last["next_offset"]; more {
		t.Fatalf("offset=512 must not expose a next page: %+v", last)
	}

	stale := sanitizeInspection(&ihtml.ChatClientInspection{
		Total: 100, Offset: 0, Limit: 20, Truncated: true, Items: clientItems,
	}, inspectQuery{offset: 20, limit: 20})
	if stale["offset"] != 20 || stale["next_offset"] != 40 {
		t.Fatalf("client Offset must not rewind pagination: %+v", stale)
	}

	empty := sanitizeInspection(&ihtml.ChatClientInspection{
		Total: 100, Offset: 0, Limit: 20, Truncated: true,
	}, inspectQuery{offset: 20, limit: 20})
	if _, more := empty["next_offset"]; more {
		t.Fatalf("an empty untrusted page must terminate instead of repeating its offset: %+v", empty)
	}

	if q := normalizeInspectQuery(map[string]any{"offset": 9999}); q.offset != maxInspectionTotal {
		t.Fatalf("request offset was not capped: %+v", q)
	}
}
