package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/agent"
	"github.com/zdypro888/ihtml/progress"
)

func TestBackgroundTaskProgressAfterSuspendPersistsAndReplays(t *testing.T) {
	store := agent.NewMemoryTaskStore()
	scope := agent.TaskScope(&agent.TurnRequest{SessionID: "alice"})
	report, finish := make(chan struct{}), make(chan struct{})
	var modelCalls atomic.Int32
	cfg := configFor(&fakeEngine{run: func(ctx context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
		modelCalls.Add(1)
		_, err := req.Tasks.Start(ctx, scope, req.InvocationScope, "slow", req.Tools[0], json.RawMessage(`{"secret":"private"}`))
		if err != nil {
			return nil, err
		}
		wait, err := req.Tasks.Suspend(ctx, scope, req.InvocationScope, "pause", agent.TaskWaitSpec{Timeout: time.Hour, Checkpoint: "wait for original task"}, map[string]bool{"slow": true})
		return &agent.TurnResult{Text: "waiting", FinishReason: "task_waiting", Waiting: &wait}, err
	}})
	oldPrepare := cfg.PrepareTurn
	cfg.PrepareTurn = func(ctx context.Context, sc *ihtml.ChatSessionContext, event *ihtml.ChatClientEvent) (*agent.TurnRequest, error) {
		req, err := oldPrepare(ctx, sc, event)
		req.Tools = []agent.Tool{{Name: "slow", Title: "核对原资料", Effect: agent.ToolEffectRead, Handler: func(ctx context.Context, _ json.RawMessage) (string, error) {
			select {
			case <-report:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			agent.ReportProgress(ctx, progress.Snapshot{Message: "正在核对关联资料", Phase: "核对", State: "running", UpdatedAt: time.Date(2026, 11, 12, 16, 10, 0, 0, time.UTC)})
			select {
			case <-finish:
				return "PRIVATE final result", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}}}
		return req, err
	}
	cfg.Tasks = &agent.TaskConfig{Store: store, Scope: scope, PollInterval: 5 * time.Millisecond}
	var mu sync.Mutex
	saved := map[string]ToolHistoryRecord{}
	cfg.PersistToolHistory = func(_ context.Context, _ *ihtml.ChatSessionContext, next ToolHistoryRecord) error {
		mu.Lock()
		defer mu.Unlock()
		saved[next.ID] = MergeToolHistory(saved[next.ID], next)
		return nil
	}
	backend, err := NewChecked(cfg)
	if err != nil {
		t.Fatal(err)
	}
	hub, err := NewBackgroundBackend(BackgroundConfig{Backend: backend, Lifetime: t.Context(), Key: func(_ context.Context, sc *ihtml.ChatSessionContext) (string, error) { return sc.User, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	rec := newRecorder()
	conn := attachBackground(t, hub, t.Context(), "alice", "one", rec)
	send(conn, "begin")
	events := rec.wait(t, ihtml.ChatStreamAssistantDone, 1)
	flow := ""
	for _, event := range events {
		if event.Type == ihtml.ChatStreamAssistantDone {
			flow = event.FlowID
		}
	}
	if flow == "" {
		t.Fatal("missing original goal")
	}
	conn.Close()
	close(report)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(saved)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	replay := newRecorder()
	attachBackground(t, hub, t.Context(), "alice", "one", replay)
	events = replay.wait(t, ihtml.ChatStreamTaskProgress, 1)
	var live ihtml.ChatStreamEvent
	for _, event := range events {
		if event.Type == ihtml.ChatStreamTaskProgress {
			live = event
		}
	}
	if live.FlowID != flow || live.RunID != "" || live.TaskState != "running" || live.ToolTitle != "核对原资料" || !live.BackgroundReplay || modelCalls.Load() != 1 {
		t.Fatalf("unscoped/new model progress: %+v calls=%d", live, modelCalls.Load())
	}
	raw, _ := json.Marshal(live)
	if strings.Contains(string(raw), "private") || strings.Contains(string(raw), "PRIVATE") || live.Progress == nil || live.Progress.UpdatedAt.Hour() != 16 {
		t.Fatal(live)
	}
	close(finish)
	replay.wait(t, ihtml.ChatStreamTaskComplete, 1)
	events = replay.wait(t, ihtml.ChatStreamTaskProgress, 2)
	var terminal ihtml.ChatStreamEvent
	for _, event := range events {
		if event.Type == ihtml.ChatStreamTaskProgress {
			terminal = event
		}
	}
	if terminal.TaskID != live.TaskID || terminal.TaskState != "completed" || terminal.ToolStatus != ihtml.ChatToolAccepted || terminal.ToolStatus == ihtml.ChatToolSuccess {
		t.Fatal(terminal)
	}
	mu.Lock()
	history := saved[live.ToolID]
	mu.Unlock()
	if history.TaskID != live.TaskID || history.FlowID != flow || history.TaskState != "completed" || history.Status != ihtml.ChatToolAccepted || history.Progress == nil || len(history.Result) != 0 {
		t.Fatal(history)
	}
}

func TestTaskProgressHistoryAndLiveNeverRegressOrMoveGoals(t *testing.T) {
	rec := newRecorder()
	cfg := configFor(&fakeEngine{})
	var saved ToolHistoryRecord
	cfg.PersistToolHistory = func(_ context.Context, _ *ihtml.ChatSessionContext, next ToolHistoryRecord) error {
		saved = MergeToolHistory(saved, next)
		return nil
	}
	chat := openSession(t, cfg, "owner", rec).(*session)
	defer chat.Close()
	at := time.Now().UTC()
	snapshot := progress.Snapshot{Message: "checking", State: "running", UpdatedAt: at}
	info := agent.TaskInfo{ID: "task-a", GoalID: "original-goal", Title: "核对资料", Tool: "arbitrary", Background: true, State: "running", Progress: &snapshot}
	chat.publishTaskProgress(info)
	first := saved
	earlier := snapshot
	earlier.UpdatedAt = at.Add(-time.Minute)
	info.Progress = &earlier
	chat.publishTaskProgress(info)
	if len(rec.snapshot()) != 1 {
		t.Fatal("late progress emitted")
	}
	info.Progress = &snapshot
	info.State = "completed"
	chat.publishTaskProgress(info)
	info.State = "running"
	later := snapshot
	later.UpdatedAt = at.Add(time.Minute)
	info.Progress = &later
	chat.publishTaskProgress(info)
	info.GoalID = "different-goal"
	chat.publishTaskProgress(info)
	if len(rec.snapshot()) != 2 || saved.TaskState != "completed" || saved.Status != ihtml.ChatToolAccepted {
		t.Fatal(rec.snapshot(), saved)
	}
	next := first
	next.FlowID = "different-goal"
	next.UpdatedAt = &later.UpdatedAt
	if merged := MergeToolHistory(saved, next); merged.FlowID != "original-goal" || merged.TaskState != "completed" {
		t.Fatal(merged)
	}
	if merged := MergeToolHistory(saved, first); merged.TaskState != "completed" {
		t.Fatal("history regressed", merged)
	}
}

func TestTaskProgressDisplayFailureDoesNotCancelExecutionContext(t *testing.T) {
	cfg := configFor(&fakeEngine{})
	cfg.PersistToolHistory = func(context.Context, *ihtml.ChatSessionContext, ToolHistoryRecord) error {
		return errors.New("storage unavailable")
	}
	chat := openSession(t, cfg, "owner", newRecorder()).(*session)
	defer chat.Close()
	chat.publishTaskProgress(agent.TaskInfo{ID: "task", GoalID: "goal", Background: true, State: "running", Progress: &progress.Snapshot{Message: "working", State: "running", UpdatedAt: time.Now()}})
	if chat.ctx.Err() != nil {
		t.Fatal("display failed and canceled work", chat.ctx.Err())
	}
}

func TestTaskProgressUnknownCanBeResolvedOnlyByFinalReceipt(t *testing.T) {
	at := time.Now().UTC()
	snapshot := &progress.Snapshot{Message: "last checkpoint", State: "running", UpdatedAt: at}
	previous := ToolHistoryRecord{ID: "task-progress/original", TaskID: "original", TaskState: "unknown", FlowID: "goal", Status: ihtml.ChatToolUnknown, Progress: snapshot, UpdatedAt: &at}
	next := previous
	next.TaskState = "running"
	next.Status = ihtml.ChatToolRunning
	if got := MergeToolHistory(previous, next); got.TaskState != "unknown" {
		t.Fatal("unknown revived", got)
	}
	for _, state := range []string{"completed", "failed", "cancelled"} {
		next = previous
		next.TaskState = state
		next.Status = ihtml.ChatToolAccepted
		if got := MergeToolHistory(previous, next); got.TaskState != state {
			t.Fatal("authoritative final result ignored", got)
		}
	}
}

func TestRestoreTaskProgressUsesAuthoritativeReceiptWithoutMovingAnchor(t *testing.T) {
	sourceAt := time.Now().Add(-time.Minute).UTC()
	displayAt := sourceAt.Add(time.Second)
	snapshot := &progress.Snapshot{Message: "last reported phase", State: "running", UpdatedAt: sourceAt.Add(-time.Second)}
	original := ToolHistoryRecord{ID: "task-progress/original", TaskID: "original", TaskState: "running", FlowID: "goal", AfterMessageID: "original-message", Call: ihtml.ProposedToolCall{ID: "task-progress/original", Name: "read", Title: "读取资料"}, Status: ihtml.ChatToolRunning, Progress: snapshot, UpdatedAt: &displayAt}
	info := agent.TaskInfo{ID: "original", GoalID: "goal", Tool: "read", Background: true, State: "completed", Progress: snapshot, Result: "PRIVATE body", Error: "PRIVATE error", Input: json.RawMessage(`{"private":"value"}`)}
	restored := RestoreTaskProgressHistory(original, info, sourceAt)
	if restored.TaskState != "completed" || restored.Status != ihtml.ChatToolAccepted || restored.AfterMessageID != original.AfterMessageID || restored.ID != original.ID || !reflect.DeepEqual(restored.Call, original.Call) || !restored.UpdatedAt.Equal(sourceAt) {
		t.Fatal(restored)
	}
	raw, _ := json.Marshal(restored)
	if strings.Contains(string(raw), "PRIVATE") || strings.Contains(string(raw), "private") {
		t.Fatal(string(raw))
	}
	info.GoalID = "other-goal"
	if got := RestoreTaskProgressHistory(original, info, sourceAt); got.TaskState != "running" || got.FlowID != "goal" {
		t.Fatal("foreign goal replaced source", got)
	}
}
