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

// 后台执行接收治理后的 Handler，不能因嵌套调用跳过签名确认；完成通知不包含原参数和结果。
func TestBackgroundTaskUsesOriginalManualConfirmation(t *testing.T) {
	r := newRecorder()
	var writes atomic.Int32
	cfg := approvalConfig(t, func(context.Context, json.RawMessage) (string, error) { writes.Add(1); return "private-result", nil })
	cfg.Tasks = &agent.TaskConfig{}
	cfg.Engine = &fakeEngine{run: func(ctx context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
		row, err := req.Tasks.Start(agent.WithToolInvocationKey(ctx, "trusted-original-key"), agent.TaskScope(req), req.InvocationScope, "nested", req.Tools[0], json.RawMessage(`{"secret":"private-argument"}`))
		if err != nil {
			return nil, err
		}
		rows, err := req.Tasks.Wait(ctx, agent.TaskScope(req), []string{row.ID}, map[string]bool{req.Tools[0].Name: true}, time.Second, true)
		if err == nil && (len(rows) != 1 || !strings.Contains(rows[0].Result, "pending")) {
			t.Errorf("confirmation proposal missing: %+v", rows)
		}
		return &agent.TurnResult{Text: "proposal only"}, err
	}}
	s := openSession(t, cfg, "alice", r)
	card := proposedCard(t, s, r)
	if writes.Load() != 0 {
		t.Fatal("background bypassed confirmation")
	}
	events := r.wait(t, ihtml.ChatStreamTaskComplete, 1)
	data, _ := json.Marshal(events)
	if strings.Contains(string(data), "private-result") || strings.Contains(string(data), "private-argument") {
		t.Fatal("background notification leaked private data")
	}
	confirmCard(s, card)
	r.wait(t, ihtml.ChatStreamAssistantDone, 2)
	if writes.Load() != 1 {
		t.Fatal("original confirmation did not execute once", writes.Load())
	}
}

func TestBackgroundTaskRechecksOriginalLiveGuard(t *testing.T) {
	r := newRecorder()
	var calls atomic.Int32
	cfg := configFor(&fakeEngine{run: func(ctx context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
		row, err := req.Tasks.Start(ctx, agent.TaskScope(req), "turn", "key", req.Tools[0], json.RawMessage(`{}`))
		if err != nil {
			return nil, err
		}
		rows, err := req.Tasks.Wait(ctx, agent.TaskScope(req), []string{row.ID}, map[string]bool{req.Tools[0].Name: true}, time.Second, true)
		if err == nil && rows[0].State != "failed" {
			t.Errorf("revoked tool executed: %+v", rows)
		}
		return &agent.TurnResult{Text: "guard rejected"}, err
	}})
	cfg.Tasks = &agent.TaskConfig{}
	prepare := cfg.PrepareTurn
	cfg.PrepareTurn = func(ctx context.Context, sc *ihtml.ChatSessionContext, ev *ihtml.ChatClientEvent) (*agent.TurnRequest, error) {
		req, err := prepare(ctx, sc, ev)
		req.Tools = []agent.Tool{{Name: "read", Effect: agent.ToolEffectRead, Handler: func(context.Context, json.RawMessage) (string, error) { calls.Add(1); return "private", nil }}}
		return req, err
	}
	cfg.ToolExecutionGuard = func(context.Context, *ihtml.ChatSessionContext, agent.Tool, json.RawMessage) error {
		return ihtml.ErrAccessDenied
	}
	s := openSession(t, cfg, "alice", r)
	send(s, "read")
	r.wait(t, ihtml.ChatStreamAssistantDone, 1)
	if calls.Load() != 0 {
		t.Fatal("live guard bypassed")
	}
}

// 正常轮次结束后排队调用仍能通过同一个原守卫执行，不能被旧投影的生命周期误取消。
func TestBackgroundQueuedCallSurvivesNormalTurnAndCanBeReadLater(t *testing.T) {
	r := newRecorder()
	firstEntered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	var calls atomic.Int32
	var ids []string
	cfg := configFor(&fakeEngine{run: func(ctx context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
		if req.UserText == "later" {
			rows, err := req.Tasks.Wait(ctx, agent.TaskScope(req), ids, map[string]bool{"sync": true}, time.Second, true)
			if err == nil && (len(rows) != 2 || rows[0].State != "completed" || rows[1].State != "completed") {
				t.Errorf("lost tasks: %+v", rows)
			}
			return &agent.TurnResult{Text: "results recovered"}, err
		}
		for _, key := range []string{"first", "second"} {
			row, err := req.Tasks.Start(ctx, agent.TaskScope(req), req.InvocationScope, key, req.Tools[0], json.RawMessage(`{}`))
			if err != nil {
				return nil, err
			}
			ids = append(ids, row.ID)
		}
		<-firstEntered
		return &agent.TurnResult{Text: "tasks continue"}, nil
	}})
	cfg.Tasks = &agent.TaskConfig{MaxConcurrent: 1}
	prepare := cfg.PrepareTurn
	cfg.PrepareTurn = func(ctx context.Context, sc *ihtml.ChatSessionContext, ev *ihtml.ChatClientEvent) (*agent.TurnRequest, error) {
		req, err := prepare(ctx, sc, ev)
		req.Tools = []agent.Tool{{Name: "sync", Effect: agent.ToolEffectRead, Handler: func(ctx context.Context, _ json.RawMessage) (string, error) {
			if calls.Add(1) == 1 {
				close(firstEntered)
				select {
				case <-release:
				case <-ctx.Done():
					return "", ctx.Err()
				}
			}
			return "actual", nil
		}}}
		return req, err
	}
	s := openSession(t, cfg, "alice", r)
	send(s, "start")
	r.wait(t, ihtml.ChatStreamAssistantDone, 1)
	// 使用 CancelAll 以外的正常释放，读取下一轮时应取得两项真实结果。
	// 不能在这里关闭后再 defer 关闭；用独立发送作为屏障信号。
	select {
	case release <- struct{}{}:
	case <-time.After(time.Second):
		t.Fatal("first task no longer running")
	}
	send(s, "later")
	r.wait(t, ihtml.ChatStreamAssistantDone, 2)
	if calls.Load() != 2 {
		t.Fatal("queued call dropped", calls.Load())
	}
}

// 模型选择后台的同步写入，在批准后也保持后台并发；全部逐项回执保存前不提前续办。
func TestBackgroundConfirmedSynchronousWritesStayConcurrentAndContinueOnce(t *testing.T) {
	r := newRecorder()
	entered := make(chan struct{}, 2)
	ready := make(chan struct{})
	var started, persisted, continued atomic.Int32
	cfg := approvalConfig(t, func(ctx context.Context, _ json.RawMessage) (string, error) {
		if agent.ToolInvocationKey(ctx) == "" {
			t.Error("original invocation identity missing")
		}
		if started.Add(1) == 2 {
			close(ready)
		}
		entered <- struct{}{}
		select {
		case <-ready:
			return "actual write receipt", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	cfg.Tasks = &agent.TaskConfig{MaxConcurrent: 2}
	cfg.CompleteTool = func(_ context.Context, _ *ihtml.ChatSessionContext, evidence *ConfirmedToolResult) error {
		if evidence.Started && evidence.Err == nil {
			persisted.Add(1)
		}
		return nil
	}
	cfg.ContinueAfterTool = func(ctx context.Context, _ *ihtml.ChatSessionContext, evidence *ConfirmedToolResult) (*ihtml.ChatClientEvent, error) {
		return NewToolContinuation(ctx, evidence), nil
	}
	cfg.Engine = &fakeEngine{run: func(ctx context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
		if req.UserText != "propose" {
			continued.Add(1)
			if persisted.Load() != 2 {
				t.Error("continued before all receipts", persisted.Load())
			}
			rows, err := req.Tasks.Read(agent.TaskScope(req), nil, map[string]bool{"destroy": true})
			if err != nil {
				return nil, err
			}
			children := 0
			for _, row := range rows {
				if row.ParentID != "" {
					children++
				}
			}
			if children != 2 {
				t.Error("confirmed execution handles not linked", rows)
			}
			return &agent.TurnResult{Text: "all actual receipts checked"}, nil
		}
		ids := []string{}
		for _, key := range []string{"one", "two"} {
			row, err := req.Tasks.Start(agent.WithToolInvocationKey(ctx, "trusted-"+key), agent.TaskScope(req), req.InvocationScope, key, req.Tools[0], json.RawMessage(`{"secret":"`+key+`"}`))
			if err != nil {
				return nil, err
			}
			ids = append(ids, row.ID)
		}
		if _, err := req.Tasks.Wait(ctx, agent.TaskScope(req), ids, map[string]bool{"destroy": true}, time.Second, true); err != nil {
			return nil, err
		}
		return &agent.TurnResult{Text: "review fixed list"}, nil
	}}
	s := openSession(t, cfg, "alice", r)
	send(s, "propose")
	events := r.wait(t, ihtml.ChatStreamAssistantDone, 1)
	cards := []ihtml.ProposedToolCall{}
	for _, event := range events {
		if event.Type == ihtml.ChatStreamConfirmationRequired {
			cards = append(cards, *event.ToolCall)
		}
	}
	if len(cards) != 2 || started.Load() != 0 {
		t.Fatal(cards, started.Load())
	}
	s.HandleEvent(t.Context(), &ihtml.ChatClientEvent{Type: ihtml.ChatClientConfirmTools, ToolCalls: cards})
	for range 2 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("confirmed calls were serialized")
		}
	}
	r.wait(t, ihtml.ChatStreamAssistantDone, 4)
	if persisted.Load() != 2 || continued.Load() != 1 {
		t.Fatal("receipts or continuation mismatch", persisted.Load(), continued.Load())
	}
}

// 已批准但尚未开始的后台调用被停止后，原 nonce 未消费；同一确切卡片应允许用户再次批准。
func TestCancelledQueuedConfirmationCanBeExplicitlyApprovedAgain(t *testing.T) {
	r := newRecorder()
	var writes atomic.Int32
	cfg := approvalConfig(t, func(context.Context, json.RawMessage) (string, error) { writes.Add(1); return "stored", nil })
	cfg.Tasks = &agent.TaskConfig{MaxConcurrent: 1}
	var scope string
	cfg.Engine = &fakeEngine{run: func(ctx context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
		scope = agent.TaskScope(req)
		row, err := req.Tasks.Start(ctx, scope, req.InvocationScope, "proposal", req.Tools[0], json.RawMessage(`{"target":"fixed"}`))
		if err != nil {
			return nil, err
		}
		_, err = req.Tasks.Wait(ctx, scope, []string{row.ID}, map[string]bool{"destroy": true}, time.Second, true)
		return &agent.TurnResult{Text: "pending"}, err
	}}
	s := openSession(t, cfg, "alice", r).(*session)
	card := proposedCard(t, s, r)
	release := make(chan struct{})
	defer close(release)
	entered := make(chan struct{})
	_, err := s.tasks.Start(t.Context(), scope, "blocker", "one", agent.Tool{Name: "local_blocker", Effect: agent.ToolEffectRead, Handler: func(context.Context, json.RawMessage) (string, error) {
		close(entered)
		<-release
		return "released", nil
	}}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	confirmCard(s, card)
	deadline := time.Now().Add(time.Second)
	child := ""
	for time.Now().Before(deadline) {
		rows, _ := s.tasks.Read(scope, nil, map[string]bool{"destroy": true})
		for _, row := range rows {
			if row.ParentID != "" {
				child = row.ID
			}
		}
		if child != "" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if child == "" {
		t.Fatal("confirmed call not admitted")
	}
	s.HandleEvent(t.Context(), &ihtml.ChatClientEvent{Type: ihtml.ChatClientCancel})
	rows, err := s.tasks.Wait(t.Context(), scope, []string{child}, map[string]bool{"destroy": true}, time.Second, true)
	if err != nil || rows[0].State != "cancelled" || writes.Load() != 0 {
		t.Fatal(rows, err, writes.Load())
	}
	deadline = time.Now().Add(time.Second)
	available := false
	for time.Now().Before(deadline) {
		s.pendingMu.Lock()
		item := s.pendingTools[card.ConfirmationNonce]
		available = item != nil && !item.queued
		s.pendingMu.Unlock()
		if available {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !available {
		t.Fatal("cancelled queued call left the issued confirmation permanently reserved")
	}
	release <- struct{}{}
	confirmCard(s, card)
	r.wait(t, ihtml.ChatStreamAssistantDone, 2)
	if writes.Load() != 1 {
		t.Fatal("explicit reapproval did not execute exactly once", writes.Load())
	}
	confirmCard(s, card)
	r.wait(t, ihtml.ChatStreamTurnError, 1)
	if writes.Load() != 1 {
		t.Fatal("consumed confirmation was replayed", writes.Load())
	}

}
