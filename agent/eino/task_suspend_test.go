package einoengine

import (
	"context"
	"encoding/json"
	"github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/schema"
	ai "github.com/zdypro888/ihtml/agent"
	"testing"
	"time"
)

// 模型主动挂起后不继续消耗迭代；原生批准会话也不会被等待 checkpoint 永久阻塞。
func TestTaskSuspendEndsModelTurnAndAllowsFreshWakeTurn(t *testing.T) {
	for _, managed := range []bool{false, true} {
		t.Run(map[bool]string{false: "stateless", true: "native"}[managed], func(t *testing.T) {
			runtime := newTaskTestRuntime(t)
			calls := 0
			state := &scriptedModelState{fn: func(input []*schema.Message, _ []*schema.ToolInfo) (*schema.Message, error) {
				calls++
				for _, m := range input {
					if m.Role == schema.User && m.Content == "wake" {
						return schema.AssistantMessage("resumed from saved facts", nil), nil
					}
				}
				return taskModelCall("suspend", "task_suspend", `{"ids":[],"timeout_ms":60000,"checkpoint":"user goal and next evidence to inspect"}`), nil
			}}
			var store RuntimeStore
			if managed {
				store = session.NewInMemoryStore[*schema.Message](nil)
			}
			engine := newNativeTestEngine(&scriptedModel{state: state}, store)
			req := &ai.TurnRequest{SessionID: "821", InvocationScope: "suspend-turn", UserText: "start", Tasks: runtime, AllowTaskSuspend: true, RequireApproval: managed, Tools: []ai.Tool{{Name: "read", Effect: ai.ToolEffectRead, LoadMode: ai.ToolLoadImmediate}}}
			// 未使用的领域 Handler 也必须是有效工具。
			req.Tools[0].Handler = func(_ context.Context, _ json.RawMessage) (string, error) { return "read", nil }
			result, err := engine.RunTurn(t.Context(), req)
			if err != nil || result == nil || result.Waiting == nil || result.FinishReason != "task_waiting" || calls != 1 {
				t.Fatal(result, err, calls)
			}
			req.UserText = "wake"
			req.InvocationScope = "wake-turn"
			req.EngineSession = result.EngineSession
			next, err := engine.RunTurn(t.Context(), req)
			if err != nil || next == nil || next.Text != "resumed from saved facts" || calls != 2 {
				t.Fatal(next, err, calls)
			}
		})
	}
}

// 挂起模型并结束本轮不会取消已经受理的同步后台调用，后续事件仍可读取其真实结果。
func TestTaskSuspendKeepsBackgroundInvocationAlive(t *testing.T) {
	runtime := newTaskTestRuntime(t)
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	var handle string
	state := &scriptedModelState{fn: func(input []*schema.Message, _ []*schema.ToolInfo) (*schema.Message, error) {
		for _, m := range input {
			if m.Role == schema.Tool && m.ToolName == taskStartTool {
				var info ai.TaskInfo
				if err := json.Unmarshal([]byte(m.Content), &info); err != nil {
					return nil, err
				}
				handle = info.ID
				args, _ := json.Marshal(map[string]any{"ids": []string{handle}, "all": true, "timeout_ms": 60000, "checkpoint": "inspect the completed original call"})
				return taskModelCall("pause", "task_suspend", string(args)), nil
			}
		}
		return taskModelCall("start", taskStartTool, `{"tool":"sync_read","arguments":{}}`), nil
	}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req := &ai.TurnRequest{SessionID: "background-suspend", InvocationScope: "turn", UserText: "do work then resume", Tasks: runtime, AllowTaskSuspend: true, Tools: []ai.Tool{{Name: "sync_read", Effect: ai.ToolEffectRead, LoadMode: ai.ToolLoadImmediate, Handler: func(ctx context.Context, _ json.RawMessage) (string, error) {
		close(started)
		select {
		case <-release:
			return "actual after pause", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}}}}
	result, err := newNativeTestEngine(&scriptedModel{state: state}, nil).RunTurn(ctx, req)
	if err != nil || result == nil || result.Waiting == nil || handle == "" {
		t.Fatal(result, err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("call never started")
	}
	cancel()
	release <- struct{}{}
	rows, err := runtime.Wait(t.Context(), ai.TaskScope(req), []string{handle}, map[string]bool{"sync_read": true}, time.Second, true)
	if err != nil || rows[0].State != "completed" || rows[0].Result != "actual after pause" {
		t.Fatal(rows, err)
	}
}
