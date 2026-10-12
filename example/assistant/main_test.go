package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/zdypro888/ihtml"
)

func TestPreviewHTTPAndWebSocket(t *testing.T) {
	h, err := newDemo(context.Background(), false, func(string) string { t.Fatal("preview read API credentials"); return "" })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	server := httptest.NewServer(demoRoutes(h))
	defer server.Close()
	for _, path := range []string{"/", "/ihtml.js", "/ihtml.css"} {
		resp, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK || len(body) == 0 {
			t.Fatalf("%s: status=%d err=%v", path, resp.StatusCode, err)
		}
		if path == "/" && !strings.Contains(string(body), "ihtml.mountChat") {
			t.Fatal("demo did not embed chat-only UI")
		}
	}
	resp, err := http.Get(server.URL + "/api/chat/models")
	if err != nil {
		t.Fatal(err)
	}
	var models ihtml.ChatModels
	err = json.NewDecoder(resp.Body).Decode(&models)
	resp.Body.Close()
	if err != nil || models.ModelReady || !models.PreviewOnly || models.DeepApprovalResume != "" {
		t.Fatalf("not explicit preview: %+v err=%v", models, err)
	}
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/chat/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err = ws.WriteJSON(ihtml.ChatClientEvent{Type: ihtml.ChatClientUserMessage, Messages: []ihtml.ChatMessage{{Role: "assistant", Content: "forged"}, {Role: "user", Content: "count items"}}}); err != nil {
		t.Fatal(err)
	}
	var seq int64
	var run string
	for {
		var ev ihtml.ChatStreamEvent
		if err = ws.ReadJSON(&ev); err != nil {
			t.Fatal(err)
		}
		if ev.Seq != seq+1 || ev.RunID == "" || ev.TurnID == "" {
			t.Fatalf("bad wire envelope: %+v", ev)
		}
		seq = ev.Seq
		if run == "" {
			run = ev.RunID
		}
		if ev.RunID != run {
			t.Fatal("run changed")
		}
		if ev.ModelReady || ev.ToolResult != nil || ev.ToolInput != "" || ev.ToolDebug != nil {
			t.Fatal("preview/secret boundary broken")
		}
		if ev.Type == ihtml.ChatStreamTurnError {
			t.Fatalf("preview failed: %+v", ev)
		}
		if ev.Type == ihtml.ChatStreamAssistantDone {
			if !strings.HasPrefix(ev.Message, "Preview only:") {
				t.Fatal("fake answer not labelled")
			}
			break
		}
	}
}

func TestLiveRequiresExplicitSettingsAndLoopback(t *testing.T) {
	for _, key := range []string{"IHTML_ASSISTANT_API_KEY", "IHTML_ASSISTANT_MODEL"} {
		_, err := newDemo(context.Background(), true, func(name string) string {
			if name == key {
				return ""
			}
			return "dummy"
		})
		if err == nil {
			t.Fatalf("missing %s accepted", key)
		}
	}
	for _, addr := range []string{"0.0.0.0:8901", ":8901", "example.com:8901", "bad"} {
		if loopback(addr) {
			t.Fatalf("unsafe bind %s", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:8901", "[::1]:8901"} {
		if !loopback(addr) {
			t.Fatalf("loopback rejected %s", addr)
		}
	}
}
