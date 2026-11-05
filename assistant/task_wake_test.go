package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/agent"
)

// 登记后可关闭原会话，再由同一持久存储恢复等待；等待期间不调用模型。
func TestTaskWakeSurvivesSessionRecreationAndUsesFreshTurn(t *testing.T) {
	store := agent.NewMemoryTaskStore()
	scope := agent.TaskScope(&agent.TurnRequest{SessionID: "alice", SessionCapability: "cap"})
	var calls atomic.Int32
	firstEngine := &fakeEngine{run: func(ctx context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
		calls.Add(1)
		wait, err := req.Tasks.Suspend(ctx, scope, req.InvocationScope, "timer", agent.TaskWaitSpec{Timeout: 80 * time.Millisecond, Checkpoint: "original goal with fixed targets"}, map[string]bool{})
		return &agent.TurnResult{Text: "waiting", FinishReason: "task_waiting", Waiting: &wait}, err
	}}
	cfg := configFor(firstEngine)
	prepare := cfg.PrepareTurn
	cfg.PrepareTurn = func(ctx context.Context, sc *ihtml.ChatSessionContext, ev *ihtml.ChatClientEvent) (*agent.TurnRequest, error) {
		req, err := prepare(ctx, sc, ev)
		req.SessionCapability = "cap"
		req.Tools = []agent.Tool{{Name: "read", Effect: agent.ToolEffectRead, Handler: func(context.Context, json.RawMessage) (string, error) { return "read", nil }}}
		return req, err
	}
	cfg.TasksProvider = func(context.Context, *ihtml.ChatSessionContext) (agent.TaskConfig, error) {
		return agent.TaskConfig{Store: store, Scope: scope, PollInterval: 5 * time.Millisecond, LeaseDuration: 100 * time.Millisecond}, nil
	}
	recorder1 := newRecorder()
	s1 := openSession(t, cfg, "alice", recorder1)
	send(s1, "start")
	recorder1.wait(t, ihtml.ChatStreamAssistantDone, 1)
	s1.Close()
	if calls.Load() != 1 {
		t.Fatal("waiting consumed model turns", calls.Load())
	}
	cfg.Engine = &fakeEngine{run: func(ctx context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
		calls.Add(1)
		if req.WakeID == "" || agent.TaskWakeID(ctx) != req.WakeID || !strings.Contains(req.UserText, "original goal with fixed targets") {
			t.Errorf("lost trusted wake/context: %+v", req)
		}
		if req.InvocationScope != "wake/"+req.WakeID {
			t.Error("wake invocation not stable")
		}
		return &agent.TurnResult{Text: "resumed using current tools"}, nil
	}}
	recorder2 := newRecorder()
	s2 := openSession(t, cfg, "alice", recorder2)
	recorder2.wait(t, ihtml.ChatStreamAssistantDone, 1)
	if calls.Load() != 2 {
		t.Fatal("duplicate wake/model call", calls.Load())
	}
	s2.Close()
	rows, err := store.ListWaits(t.Context(), scope, agent.TaskScan{Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].State != "delivered" {
		t.Fatal(rows, err)
	}
}

func TestTaskWakeChecksLivePermissionBeforeDelivery(t *testing.T) {
	store := agent.NewMemoryTaskStore()
	runtime, err := agent.NewTaskRuntime(t.Context(), agent.TaskConfig{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	_, err = runtime.Suspend(t.Context(), "scope", "turn", "timer", agent.TaskWaitSpec{Timeout: time.Millisecond, Checkpoint: "read original results"}, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	cfg := configFor(&fakeEngine{run: func(context.Context, *agent.TurnRequest) (*agent.TurnResult, error) {
		calls.Add(1)
		return &agent.TurnResult{Text: "should not run"}, nil
	}})
	cfg.TasksProvider = func(context.Context, *ihtml.ChatSessionContext) (agent.TaskConfig, error) {
		return agent.TaskConfig{Store: store, Scope: "scope", PollInterval: time.Millisecond}, nil
	}
	guarded := make(chan struct{}, 1)
	cfg.TaskWakeGuard = func(context.Context, *ihtml.ChatSessionContext) error {
		select {
		case guarded <- struct{}{}:
		default:
		}
		return ihtml.ErrAccessDenied
	}
	s := openSession(t, cfg, "alice", newRecorder())
	select {
	case <-guarded:
	case <-time.After(time.Second):
		t.Fatal("wake guard not consulted")
	}
	// guard 返回后才写取消状态；先等待持久结果，避免 Close 取消正在执行的存储请求。
	deadline := time.Now().Add(time.Second)
	for {
		rows, err := store.ListWaits(t.Context(), "scope", agent.TaskScan{Limit: 10})
		if err == nil && len(rows) == 1 && rows[0].State == "cancelled" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("revoked wait was not cancelled", rows, err)
		}
		time.Sleep(time.Millisecond)
	}
	s.Close()
	if calls.Load() != 0 {
		t.Fatal("revoked session woke model")
	}
	rows, _ := store.ListWaits(t.Context(), "scope", agent.TaskScan{Limit: 10})
	if rows[0].State != "cancelled" {
		t.Fatal(rows)
	}
}

// 后台预览晚于模型挂起返回时仍可发出原范围确认卡，不能丢弃或扩张授权。
func TestBackgroundProposalCanArriveAfterModelTurnEnds(t *testing.T) {
	rec := newRecorder()
	release := make(chan struct{})
	defer close(release)
	var writes atomic.Int32
	cfg := approvalConfig(t, func(context.Context, json.RawMessage) (string, error) { writes.Add(1); return "actual", nil })
	cfg.Tasks = &agent.TaskConfig{}
	oldPreview := cfg.Confirmation.Preview
	cfg.Confirmation.Preview = func(ctx context.Context, sc *ihtml.ChatSessionContext, tool agent.Tool, args json.RawMessage) (ihtml.ProposedToolCall, error) {
		select {
		case <-release:
			return oldPreview(ctx, sc, tool, args)
		case <-ctx.Done():
			return ihtml.ProposedToolCall{}, ctx.Err()
		}
	}
	cfg.Engine = &fakeEngine{run: func(ctx context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
		_, err := req.Tasks.Start(ctx, agent.TaskScope(req), req.InvocationScope, "slow-preview", req.Tools[0], json.RawMessage(`{"target":"fixed"}`))
		return &agent.TurnResult{Text: "waiting for background preparation"}, err
	}}
	s := openSession(t, cfg, "alice", rec)
	send(s, "propose")
	rec.wait(t, ihtml.ChatStreamAssistantDone, 1)
	release <- struct{}{}
	events := rec.wait(t, ihtml.ChatStreamConfirmationRequired, 1)
	var card ihtml.ProposedToolCall
	for _, event := range events {
		if event.ToolCall != nil {
			card = *event.ToolCall
		}
	}
	if card.ConfirmationNonce == "" || writes.Load() != 0 {
		t.Fatal(card, writes.Load())
	}
	confirmCard(s, card)
	rec.wait(t, ihtml.ChatStreamAssistantDone, 3)
	if writes.Load() != 1 {
		t.Fatal("late card did not execute once", writes.Load())
	}
}

// 浏览器即使知道等待编号，也不能用 RequestID 冒充服务端恢复事件。
func TestTaskWakeRequestIDDoesNotGrantInternalEventProvenance(t *testing.T) {
	store := agent.NewMemoryTaskStore()
	runtime, err := agent.NewTaskRuntime(t.Context(), agent.TaskConfig{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	wait, err := runtime.Suspend(t.Context(), "scope", "turn", "wait", agent.TaskWaitSpec{Timeout: time.Hour, Checkpoint: "original facts"}, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	cfg := configFor(&fakeEngine{run: func(ctx context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
		if req.WakeID != "" || agent.TaskWakeID(ctx) != "" {
			t.Error("browser request acquired wake identity")
		}
		return &agent.TurnResult{Text: "ordinary user request"}, nil
	}})
	cfg.TasksProvider = func(context.Context, *ihtml.ChatSessionContext) (agent.TaskConfig, error) {
		return agent.TaskConfig{Store: store, Scope: "scope"}, nil
	}
	rec := newRecorder()
	s := openSession(t, cfg, "alice", rec).(*session)
	s.mu.Lock()
	s.taskWakes = map[string]taskWakeState{"task-wake/" + wait.ID: {record: wait, consumer: "server-only"}}
	s.mu.Unlock()
	s.HandleEvent(t.Context(), &ihtml.ChatClientEvent{Type: ihtml.ChatClientUserMessage, RequestID: "task-wake/" + wait.ID, Messages: []ihtml.ChatMessage{{Role: "user", Content: "a real user message"}}})
	events := rec.wait(t, ihtml.ChatStreamAssistantDone, 1)
	if events[len(events)-1].Message != "ordinary user request" {
		t.Fatal(events)
	}
}
