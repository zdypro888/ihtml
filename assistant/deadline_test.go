package assistant

import (
	"context"
	"encoding/json"
	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/agent"
	"testing"
	"time"
)

func TestTrustedPreviewDeadlineSurvivesBridge(t *testing.T) {
	cfg := approvalConfig(t, func(context.Context, json.RawMessage) (string, error) { t.Fatal("unconfirmed write"); return "", nil })
	deadline := time.Now().Add(time.Minute).Unix()
	cfg.Confirmation.Preview = func(context.Context, *ihtml.ChatSessionContext, agent.Tool, json.RawMessage) (ihtml.ProposedToolCall, error) {
		return ihtml.ProposedToolCall{Title: "Deadline fixture", Method: "POST", Path: "/fixture", ConfirmationExpires: deadline}, nil
	}
	r := newRecorder()
	s := openSession(t, cfg, "u", r)
	defer s.Close()
	card := proposedCard(t, s, r)
	if card.ConfirmationExpires != deadline {
		t.Fatal("host deadline replaced with a fresh TTL", card.ConfirmationExpires, deadline)
	}
}

func TestPreviewRefreshReturnsNoCardAndDoesNotExecute(t *testing.T) {
	cfg := approvalConfig(t, func(context.Context, json.RawMessage) (string, error) { t.Fatal("unconfirmed write"); return "", nil })
	cfg.Confirmation.Preview = func(context.Context, *ihtml.ChatSessionContext, agent.Tool, json.RawMessage) (ihtml.ProposedToolCall, error) {
		return ihtml.ProposedToolCall{}, ErrPreviewRefreshRequired
	}
	r := newRecorder()
	s := openSession(t, cfg, "u", r)
	defer s.Close()
	send(s, "propose")
	events := r.wait(t, ihtml.ChatStreamAssistantDone, 1)
	for _, ev := range events {
		if ev.Type == ihtml.ChatStreamConfirmationRequired || ev.Type == ihtml.ChatStreamTurnError {
			t.Fatal("refresh issued a stale card or failed the turn", ev)
		}
	}
	found := false
	for _, ev := range events {
		if ev.Type == ihtml.ChatStreamToolResult {
			found = true
			if ev.ToolStatus != ihtml.ChatToolCancelled {
				t.Fatal("refresh misreported as execution", ev)
			}
		}
	}
	if !found {
		t.Fatal("missing non-execution receipt")
	}
}
