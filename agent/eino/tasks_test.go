package einoengine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	ai "github.com/zdypro888/ihtml/agent"
)

func newTaskTestRuntime(t *testing.T) *ai.TaskRuntime {
	t.Helper()
	r, err := ai.NewTaskRuntime(t.Context(), ai.TaskConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	return r
}
func taskModelCall(id, name, args string) *schema.Message {
	return schema.AssistantMessage("", []schema.ToolCall{{ID: id, Type: "function", Function: schema.FunctionCall{Name: name, Arguments: args}}})
}

// 两个同步 Handler 尚未返回时模型已执行另一项查询，再自行选择等待；真实结果按句柄对应。
func TestDeepAgentChoosesBackgroundStartsThenOtherWorkAndWait(t *testing.T) {
	for _, mode := range []ai.ToolLoadMode{ai.ToolLoadImmediate, ai.ToolLoadDeferred} {
		t.Run(string(mode), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			runtime := newTaskTestRuntime(t)
			started := make(chan struct{}, 2)
			release := make(chan struct{})
			var calls atomic.Int32
			ids := []string{}
			state := &scriptedModelState{fn: func(input []*schema.Message, visible []*schema.ToolInfo) (*schema.Message, error) {
				found := false
				for _, tool := range visible {
					if tool.Name == "sync_query" {
						found = true
					}
				}
				if !found {
					return taskModelCall("search", toolSearchToolName, `{"query":"select:sync_query"}`), nil
				}
				readDone, waitDone := false, false
				ids = nil
				for _, m := range input {
					if m.Role != schema.Tool {
						continue
					}
					if m.ToolName == taskStartTool {
						var info ai.TaskInfo
						if json.Unmarshal([]byte(m.Content), &info) == nil && info.ID != "" {
							ids = append(ids, info.ID)
						}
					}
					if m.ToolName == "independent_query" {
						readDone = true
					}
					if m.ToolName == "task_wait" {
						waitDone = true
						if !strings.Contains(m.Content, `result-a`) || !strings.Contains(m.Content, `result-b`) {
							return nil, fmt.Errorf("missing original results: %s", m.Content)
						}
					}
				}
				if waitDone {
					return schema.AssistantMessage("actual results checked", nil), nil
				}
				if len(ids) == 0 {
					return schema.AssistantMessage("", []schema.ToolCall{
						{ID: "start-a", Type: "function", Function: schema.FunctionCall{Name: taskStartTool, Arguments: `{"tool":"sync_query","arguments":{"item":"a"}}`}},
						{ID: "start-b", Type: "function", Function: schema.FunctionCall{Name: taskStartTool, Arguments: `{"tool":"sync_query","arguments":{"item":"b"}}`}},
					}), nil
				}
				if !readDone {
					return taskModelCall("other", "independent_query", `{}`), nil
				}
				args, _ := json.Marshal(map[string]any{"ids": ids, "all": true, "timeout_ms": 2000})
				return taskModelCall("wait", "task_wait", string(args)), nil
			}}
			req := &ai.TurnRequest{SessionID: "background-test", InvocationScope: "turn-1", Tasks: runtime, UserText: "查询几个结果", Tools: []ai.Tool{
				{Name: "sync_query", Effect: ai.ToolEffectRead, LoadMode: mode, InputSchema: taskSchema(map[string]any{"item": map[string]any{"type": "string"}}, "item"), Handler: func(c context.Context, args json.RawMessage) (string, error) {
					calls.Add(1)
					started <- struct{}{}
					select {
					case <-release:
						var in struct {
							Item string `json:"item"`
						}
						json.Unmarshal(args, &in)
						return "result-" + in.Item, nil
					case <-c.Done():
						return "", c.Err()
					}
				}},
				{Name: "independent_query", Effect: ai.ToolEffectRead, LoadMode: ai.ToolLoadImmediate, Handler: func(c context.Context, _ json.RawMessage) (string, error) {
					for range 2 {
						select {
						case <-started:
						case <-c.Done():
							return "", c.Err()
						}
					}
					close(release)
					return "other-result", nil
				}},
			}}
			result, err := newNativeTestEngine(&scriptedModel{state: state}, nil).RunTurn(ctx, req)
			if err != nil || result == nil || result.Text != "actual results checked" || calls.Load() != 2 {
				t.Fatal(result, err, calls.Load())
			}
		})
	}
}

// 直接调用与后台包装共享原调用键，两种调用方式之间不能重放同一副作用。
func TestBackgroundAndDirectCallsShareOriginalEffectBoundary(t *testing.T) {
	for _, backgroundFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(backgroundFirst), func(t *testing.T) {
			runtime := newTaskTestRuntime(t)
			var calls atomic.Int32
			original := ai.Tool{Name: "write_item", Effect: ai.ToolEffectWrite, LoadMode: ai.ToolLoadImmediate, Handler: func(ctx context.Context, _ json.RawMessage) (string, error) {
				if ai.ToolInvocationKey(ctx) == "" {
					t.Error("invocation key missing")
				}
				calls.Add(1)
				return "stored", nil
			}}
			m, err := newTurnToolMiddleware(t.Context(), "scope", []ai.Tool{original}, nil)
			if err != nil {
				t.Fatal(err)
			}
			m.tasks = runtime
			m.taskScope = "owner"
			start := &compose.ToolInput{Name: taskStartTool, CallID: "start", Arguments: `{"tool":"write_item","arguments":{"value":"x"}}`}
			direct := &compose.ToolInput{Name: original.Name, CallID: "direct", Arguments: `{"value":"x"}`}
			invoke := m.guardInvokable(func(ctx context.Context, in *compose.ToolInput) (*compose.ToolOutput, error) {
				result, err := original.Handler(ctx, json.RawMessage(in.Arguments))
				return &compose.ToolOutput{Result: result}, err
			})
			first, second := start, direct
			if !backgroundFirst {
				first, second = direct, start
			}
			if _, err = invoke(t.Context(), first); err != nil {
				t.Fatal(err)
			}
			if _, err = invoke(t.Context(), second); err != nil {
				t.Fatal(err)
			}
			runtime.EndTurn("owner", "scope", true)
			if backgroundFirst {
				rows, _ := runtime.Read("owner", nil, map[string]bool{original.Name: true})
				if len(rows) != 1 {
					t.Fatal(rows)
				}
				if _, err = runtime.Wait(t.Context(), "owner", []string{rows[0].ID}, map[string]bool{original.Name: true}, time.Second, true); err != nil {
					t.Fatal(err)
				}
			}
			if calls.Load() != 1 {
				t.Fatal("effect replayed", calls.Load())
			}
		})
	}
}

// 原生批准必须在后台任务被接纳之前挂起；重建引擎后批准/拒绝仍绑定原工具与确切参数。
func TestBackgroundDispatchUsesNativeApprovalCheckpoint(t *testing.T) {
	for _, mode := range []ai.ToolLoadMode{ai.ToolLoadImmediate, ai.ToolLoadDeferred} {
		for _, approve := range []bool{true, false} {
			t.Run(fmt.Sprint(approve)+string(mode), func(t *testing.T) {
				runtime := newTaskTestRuntime(t)
				store := session.NewInMemoryStore[*schema.Message](nil)
				var calls atomic.Int32
				state := &scriptedModelState{fn: func(input []*schema.Message, visible []*schema.ToolInfo) (*schema.Message, error) {
					for _, m := range input {
						if m.Role == schema.Tool && m.ToolName == taskStartTool {
							return schema.AssistantMessage("dispatch returned: "+m.Content, nil), nil
						}
					}
					found := false
					for _, tool := range visible {
						if tool.Name == "write_item" {
							found = true
						}
					}
					if !found {
						return taskModelCall("discover", toolSearchToolName, `{"query":"select:write_item"}`), nil
					}
					return taskModelCall("async-approval", taskStartTool, `{"tool":"write_item","arguments":{"value":"exact"}}`), nil
				}}
				req := &ai.TurnRequest{SessionID: "72", InvocationScope: "approval-task", UserText: "write", Tasks: runtime, RequireApproval: true, Tools: []ai.Tool{{Name: "write_item", Effect: ai.ToolEffectWrite, LoadMode: mode, ApprovalRequired: true, Handler: func(context.Context, json.RawMessage) (string, error) { calls.Add(1); return "actual receipt", nil }}}}
				first, err := newNativeTestEngine(&scriptedModel{state: state}, store).RunTurn(t.Context(), req)
				if err != nil || first == nil || first.PendingApproval == nil || calls.Load() != 0 {
					t.Fatal(first, err, calls.Load())
				}
				pending := first.PendingApproval.Tools[0]
				if pending.ToolName != "write_item" || string(pending.Args) != `{"value":"exact"}` {
					t.Fatal(pending)
				}
				req.EngineSession = first.EngineSession
				req.Resume = &ai.ResumeRequest{CheckPointID: first.PendingApproval.CheckPointID, Decisions: map[string]bool{pending.InterruptID: approve}}
				resumed, err := newNativeTestEngine(&scriptedModel{state: state}, store).RunTurn(t.Context(), req)
				if err != nil || resumed == nil || resumed.PendingApproval != nil {
					t.Fatal(resumed, err)
				}
				rows, err := runtime.Read(ai.TaskScope(req), nil, map[string]bool{"write_item": true})
				if err != nil {
					t.Fatal(err)
				}
				if approve {
					if len(rows) != 1 {
						t.Fatal(rows)
					}
					rows, err = runtime.Wait(t.Context(), ai.TaskScope(req), []string{rows[0].ID}, map[string]bool{"write_item": true}, time.Second, true)
					if err != nil || rows[0].Result != "actual receipt" || calls.Load() != 1 {
						t.Fatal(rows, err, calls.Load())
					}
				} else if len(rows) != 0 || calls.Load() != 0 {
					t.Fatal("rejected dispatch executed", rows, calls.Load())
				}
			})
		}
	}
}

func TestBackgroundDispatchCannotExecuteUnloadedOrRecursiveTool(t *testing.T) {
	runtime := newTaskTestRuntime(t)
	var calls atomic.Int32
	m, err := newTurnToolMiddleware(t.Context(), "turn", []ai.Tool{{Name: "hidden", Effect: ai.ToolEffectRead, Handler: func(context.Context, json.RawMessage) (string, error) { calls.Add(1); return "bad", nil }}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.tasks = runtime
	m.taskScope = "owner"
	output, err := m.startTask(t.Context(), &compose.ToolInput{Name: taskStartTool, CallID: "x", Arguments: `{"tool":"hidden","arguments":{}}`})
	if err != nil || !strings.Contains(output.Result, "tool_search") || calls.Load() != 0 {
		t.Fatal(output, err, calls.Load())
	}
	if _, err = m.startTask(t.Context(), &compose.ToolInput{Name: taskStartTool, CallID: "x", Arguments: `{"tool":"task_start","arguments":{}}`}); err == nil {
		t.Fatal("recursive dispatcher allowed")
	}
}
