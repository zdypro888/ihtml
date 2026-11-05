package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/agent"
)

// 57 项使用真实签名和确认队列；一项执行前撤权不影响独立回执，续办只在整批之后发生。
func TestBatchConfirmationExactScopeAndSingleContinuation(t *testing.T) {
	var executed, continued atomic.Int32
	cfg := approvalConfig(t, func(_ context.Context, raw json.RawMessage) (string, error) {
		executed.Add(1)
		return string(raw), nil
	})
	cfg.Confirmation.MaxPendingTools = 64
	cfg.ToolExecutionGuard = func(_ context.Context, _ *ihtml.ChatSessionContext, _ agent.Tool, raw json.RawMessage) error {
		if string(raw) == `{"target":10}` {
			return errors.New("permission revoked")
		}
		return nil
	}
	cfg.ContinueAfterTool = func(ctx context.Context, _ *ihtml.ChatSessionContext, r *ConfirmedToolResult) (*ihtml.ChatClientEvent, error) {
		return NewToolContinuation(ctx, r), nil
	}
	cfg.Engine = &fakeEngine{run: func(ctx context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
		if req.UserText != "propose" {
			continued.Add(1)
			if executed.Load() != 56 {
				t.Errorf("continuation ran before batch drained: %d", executed.Load())
			}
			return &agent.TurnResult{Text: "batch reviewed"}, nil
		}
		for i := 0; i < 57; i++ {
			if _, err := req.Tools[0].Handler(agent.WithToolInvocationKey(ctx, fmt.Sprintf("item-%d", i)), json.RawMessage(fmt.Sprintf(`{"target":%d}`, i))); err != nil {
				return nil, err
			}
		}
		return &agent.TurnResult{Text: "review batch"}, nil
	}}
	r := newRecorder()
	s := openSession(t, cfg, "alice", r)
	send(s, "propose")
	events := r.wait(t, ihtml.ChatStreamAssistantDone, 1)
	calls := []ihtml.ProposedToolCall{}
	for _, ev := range events {
		if ev.Type == ihtml.ChatStreamConfirmationRequired {
			calls = append(calls, *ev.ToolCall)
		}
	}
	if len(calls) != 57 || executed.Load() != 0 {
		t.Fatal("proposal scope or premature execution", len(calls), executed.Load())
	}
	// 任一参数被替换，或同一 nonce 重复出现，整批均不得进入执行队列。
	tampered := append([]ihtml.ProposedToolCall(nil), calls...)
	tampered[1].Body = map[string]any{"target": "outside"}
	for _, bad := range [][]ihtml.ProposedToolCall{tampered, {calls[0], calls[0]}} {
		s.HandleEvent(context.Background(), &ihtml.ChatClientEvent{Type: ihtml.ChatClientConfirmTools, ToolCalls: bad})
	}
	if executed.Load() != 0 {
		t.Fatal("invalid batch executed")
	}
	s.HandleEvent(context.Background(), &ihtml.ChatClientEvent{Type: ihtml.ChatClientConfirmTools, ToolCalls: calls})
	r.wait(t, ihtml.ChatStreamAssistantDone, 58)
	if executed.Load() != 56 || continued.Load() != 1 {
		t.Fatal("incorrect execution or repeated continuation", executed.Load(), continued.Load())
	}
	successes, failures := 0, 0
	for _, ev := range r.snapshot() {
		if ev.Type == ihtml.ChatStreamToolResult {
			if ev.ToolStatus == ihtml.ChatToolSuccess {
				successes++
			}
			if ev.ToolStatus == ihtml.ChatToolError {
				failures++
			}
		}
	}
	if successes != 56 || failures != 1 {
		t.Fatal("partial results were lost", successes, failures)
	}
	// 批次重放不重新执行已经消费的签名。
	s.HandleEvent(context.Background(), &ihtml.ChatClientEvent{Type: ihtml.ChatClientConfirmTools, ToolCalls: calls})
	if executed.Load() != 56 {
		t.Fatal("batch replay executed")
	}
}

// 即使宿主复用 InvocationScope，历史恢复也不能跨原始提案轮次组合批准。
func TestBatchConfirmationKeepsOriginalTurnBoundary(t *testing.T) {
	var executed atomic.Int32
	cfg := approvalConfig(t, func(context.Context, json.RawMessage) (string, error) { executed.Add(1); return "saved", nil })
	r := newRecorder()
	s := openSession(t, cfg, "alice", r)
	send(s, "propose")
	r.wait(t, ihtml.ChatStreamAssistantDone, 1)
	send(s, "propose")
	events := r.wait(t, ihtml.ChatStreamAssistantDone, 2)
	calls := []ihtml.ProposedToolCall{}
	for _, ev := range events {
		if ev.Type == ihtml.ChatStreamConfirmationRequired {
			calls = append(calls, *ev.ToolCall)
		}
	}
	if len(calls) != 2 || calls[0].ConfirmationGroup == calls[1].ConfirmationGroup {
		t.Fatal("original groups lost")
	}
	s.HandleEvent(context.Background(), &ihtml.ChatClientEvent{Type: ihtml.ChatClientConfirmTools, ToolCalls: calls})
	if executed.Load() != 0 {
		t.Fatal("mixed original turns executed")
	}
	confirmCard(s, calls[0])
	r.wait(t, ihtml.ChatStreamToolResult, 1)
	if executed.Load() != 1 {
		t.Fatal("rejected batch consumed original approval")
	}
}
