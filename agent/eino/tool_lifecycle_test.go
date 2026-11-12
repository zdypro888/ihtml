package einoengine

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	ai "github.com/zdypro888/ihtml/agent"
)

// 端点真正阻塞时开始事件必须已经可见，不能等原调用返回后补造进度。
func TestToolLifecycleStartPrecedesSlowHandlerReturn(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	starts := make(chan ai.Step, 2)
	var mu sync.Mutex
	var steps []ai.Step
	state := &scriptedModelState{fn: func(input []*schema.Message, _ []*schema.ToolInfo) (*schema.Message, error) {
		for _, m := range input {
			if m.Role == schema.Tool && m.ToolCallID == "slow-call" {
				return schema.AssistantMessage("done", nil), nil
			}
		}
		return schema.AssistantMessage("", []schema.ToolCall{{ID: "slow-call", Type: "function", Function: schema.FunctionCall{Name: "slow_read", Arguments: `{"private":"value"}`}}}), nil
	}}
	done := make(chan error, 1)
	go func() {
		_, err := newNativeTestEngine(&scriptedModel{state: state}, nil).RunTurn(ctx, &ai.TurnRequest{UserText: "read", Tools: []ai.Tool{{Name: "slow_read", Title: "读取原任务资料", LoadMode: ai.ToolLoadImmediate, Effect: ai.ToolEffectRead, Handler: func(ctx context.Context, _ json.RawMessage) (string, error) {
			close(entered)
			select {
			case <-release:
				return `{}`, nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}}}, OnEvent: func(step ai.Step) {
			mu.Lock()
			if step.Kind != ai.StepText {
				steps = append(steps, step)
			}
			mu.Unlock()
			if step.Kind == ai.StepToolStart {
				starts <- step
			}
		}})
		done <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case step := <-starts:
		if step.ToolCallID != "slow-call" || step.ToolTitle != "读取原任务资料" || len(step.Args) != 0 {
			t.Fatal(step)
		}
	default:
		t.Fatal("handler entered before public start")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(steps) != 2 || steps[0].Kind != ai.StepToolStart || steps[1].Kind != ai.StepToolCall || steps[1].ToolCallID != steps[0].ToolCallID || steps[1].ToolTitle != steps[0].ToolTitle {
		t.Fatalf("mismatched lifecycle: %+v", steps)
	}
}

// 未发现、去重抑制和未批准调用都不能被报告为正在执行业务。
func TestToolLifecycleDoesNotStartBlockedOrReplayedCalls(t *testing.T) {
	var mu sync.Mutex
	var starts []ai.Step
	tool := ai.Tool{Name: "write_item", Title: "保存资料", Effect: ai.ToolEffectWrite, LoadMode: ai.ToolLoadImmediate, Handler: func(context.Context, json.RawMessage) (string, error) { return "ok", nil }}
	m, err := newTurnToolMiddleware(t.Context(), "turn", []ai.Tool{tool}, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.onEvent = func(s ai.Step) { mu.Lock(); starts = append(starts, s); mu.Unlock() }
	endpoint := m.guardInvokable(func(context.Context, *compose.ToolInput) (*compose.ToolOutput, error) {
		return &compose.ToolOutput{Result: "ok"}, nil
	})
	for _, id := range []string{"first", "duplicate"} {
		if _, err := endpoint(t.Context(), &compose.ToolInput{Name: tool.Name, CallID: id, Arguments: `{}`}); err != nil {
			t.Fatal(err)
		}
	}
	if len(starts) != 1 || starts[0].ToolCallID != "first" {
		t.Fatal(starts)
	}
	blocked := tool
	blocked.Name = "hidden"
	blocked.LoadMode = ai.ToolLoadDeferred
	m, err = newTurnToolMiddleware(t.Context(), "turn", []ai.Tool{blocked}, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.onEvent = func(s ai.Step) { t.Errorf("unselected tool started: %+v", s) }
	if _, err = m.guardInvokable(func(context.Context, *compose.ToolInput) (*compose.ToolOutput, error) {
		t.Fatal("unselected handler ran")
		return nil, nil
	})(t.Context(), &compose.ToolInput{Name: "hidden", CallID: "blocked", Arguments: `{}`}); err != nil {
		t.Fatal(err)
	}
	tool.ApprovalRequired = true
	m, err = newTurnToolMiddleware(t.Context(), "turn", []ai.Tool{tool}, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.taskApproval = true
	m.onEvent = func(s ai.Step) { t.Errorf("unapproved tool started: %+v", s) }
	gated := approvalMiddleware([]ai.Tool{tool}, true, m).Invokable(m.guardInvokable(func(context.Context, *compose.ToolInput) (*compose.ToolOutput, error) {
		t.Fatal("unapproved handler ran")
		return nil, nil
	}))
	_, err = gated(t.Context(), &compose.ToolInput{Name: tool.Name, CallID: "approval", Arguments: `{}`})
	if err == nil {
		t.Fatalf("missing approval pause: %v", err)
	}
}
