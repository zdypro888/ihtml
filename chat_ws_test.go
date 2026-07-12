package ihtml

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeChatBackend: 收到 user_message 就回 turn_start + delta + done,
// 用于验证 WS 通道与事件编解码, 不碰真模型。
type fakeChatBackend struct{}

type fakeChatSession struct {
	sctx *ChatSessionContext
}

func (b *fakeChatBackend) NewSession(_ context.Context, sctx *ChatSessionContext) (ChatSession, error) {
	return &fakeChatSession{sctx: sctx}, nil
}

func (s *fakeChatSession) HandleEvent(ctx context.Context, ev *ChatClientEvent) {
	if ev.Type != "user_message" {
		return
	}
	_ = s.sctx.Emit(ChatStreamEvent{Type: "turn_start", ModelReady: true})
	// 顺手验证 Service 可用: 写一个 Item
	_ = s.sctx.Service.PutItems(ctx, s.sctx.User, []Item{{ID: "from-ai", Type: ItemHTML, Content: "<i>hi</i>"}}, "chat", "fake")
	_ = s.sctx.Emit(ChatStreamEvent{Type: "assistant_delta", Delta: "<p>你好 " + s.sctx.User + "</p>", ModelReady: true})
	_ = s.sctx.Emit(ChatStreamEvent{Type: "assistant_done", Message: "<p>你好</p>", ModelReady: true})
}

func (s *fakeChatSession) Close() {}

func TestChatWebSocket(t *testing.T) {
	h := New(NewMemoryStore(), WithUserResolver(DevelopmentUserResolver), WithChatBackend(&fakeChatBackend{}))
	srv := httptest.NewServer(h)
	defer srv.Close()

	// config 应显示 chat 开启
	status, out := doJSON(t, "GET", srv.URL+"/api/config?uid=w1", "")
	if status != 200 || out["chat"] != true {
		t.Fatalf("config chat should be true: %d %v", status, out)
	}

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/chat/ws?uid=w1"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(ChatClientEvent{
		Type:     "user_message",
		Messages: []ChatMessage{{Role: "user", Content: "做个页面"}},
	}); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	deadline := time.Now().Add(3 * time.Second)
	for !got["assistant_done"] {
		_ = conn.SetReadDeadline(deadline)
		ev := ChatStreamEvent{}
		if err := conn.ReadJSON(&ev); err != nil {
			t.Fatalf("read ws (got=%v): %v", got, err)
		}
		got[ev.Type] = true
	}
	if !got["turn_start"] || !got["assistant_delta"] {
		t.Fatalf("missing events: %v", got)
	}
	// fake 会话通过 Service 写的 Item 应已入库
	items, err := h.ListItems(context.Background(), "w1")
	if err != nil || len(items) != 1 || items[0].ID != "from-ai" {
		t.Fatalf("service write from chat session failed: %v %v", items, err)
	}
}

func TestChatDisabled(t *testing.T) {
	srv, _ := newTestServer(t)
	status, _ := doJSON(t, "GET", srv.URL+"/api/chat/ws?uid=x", "")
	if status != 501 {
		t.Fatalf("chat ws without backend should 501, got %d", status)
	}
	status, out := doJSON(t, "GET", srv.URL+"/api/chat/models?uid=x", "")
	if status != 200 || out["model_ready"] != false {
		t.Fatalf("models without backend: %d %v", status, out)
	}
}
