package chat

import (
	"context"
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

	result, summary, err := executeInspect(context.Background(), rt)
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
	if !strings.Contains(summary, "1 个 Item 在渲染") || !strings.Contains(summary, "1 个未渲染") {
		t.Fatalf("summary: %q", summary)
	}
}

func TestInspectTimesOutWhenPageOffline(t *testing.T) {
	var mu sync.Mutex
	var emitted []ihtml.ChatStreamEvent
	rt := inspectRuntime(&emitted, &mu)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	result, summary, err := executeInspect(ctx, rt)
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
