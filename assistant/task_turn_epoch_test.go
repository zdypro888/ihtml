package assistant

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/agent"
)

// 远程停止覆盖纯推理和旧队列；工具关闭不解除停止约束，停止后新接纳的明确消息仍可继续。
func TestRemoteStopCancelsPlanningAndFencesOldQueuedMessages(t *testing.T) {
	for _, confirmationQueue := range []bool{false, true} {
		name := "runner"
		if confirmationQueue {
			name = "confirmation-queue"
		}
		t.Run(name, func(t *testing.T) {
			store := agent.NewMemoryTaskStore()
			entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			engine := &fakeEngine{run: func(ctx context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
				calls.Add(1)
				if req.Tasks != nil || len(req.Tools) != 0 {
					t.Error("model watcher exposed disabled tool capabilities")
				}
				switch req.UserText {
				case "active":
					close(entered)
					<-ctx.Done()
					close(cancelled)
					<-release
					return nil, ctx.Err()
				case "old queued":
					t.Error("old queued message acquired the new stop generation")
				}
				return &agent.TurnResult{Text: req.UserText}, nil
			}}
			cfg := configFor(engine)
			if confirmationQueue {
				approval := approvalConfig(t, func(context.Context, json.RawMessage) (string, error) { return "unused", nil })
				cfg.Confirmation, cfg.ToolExecutionGuard = approval.Confirmation, approval.ToolExecutionGuard
			}
			cfg.TasksProvider = func(context.Context, *ihtml.ChatSessionContext) (agent.TaskConfig, error) {
				return agent.TaskConfig{Store: store, Scope: "shared-scope", PollInterval: time.Millisecond}, nil
			}
			rec := newRecorder()
			first := openSession(t, cfg, "alice", rec)
			t.Cleanup(func() {
				select {
				case <-release:
				default:
					close(release)
				}
			})
			remote := openSession(t, cfg, "alice", newRecorder())
			toolsEnabled := false
			first.HandleEvent(t.Context(), &ihtml.ChatClientEvent{Type: ihtml.ChatClientUserMessage, ToolsEnabled: &toolsEnabled, Messages: []ihtml.ChatMessage{{Role: "user", Content: "active"}}})
			waitSignal(t, entered)
			send(first, "old queued")
			remote.HandleEvent(t.Context(), &ihtml.ChatClientEvent{Type: ihtml.ChatClientCancel})
			waitSignal(t, cancelled)
			first.HandleEvent(t.Context(), &ihtml.ChatClientEvent{Type: ihtml.ChatClientUserMessage, ToolsEnabled: &toolsEnabled, Messages: []ihtml.ChatMessage{{Role: "user", Content: "fresh explicit message"}}})
			close(release)
			events := rec.wait(t, ihtml.ChatStreamAssistantDone, 1)
			if calls.Load() != 2 || events[len(events)-1].Message != "fresh explicit message" {
				t.Fatal("stopped queue or new explicit admission was handled incorrectly", calls.Load(), events)
			}
		})
	}
}

// 确认点击的接纳代次属于当前明确事件，不能在队列出队或恢复原提案上下文时重新取得新代次。
func TestRemoteStopFencesPreviouslyQueuedConfirmation(t *testing.T) {
	store := agent.NewMemoryTaskStore()
	var writes atomic.Int32
	cfg := approvalConfig(t, func(context.Context, json.RawMessage) (string, error) { writes.Add(1); return "written", nil })
	proposalEngine := cfg.Engine
	entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	cfg.Engine = &fakeEngine{run: func(ctx context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
		if req.UserText == "active" {
			close(entered)
			<-ctx.Done()
			close(cancelled)
			<-release
			return nil, ctx.Err()
		}
		return proposalEngine.RunTurn(ctx, req)
	}}
	scope := agent.TaskScope(&agent.TurnRequest{SessionID: "alice"})
	cfg.TasksProvider = func(context.Context, *ihtml.ChatSessionContext) (agent.TaskConfig, error) {
		return agent.TaskConfig{Store: store, Scope: scope, PollInterval: time.Millisecond}, nil
	}
	rec := newRecorder()
	first := openSession(t, cfg, "alice", rec)
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	remote := openSession(t, cfg, "alice", newRecorder())
	card := proposedCard(t, first, rec)
	send(first, "active")
	waitSignal(t, entered)
	confirmCard(first, card)
	remote.HandleEvent(t.Context(), &ihtml.ChatClientEvent{Type: ihtml.ChatClientCancel})
	waitSignal(t, cancelled)
	close(release)
	// 排队的确认必须退休；随后新的显式消息形成屏障，确保断言发生于旧队列已处理之后。
	send(first, "after stop")
	rec.wait(t, ihtml.ChatStreamAssistantDone, 2)
	if writes.Load() != 0 {
		t.Fatal("old confirmation crossed the stop generation", writes.Load())
	}
}
