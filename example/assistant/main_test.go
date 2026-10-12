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
	"github.com/zdypro888/ihtml/agent"
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
	if err != nil || models.ModelReady || !models.PreviewOnly || models.DeepApprovalResume != "manual_tool_only" {
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

func TestDemoRejectsUIHTTPMutations(t *testing.T) {
	h, err := newDemo(context.Background(), false, func(string) string { t.Fatal("preview read API credentials"); return "" })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	routes := demoRoutes(h)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		request := httptest.NewRequest(method, "/api/items", strings.NewReader(`{"id":"forged"}`))
		response := httptest.NewRecorder()
		routes.ServeHTTP(response, request)
		if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "GET, HEAD" {
			t.Fatalf("%s UI mutation was not blocked: %d", method, response.Code)
		}
	}
}

func TestPreviewConfirmationAndCancellation(t *testing.T) {
	h, err := newDemo(context.Background(), false, func(string) string { t.Fatal("preview read API credentials"); return "" })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	server := httptest.NewServer(demoRoutes(h))
	defer server.Close()
	connect := func() *websocket.Conn {
		ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/chat/ws", nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ws.Close() })
		return ws
	}
	read := func(ws *websocket.Conn) ihtml.ChatStreamEvent {
		_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
		var ev ihtml.ChatStreamEvent
		if err := ws.ReadJSON(&ev); err != nil {
			t.Fatal(err)
		}
		return ev
	}
	write := func(ws *websocket.Conn, ev ihtml.ChatClientEvent) {
		if err := ws.WriteJSON(ev); err != nil {
			t.Fatal(err)
		}
	}
	user := func(ws *websocket.Conn, text string) {
		write(ws, ihtml.ChatClientEvent{Type: ihtml.ChatClientUserMessage, Messages: []ihtml.ChatMessage{{Role: "user", Content: text}}})
	}
	finish := func(ws *websocket.Conn) []ihtml.ChatStreamEvent {
		var events []ihtml.ChatStreamEvent
		for {
			ev := read(ws)
			events = append(events, ev)
			if ev.Type == ihtml.ChatStreamAssistantDone || ev.Type == ihtml.ChatStreamTurnError {
				return events
			}
		}
	}
	status := func(ws *websocket.Conn, expected string) {
		user(ws, "status")
		events := finish(ws)
		if last := events[len(events)-1]; last.Type != ihtml.ChatStreamAssistantDone || !strings.Contains(last.Message, "Scratch marker: "+expected+".") {
			t.Fatalf("unexpected marker: %+v", last)
		}
	}
	propose := func(ws *websocket.Conn) *ihtml.ProposedToolCall {
		user(ws, "mark")
		var call *ihtml.ProposedToolCall
		for _, ev := range finish(ws) {
			if ev.Type == ihtml.ChatStreamConfirmationRequired {
				call = ev.ToolCall
			}
			if ev.Type == ihtml.ChatStreamToolStart || ev.Type == ihtml.ChatStreamToolResult && ev.ToolStatus == ihtml.ChatToolSuccess {
				t.Fatal("proposal falsely reported execution")
			}
		}
		if call == nil || call.ApprovalResume != "manual_tool_only" || call.ConfirmationToken == "" {
			t.Fatalf("missing signed manual card: %+v", call)
		}
		return call
	}
	ws := connect()
	call := propose(ws)
	status(ws, "unmarked") // The original handler has not executed.
	write(ws, ihtml.ChatClientEvent{Type: ihtml.ChatClientConfirmTool, ToolCall: call})
	events := finish(ws)
	started, succeeded := false, false
	for _, ev := range events {
		if ev.Type == ihtml.ChatStreamToolStart {
			started = true
		}
		if ev.Type == ihtml.ChatStreamToolResult && ev.ToolStatus == ihtml.ChatToolSuccess {
			succeeded = true
		}
	}
	if !started || !succeeded || events[len(events)-1].Type != ihtml.ChatStreamAssistantDone {
		t.Fatalf("confirmation failed: %+v", events)
	}
	status(ws, "marked")
	write(ws, ihtml.ChatClientEvent{Type: ihtml.ChatClientConfirmTool, ToolCall: call})
	if last := finish(ws); last[len(last)-1].Type != ihtml.ChatStreamTurnError {
		t.Fatal("duplicate approval accepted")
	}
	_ = ws.Close()
	other := connect()
	status(other, "unmarked") // A fresh connection does not restore scratch state.
	call = propose(other)
	write(other, ihtml.ChatClientEvent{Type: ihtml.ChatClientCancelTool, ToolCall: call})
	ack := read(other)
	if ack.Type != ihtml.ChatStreamCancelToolResult || ack.ToolStatus != ihtml.ChatToolCancelled || ack.ToolID != call.ID || ack.ConfirmationNonce != call.ConfirmationNonce {
		t.Fatalf("bad cancellation ACK: %+v", ack)
	}
	status(other, "unmarked")
	write(other, ihtml.ChatClientEvent{Type: ihtml.ChatClientConfirmTool, ToolCall: call})
	if last := finish(other); last[len(last)-1].Type != ihtml.ChatStreamTurnError {
		t.Fatal("cancelled approval accepted")
	}
	status(other, "unmarked")
}

func TestPrepareInjectsTrustedScratchStateAndGuardRestrictsCapability(t *testing.T) {
	host := &demoHost{states: make(map[*http.Request]*hostState)}
	ctx := context.Background()
	sc := &ihtml.ChatSessionContext{User: "loopback-demo", Request: httptest.NewRequest(http.MethodGet, "/api/chat/ws", nil), ScopedService: struct{ ihtml.ScopedService }{}}
	event := &ihtml.ChatClientEvent{Messages: []ihtml.ChatMessage{{Role: "user", Content: "status"}}}
	request, err := host.prepare(ctx, sc, event)
	if err != nil || !strings.Contains(request.System, "Current connection scratch marker: unmarked.") {
		t.Fatalf("missing trusted initial fact: %+v, %v", request, err)
	}
	host.mu.Lock()
	host.states[sc.Request].marker = "marked"
	host.states[sc.Request].engineSession = "existing-native-session"
	host.mu.Unlock()
	request, err = host.prepare(ctx, sc, event)
	if err != nil || request.EngineSession != "existing-native-session" || !strings.Contains(request.System, "Current connection scratch marker: marked.") {
		t.Fatalf("existing runtime did not receive trusted current fact: %+v, %v", request, err)
	}
	tool := agent.Tool{Name: "set_demo_marker", Effect: agent.ToolEffectWrite, RequiredAction: "demo:scratch", ApprovalRequired: true}
	if err := host.guard(ctx, sc, tool, json.RawMessage(`{"value":"marked"}`)); err != nil {
		t.Fatal(err)
	}
	for _, args := range []string{`{"value":"other"}`, `{"value":"marked","path":"/business"}`, `null`} {
		if host.guard(ctx, sc, tool, json.RawMessage(args)) == nil {
			t.Fatalf("unsafe scratch args accepted: %s", args)
		}
	}
	if host.guard(ctx, sc, agent.Tool{Name: "modify_ui", Effect: agent.ToolEffectWrite}, json.RawMessage(`{}`)) == nil {
		t.Fatal("UI write capability accepted")
	}
	sc.Access.ReadOnly = true
	if host.guard(ctx, sc, tool, json.RawMessage(`{"value":"marked"}`)) == nil {
		t.Fatal("read-only scratch write accepted")
	}
}
