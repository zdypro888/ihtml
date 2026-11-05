package einoengine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/cloudwego/eino/compose"
	ai "github.com/zdypro888/ihtml/agent"
)

const taskStartTool = "task_start"

func taskKernelName(name string) bool {
	return name == taskStartTool || name == "task_read" || name == "task_wait" || name == "task_cancel" || name == "task_suspend"
}

type taskStartInput struct {
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
}

func decodeTaskInput(raw string, v any) error {
	if len(raw) > 70<<10 {
		return errors.New("task input too large")
	}
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return errors.New("invalid task input")
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("invalid task input")
	}
	return nil
}
func taskJSON(v any) (string, error) { raw, err := json.Marshal(v); return string(raw), err }
func taskSchema(props map[string]any, required ...string) map[string]any {
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}

// taskTools 提供与业务无关的执行原语；具体目标仍须来自本轮授权和已发现能力。
func taskTools(req *ai.TurnRequest) ([]ai.Tool, error) {
	allowed := map[string]bool{}
	for _, t := range req.Tools {
		if taskKernelName(t.Name) {
			return nil, errors.New("reserved task tool name")
		}
		allowed[t.Name] = true
	}
	allowed["task_suspend"] = true
	scope := ai.TaskScope(req)
	handles := map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 8}
	tools := []ai.Tool{
		{Name: taskStartTool, Effect: ai.ToolEffectUnknown, Completion: ai.ToolCompletionAsynchronous, Description: "将一个已发现的原工具调用放入受管后台执行，即使原接口同步也立即返回调用句柄。由你根据目标和依赖决定是否使用。仍走原工具的确认、权限和防重放；启动不代表调用或业务完成。arguments 必须符合原工具协议。", InputSchema: taskSchema(map[string]any{"tool": map[string]any{"type": "string"}, "arguments": map[string]any{"type": "object"}}, "tool", "arguments"), Handler: func(context.Context, json.RawMessage) (string, error) {
			return "", errors.New("task dispatch middleware required")
		}},
		{Name: "task_read", Effect: ai.ToolEffectRead, Description: "读取本会话后台调用的状态和已返回结果；ids 留空分页列调用及活跃等待。next/after 为调用游标，wait_next/wait_after 为等待游标；显式句柄可查询原冻结参数、结果和等待状态。completed 表示原 Handler 返回，结果若只是待确认或业务受理，实际业务仍未完成。账本持久性由宿主存储提供；正文过期会明确标记，不替代原业务回执。", InputSchema: taskSchema(map[string]any{"ids": handles, "after": map[string]any{"type": "string"}, "wait_after": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 128}}), Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in struct {
				IDs       []string `json:"ids"`
				After     string   `json:"after"`
				WaitAfter string   `json:"wait_after"`
				Limit     int      `json:"limit"`
			}
			if err := decodeTaskInput(string(raw), &in); err != nil {
				return "", err
			}
			if len(in.IDs) == 0 {
				if in.Limit == 0 {
					in.Limit = 64
				}
				page, err := req.Tasks.List(ctx, scope, in.After, in.Limit, allowed)
				if err != nil {
					return "", err
				}
				waits, next, err := req.Tasks.ListWaitPage(ctx, scope, in.WaitAfter, in.Limit)
				if err != nil {
					return "", err
				}
				page.Waits = waits
				page.WaitNext = next
				return taskJSON(page)
			}
			rows, err := req.Tasks.Read(scope, in.IDs, allowed)
			if err != nil {
				return "", err
			}
			return taskJSON(map[string]any{"tasks": rows})
		}},
		{Name: "task_wait", Effect: ai.ToolEffectRead, Description: "通过完成通道等待指定后台调用，all=false 等任意一个、true 等全部。timeout_ms 为0至30000，只结束本次等待，不取消任务；超时返回当前真实状态。由你决定等待时机及是否继续等待。", InputSchema: taskSchema(map[string]any{"ids": handles, "all": map[string]any{"type": "boolean"}, "timeout_ms": map[string]any{"type": "integer", "minimum": 0, "maximum": 30000}}, "ids", "timeout_ms"), Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in struct {
				IDs     []string `json:"ids"`
				All     bool     `json:"all"`
				Timeout int      `json:"timeout_ms"`
			}
			if err := decodeTaskInput(string(raw), &in); err != nil {
				return "", err
			}
			if in.Timeout < 0 || in.Timeout > 30000 {
				return "", errors.New("invalid task wait timeout")
			}
			rows, err := req.Tasks.Wait(ctx, scope, in.IDs, allowed, time.Duration(in.Timeout)*time.Millisecond, in.All)
			if err != nil {
				return "", err
			}
			return taskJSON(map[string]any{"tasks": rows})
		}},
		{Name: "task_cancel", Effect: ai.ToolEffectExecute, Description: "对指定本会话调用请求协作取消，或取消事件等待以阻止后续唤醒；不撤销已发生的业务影响。cancelling 不是最终结果，外部影响未知时核对原业务记录，不重放。", InputSchema: taskSchema(map[string]any{"id": map[string]any{"type": "string"}}, "id"), Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in struct {
				ID string `json:"id"`
			}
			if err := decodeTaskInput(string(raw), &in); err != nil {
				return "", err
			}
			row, err := req.Tasks.Cancel(scope, in.ID, allowed)
			if err != nil {
				return "", err
			}
			return taskJSON(row)
		}},
	}
	if req.AllowTaskSuspend {
		tools = append(tools, ai.Tool{Name: "task_suspend", Completion: ai.ToolCompletionAsynchronous, Effect: ai.ToolEffectExecute, Description: "主动挂起当前推理，等待已启动调用的任意/全部终态或期限，再由宿主唤醒。不会重复轮询模型；checkpoint 保存用户目标、约束、待办与证据句柄，不含隐藏推理或新授权。ids 可为空以仅等待计时事件。由你选择条件和后续编排；恢复后写入仍重新确认。", InputSchema: taskSchema(map[string]any{"ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 128}, "all": map[string]any{"type": "boolean"}, "timeout_ms": map[string]any{"type": "integer", "minimum": 1, "maximum": 86400000}, "checkpoint": map[string]any{"type": "string", "maxLength": 8192}}, "ids", "timeout_ms", "checkpoint"), Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in struct {
				IDs        []string `json:"ids"`
				All        bool     `json:"all"`
				Timeout    int      `json:"timeout_ms"`
				Checkpoint string   `json:"checkpoint"`
			}
			if err := decodeTaskInput(string(raw), &in); err != nil {
				return "", err
			}
			if in.Timeout < 1 || in.Timeout > 86400000 {
				return "", errors.New("invalid suspension timeout")
			}
			spec := ai.TaskWaitSpec{IDs: in.IDs, All: in.All, Timeout: time.Duration(in.Timeout) * time.Millisecond, Checkpoint: in.Checkpoint}
			wait, err := req.Tasks.Suspend(ctx, scope, req.InvocationScope, "suspend", spec, allowed)
			if err != nil {
				return "", err
			}
			payload, _ := json.Marshal(struct {
				Kind string            `json:"kind"`
				Wait ai.TaskWaitRecord `json:"wait"`
			}{"task_wait", wait})
			return "", compose.StatefulInterrupt(ctx, string(payload), string(payload))
		}})
	}
	for i := range tools {
		tools[i].LoadMode = ai.ToolLoadImmediate
	}
	return tools, nil
}

// startTask 在当前同步边界完成原工具的发现、原生批准与防重放，再把原 Handler 交给执行器。
// 原生批准中断发生在创建 goroutine 之前，不能借后台调度绕过 checkpoint。
func (m *turnToolMiddleware) startTask(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
	var in taskStartInput
	if err := decodeTaskInput(input.Arguments, &in); err != nil {
		return nil, err
	}
	t, ok := m.tools[in.Tool]
	if !ok || taskKernelName(in.Tool) || t.Handler == nil {
		return nil, errors.New("task target unavailable")
	}
	if !m.callable(in.Tool) {
		interrupted, _, _ := compose.GetInterruptState[string](ctx)
		if !m.taskApproval || !interrupted {
			return &compose.ToolOutput{Result: deferredToolNotLoadedResult(in.Tool)}, nil
		}
	}
	var obj map[string]any
	if json.Unmarshal(in.Arguments, &obj) != nil || obj == nil {
		return nil, errors.New("task arguments must be an object")
	}
	var canonical any
	if json.Unmarshal(in.Arguments, &canonical) != nil {
		return nil, errors.New("invalid task arguments")
	}
	in.Arguments, _ = json.Marshal(canonical)
	delegated := &compose.ToolInput{Name: in.Tool, Arguments: string(in.Arguments), CallID: input.CallID}
	schedule := func(callCtx context.Context, original *compose.ToolInput) (*compose.ToolOutput, error) {
		key := ai.ToolInvocationKey(callCtx)
		if key == "" {
			key = input.CallID
		}
		row, err := m.tasks.Start(callCtx, m.taskScope, m.sessionID, key, t, json.RawMessage(original.Arguments))
		if err != nil {
			return nil, err
		}
		result, err := taskJSON(row)
		return &compose.ToolOutput{Result: result}, err
	}
	originalTools := make([]ai.Tool, 0, len(m.tools))
	for _, tool := range m.tools {
		originalTools = append(originalTools, tool)
	}
	guarded := approvalMiddleware(originalTools, m.taskApproval, m).Invokable(m.guardInvokable(schedule))
	output, err := guarded(context.WithValue(ctx, taskSchedulingKey{}, true), delegated)
	if m.rejected(in.Tool, input.CallID) {
		m.mu.Lock()
		m.rejectedCalls[toolCallKey(input.Name, input.CallID)] = struct{}{}
		m.mu.Unlock()
	}
	return output, err
}

type taskSchedulingKey struct{}

// recordInvocation 复用原端点，不为各接口编写持久化分支；控制工具自身不递归记账。
func (m *turnToolMiddleware) recordInvocation(ctx context.Context, input *compose.ToolInput, next compose.InvokableToolEndpoint) (*compose.ToolOutput, error) {
	scheduling, _ := ctx.Value(taskSchedulingKey{}).(bool)
	if m.tasks == nil || scheduling || taskKernelName(input.Name) {
		return next(ctx, input)
	}
	t, ok := m.tools[input.Name]
	if !ok {
		return next(ctx, input)
	}
	key := ai.ToolInvocationKey(ctx)
	if key == "" {
		key = input.CallID
	}
	original := t
	original.Handler = func(callCtx context.Context, args json.RawMessage) (string, error) {
		copied := *input
		copied.Arguments = string(args)
		output, err := next(callCtx, &copied)
		if output == nil {
			return "", err
		}
		return output.Result, err
	}
	result, err := m.tasks.Call(ctx, m.taskScope, m.sessionID, key, original, json.RawMessage(input.Arguments))
	return &compose.ToolOutput{Result: result}, err
}
