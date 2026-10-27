package assistant

import (
	"context"
	"testing"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/agent"
)

// 宿主释放租约通常发生在 CompleteTurn；此时不能把已经成功的轮次误报为取消。
func TestTurnLifetimeReleaseDuringCompletionPreservesSuccess(t *testing.T) {
	lifetime, release := context.WithCancel(context.Background())
	defer release()
	cfg := configFor(&fakeEngine{run: func(context.Context, *agent.TurnRequest) (*agent.TurnResult, error) {
		return &agent.TurnResult{Text: "completed"}, nil
	}})
	cfg.TurnLifetime = func(context.Context, *ihtml.ChatSessionContext, *agent.TurnRequest) (context.Context, error) {
		return lifetime, nil
	}
	cfg.CompleteTurn = func(context.Context, *ihtml.ChatSessionContext, *agent.TurnRequest, *agent.TurnResult, error) error {
		release()
		return nil
	}
	r := newRecorder()
	s := openSession(t, cfg, "user", r)
	send(s, "fixture")
	events := r.wait(t, ihtml.ChatStreamAssistantDone, 1)
	for _, event := range events {
		if event.Type == ihtml.ChatStreamTurnError {
			t.Fatal("completion became cancellation")
		}
	}
}

func TestTurnLifetimeStopsExecution(t *testing.T) {
	lifetime, release := context.WithCancel(context.Background())
	defer release()
	started := make(chan struct{})
	cfg := configFor(&fakeEngine{run: func(ctx context.Context, _ *agent.TurnRequest) (*agent.TurnResult, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	cfg.TurnLifetime = func(context.Context, *ihtml.ChatSessionContext, *agent.TurnRequest) (context.Context, error) {
		return lifetime, nil
	}
	r := newRecorder()
	s := openSession(t, cfg, "user", r)
	send(s, "fixture")
	waitSignal(t, started)
	release()
	r.wait(t, ihtml.ChatStreamTurnError, 1)
}

func TestTurnLifetimeCannotDetachUserCancellation(t *testing.T) {
	started := make(chan struct{})
	cfg := configFor(&fakeEngine{run: func(ctx context.Context, _ *agent.TurnRequest) (*agent.TurnResult, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	cfg.TurnLifetime = func(context.Context, *ihtml.ChatSessionContext, *agent.TurnRequest) (context.Context, error) {
		return context.Background(), nil
	}
	r := newRecorder()
	s := openSession(t, cfg, "user", r)
	send(s, "fixture")
	waitSignal(t, started)
	s.HandleEvent(context.Background(), &ihtml.ChatClientEvent{Type: ihtml.ChatClientCancel})
	r.wait(t, ihtml.ChatStreamTurnError, 1)
}

func TestNewToolContinuationRequiresSuccessfulExecution(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	if NewToolContinuation(ctx, nil) != nil || NewToolContinuation(ctx, &ConfirmedToolResult{}) != nil {
		t.Fatal("unexecuted action continued")
	}
	if event := NewToolContinuation(ctx, &ConfirmedToolResult{Started: true}); event == nil || event.Type != ihtml.ChatClientUserMessage || len(event.Messages) != 1 {
		t.Fatal("missing generic continuation")
	}
	cancel()
	if NewToolContinuation(ctx, &ConfirmedToolResult{Started: true}) != nil {
		t.Fatal("cancelled turn continued")
	}
}
