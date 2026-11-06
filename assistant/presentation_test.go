package assistant

import (
	"context"
	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/agent"
	"sync"
	"testing"
)

// 显示归属由框架产生，完成钩子与公开操作记录共享身份，不从模型文本推断目标。
func TestPresentationIdentitySharedByHooksAndPublicEvents(t *testing.T) {
	r := newRecorder()
	cfg := configFor(&fakeEngine{run: func(ctx context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
		req.OnEvent(agent.Step{Kind: agent.StepToolCall, ToolName: "arbitrary_read", Result: `{"state":"ready"}`})
		return &agent.TurnResult{Text: "snapshot"}, nil
	}})
	var mu sync.Mutex
	var identity PresentationIdentity
	var records []ToolHistoryRecord
	cfg.CompleteTurn = func(ctx context.Context, _ *ihtml.ChatSessionContext, _ *agent.TurnRequest, _ *agent.TurnResult, _ error) error {
		mu.Lock()
		identity = Presentation(ctx)
		mu.Unlock()
		return nil
	}
	cfg.PersistToolHistory = func(_ context.Context, _ *ihtml.ChatSessionContext, record ToolHistoryRecord) error {
		mu.Lock()
		records = append(records, record)
		mu.Unlock()
		return nil
	}
	s := openSession(t, cfg, "owner", r)
	send(s, "question")
	events := r.wait(t, ihtml.ChatStreamAssistantDone, 1)
	mu.Lock()
	defer mu.Unlock()
	if identity.FlowID == "" || identity.RunID == "" || len(records) != 1 || records[0].FlowID != identity.FlowID {
		t.Fatal(identity, records)
	}
	for _, event := range events {
		if event.Type != ihtml.ChatStreamTurnStart && (event.FlowID != identity.FlowID || event.RunID != identity.RunID) {
			t.Fatal("event/hook identities differ", event)
		}
	}
}
