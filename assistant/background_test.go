package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/agent"
)

func backgroundFixture(t *testing.T, engine agent.Engine) (*BackgroundBackend, *Backend) {
	t.Helper()
	plain, err := NewChecked(configFor(engine))
	if err != nil {
		t.Fatal(err)
	}
	hub, err := NewBackgroundBackend(BackgroundConfig{Backend: plain, Lifetime: t.Context(), Key: func(_ context.Context, sc *ihtml.ChatSessionContext) (string, error) {
		return sc.User + ":" + sc.Request.URL.Query().Get("conversation"), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hub.Close)
	return hub, plain
}
func attachBackground(t *testing.T, hub *BackgroundBackend, ctx context.Context, user, conversation string, log *recorder) ihtml.ChatSession {
	t.Helper()
	session, err := hub.NewSession(ctx, &ihtml.ChatSessionContext{User: user, Emit: log.emit, Request: httptest.NewRequest("GET", "http://fixture.test/?conversation="+conversation, nil)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.Close)
	return session
}

// 模型执行、会话和用户生命周期一致；任何一个 WebSocket 的取消都不应中断原轮次。
func TestBackgroundDetachKeepsExecutionAndReplaysWithoutNewModelCall(t *testing.T) {
	started := make(chan struct{})
	finish := make(chan struct{})
	var calls atomic.Int32
	hub, _ := backgroundFixture(t, &fakeEngine{run: func(ctx context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
		calls.Add(1)
		req.OnDelta("partial")
		close(started)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-finish:
		}
		req.OnDelta("partial completed")
		return &agent.TurnResult{Text: "final result", FinishReason: "stop"}, nil
	}})
	parent, cancel := context.WithCancel(t.Context())
	first := newRecorder()
	conn := attachBackground(t, hub, parent, "owner", "one", first)
	send(conn, "request")
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("model not started")
	}
	conn.Close()
	cancel()
	close(finish)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		hub.mu.Lock()
		e := hub.sessions["owner:one"]
		hub.mu.Unlock()
		e.mu.Lock()
		active := len(e.active)
		e.mu.Unlock()
		if active == 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	second := newRecorder()
	attachBackground(t, hub, t.Context(), "owner", "one", second)
	events := second.wait(t, ihtml.ChatStreamAssistantDone, 1)
	if calls.Load() != 1 {
		t.Fatal("reconnect reran model", calls.Load())
	}
	for _, event := range events {
		if event.Type == ihtml.ChatStreamTurnError {
			t.Fatal("disconnect canceled model", event)
		}
	}
	if events[len(events)-1].Message != "final result" {
		t.Fatal(events)
	}
}

func TestBackgroundReplaySnapshotsAndOwnerIsolation(t *testing.T) {
	hub, _ := backgroundFixture(t, &fakeEngine{run: func(_ context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
		req.OnDelta("a")
		req.OnDelta("ab")
		req.OnDelta("abc")
		return &agent.TurnResult{Text: "abc"}, nil
	}})
	log := newRecorder()
	first := attachBackground(t, hub, t.Context(), "owner", "one", log)
	send(first, "request")
	log.wait(t, ihtml.ChatStreamAssistantDone, 1)
	first.Close()
	replay := newRecorder()
	attachBackground(t, hub, t.Context(), "owner", "one", replay)
	events := replay.wait(t, ihtml.ChatStreamAssistantDone, 1)
	snapshots := 0
	var seq uint64
	for _, event := range events {
		if event.BackgroundSeq <= seq {
			t.Fatal("unordered replay", events)
		}
		seq = event.BackgroundSeq
		if event.Type == ihtml.ChatStreamAssistantSnapshot {
			snapshots++
			if event.Delta != "abc" {
				t.Fatal("delta replay was not a full snapshot", event)
			}
		}
	}
	if snapshots != 1 {
		t.Fatal("stream was not coalesced", snapshots)
	}
	foreign := newRecorder()
	attachBackground(t, hub, t.Context(), "other", "one", foreign)
	if len(foreign.snapshot()) != 0 {
		t.Fatal("foreign owner saw events")
	}
}

func TestBackgroundExplicitStopStillCancelsOriginalRun(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	hub, _ := backgroundFixture(t, &fakeEngine{run: func(ctx context.Context, _ *agent.TurnRequest) (*agent.TurnResult, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return nil, ctx.Err()
	}})
	first := attachBackground(t, hub, t.Context(), "owner", "one", newRecorder())
	send(first, "request")
	<-started
	first.Close()
	second := attachBackground(t, hub, t.Context(), "owner", "one", newRecorder())
	second.HandleEvent(t.Context(), &ihtml.ChatClientEvent{Type: ihtml.ChatClientCancel})
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("stop did not reach original execution")
	}
	models, err := hub.Models(t.Context())
	if err != nil || !models.BackgroundExecution || models.ConfirmationScope != "session" {
		t.Fatal("missing honest capability", models, err)
	}
}

func TestBackgroundDeliveryFailureDoesNotAbortWork(t *testing.T) {
	var completed atomic.Bool
	hub, _ := backgroundFixture(t, &fakeEngine{run: func(_ context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
		req.OnDelta("work")
		completed.Store(true)
		return &agent.TurnResult{Text: "done"}, nil
	}})
	first, err := hub.NewSession(t.Context(), &ihtml.ChatSessionContext{User: "owner", Emit: func(ihtml.ChatStreamEvent) error { return errors.New("transport lost") }, Request: httptest.NewRequest("GET", "http://fixture.test/?conversation=one", nil)})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	send(first, "request")
	deadline := time.Now().Add(time.Second)
	for !completed.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !completed.Load() {
		t.Fatal("failed delivery canceled model")
	}
	replay := newRecorder()
	attachBackground(t, hub, t.Context(), "owner", "one", replay)
	replay.wait(t, ihtml.ChatStreamAssistantDone, 1)
}

// 重连复用同一签名域和待确认状态；既不重签，也不把确认当成新提问。
func TestBackgroundPendingApprovalSurvivesDetachAndExecutesOnce(t *testing.T) {
	var calls atomic.Int32
	cfg := approvalConfig(t, func(context.Context, json.RawMessage) (string, error) { calls.Add(1); return "done", nil })
	backend, err := NewChecked(cfg)
	if err != nil {
		t.Fatal(err)
	}
	hub, err := NewBackgroundBackend(BackgroundConfig{Backend: backend, Lifetime: t.Context(), Key: func(_ context.Context, sc *ihtml.ChatSessionContext) (string, error) { return sc.User, nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hub.Close)
	log := newRecorder()
	first := attachBackground(t, hub, t.Context(), "owner", "one", log)
	card := proposedCard(t, first, log)
	first.Close()
	secondLog := newRecorder()
	second := attachBackground(t, hub, t.Context(), "owner", "one", secondLog)
	events := secondLog.wait(t, ihtml.ChatStreamAssistantDone, 1)
	restored := false
	for _, event := range events {
		if event.ToolCall != nil && event.ToolCall.ConfirmationToken == card.ConfirmationToken {
			restored = true
		}
	}
	if !restored {
		t.Fatal("original signed card was not restored")
	}
	confirmCard(second, card)
	secondLog.wait(t, ihtml.ChatStreamToolResult, 1)
	confirmCard(second, card)
	if calls.Load() != 1 {
		t.Fatal("approval did not execute exactly once", calls.Load())
	}
}
