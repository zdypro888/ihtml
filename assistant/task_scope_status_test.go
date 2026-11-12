package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/agent"
)

func TestTaskScopeStatusDisplayFailureDoesNotCancelWork(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	runtime, err := agent.NewTaskRuntime(ctx, agent.TaskConfig{Scope: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	s := &session{ctx: ctx, cancel: cancel, tasks: runtime, sctx: ihtml.ChatSessionContext{Emit: func(ihtml.ChatStreamEvent) error { return errors.New("offline") }}}
	s.publishTaskScopeStatus()
	if ctx.Err() != nil {
		t.Fatal("display failure cancelled runtime")
	}
}

func TestBackgroundTaskScopeStatusClearsOnCompletionWithoutProgress(t *testing.T) {
	cfg := configFor(&fakeEngine{run: func(context.Context, *agent.TurnRequest) (*agent.TurnResult, error) {
		t.Error("snapshot invoked model")
		return nil, nil
	}})
	cfg.Tasks = &agent.TaskConfig{Store: agent.NewMemoryTaskStore(), Scope: "owner"}
	backend, err := NewChecked(cfg)
	if err != nil {
		t.Fatal(err)
	}
	hub, err := NewBackgroundBackend(BackgroundConfig{Backend: backend, Lifetime: t.Context(), Key: func(_ context.Context, sc *ihtml.ChatSessionContext) (string, error) { return sc.User, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	first := attachBackground(t, hub, t.Context(), "alice", "one", newRecorder())
	s := first.(*backgroundAttachment).session.inner.(*session)
	finish := make(chan struct{})
	_, err = s.tasks.Start(t.Context(), "owner", "turn", "key", agent.Tool{Name: "slow", Effect: agent.ToolEffectRead, Handler: func(ctx context.Context, _ json.RawMessage) (string, error) {
		select {
		case <-finish:
			return "done", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	first.Close()
	log := newRecorder()
	attachBackground(t, hub, t.Context(), "alice", "one", log)
	events := log.wait(t, ihtml.ChatStreamTaskScopeStatus, 1)
	if !lastTaskScopeEvent(events).TaskScope.CanStop {
		t.Fatal(events)
	}
	close(finish)
	events = log.wait(t, ihtml.ChatStreamTaskScopeStatus, 2)
	if lastTaskScopeEvent(events).TaskScope.CanStop {
		t.Fatal("completed task retained stop control", events)
	}
}

func TestBackgroundTaskScopeStatusRestoresStopAndClearsAfterCancel(t *testing.T) {
	cfg := configFor(&fakeEngine{run: func(context.Context, *agent.TurnRequest) (*agent.TurnResult, error) {
		t.Error("snapshot invoked model")
		return nil, nil
	}})
	cfg.Tasks = &agent.TaskConfig{Store: agent.NewMemoryTaskStore(), Scope: "owner"}
	backend, err := NewChecked(cfg)
	if err != nil {
		t.Fatal(err)
	}
	hub, err := NewBackgroundBackend(BackgroundConfig{Backend: backend, Lifetime: t.Context(), Key: func(_ context.Context, sc *ihtml.ChatSessionContext) (string, error) { return sc.User, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	first := newRecorder()
	conn := attachBackground(t, hub, t.Context(), "alice", "one", first)
	e := conn.(*backgroundAttachment).session
	s := e.inner.(*session)
	info, err := s.tasks.Start(t.Context(), "owner", "turn", "key", agent.Tool{Name: "slow", Effect: agent.ToolEffectRead, Handler: func(ctx context.Context, _ json.RawMessage) (string, error) { <-ctx.Done(); return "", ctx.Err() }}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.tasks.Suspend(t.Context(), "owner", "turn", "wait", agent.TaskWaitSpec{IDs: []string{info.ID}, Timeout: time.Hour, Checkpoint: "original task"}, map[string]bool{"slow": true})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.publish(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamAssistantDone, RunID: "old-run", FinishReason: "task_waiting"}); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	replay := newRecorder()
	resumed := attachBackground(t, hub, t.Context(), "alice", "one", replay)
	events := replay.wait(t, ihtml.ChatStreamTaskScopeStatus, 1)
	last := lastTaskScopeEvent(events)
	if last.Type != ihtml.ChatStreamTaskScopeStatus || last.BackgroundReplay || last.TaskScope == nil || !last.TaskScope.CanStop || last.TaskScope.ActiveTasks != 1 || last.TaskScope.ActiveWaits != 1 {
		t.Fatal("missing authoritative resumed stop", events)
	}
	resumed.HandleEvent(t.Context(), &ihtml.ChatClientEvent{Type: ihtml.ChatClientCancel})
	events = replay.wait(t, ihtml.ChatStreamTaskScopeStatus, 2)
	last = lastTaskScopeEvent(events)
	if last.TaskScope == nil || last.TaskScope.CanStop || last.TaskScope.ActiveTasks != 0 || last.TaskScope.ActiveWaits != 0 {
		t.Fatal("cancel did not clear scope", events)
	}
	resumed.Close()
	afterCancel := newRecorder()
	attachBackground(t, hub, t.Context(), "alice", "one", afterCancel)
	events = afterCancel.wait(t, ihtml.ChatStreamTaskScopeStatus, 1)
	last = lastTaskScopeEvent(events)
	if last.BackgroundReplay || last.TaskScope == nil || last.TaskScope.CanStop {
		t.Fatal("old replay revived stopped task", events)
	}
	for _, event := range events {
		if event.Type == ihtml.ChatStreamTaskScopeStatus && event.BackgroundReplay {
			t.Fatal("replayed obsolete authority", event)
		}
	}
}

func lastTaskScopeEvent(events []ihtml.ChatStreamEvent) ihtml.ChatStreamEvent {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == ihtml.ChatStreamTaskScopeStatus {
			return events[i]
		}
	}
	return ihtml.ChatStreamEvent{}
}
