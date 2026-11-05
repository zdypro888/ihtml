package einoengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/schema"
	ai "github.com/zdypro888/ihtml/agent"
)

// 直接走 Eino 模型/工具边界验证预算，不能只测试调用方主动调用 ChargeGoal。
func TestEinoCumulativeModelBudgetAndQuoteAdmission(t *testing.T) {
	for _, cost := range []bool{false, true} {
		t.Run(fmt.Sprint(cost), func(t *testing.T) {
			limits := ai.TaskGoalLimits{ModelCalls: 1}
			if cost {
				limits.CostMicros = 1
			}
			runtime, err := ai.NewTaskRuntime(t.Context(), ai.TaskConfig{GoalLimits: limits})
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Close()
			calls := 0
			state := &scriptedModelState{fn: func(_ []*schema.Message, _ []*schema.ToolInfo) (*schema.Message, error) {
				calls++
				return taskModelCall("budget", "task_budget", `{}`), nil
			}}
			engine := newNativeTestEngine(&scriptedModel{state: state}, nil)
			_, err = engine.RunTurn(t.Context(), &ai.TurnRequest{SessionID: "budget", Tasks: runtime, UserText: "check budget", MaxIterations: 5})
			want := 1
			if cost {
				want = 0
			}
			if calls != want {
				t.Fatal("budget not at actual model boundary", calls, err)
			}
		})
	}
}

func TestEinoNativeResultBackendAndReferenceAwareHandler(t *testing.T) {
	runtime, err := ai.NewTaskRuntime(t.Context(), ai.TaskConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	source := ai.Tool{Name: "source", Effect: ai.ToolEffectRead, Handler: func(context.Context, json.RawMessage) (string, error) { return "first\nsecond\nthird", nil }}
	scope := "fixture"
	info, err := runtime.Start(t.Context(), scope, "turn", "one", source, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := runtime.Wait(t.Context(), scope, []string{info.ID}, map[string]bool{"source": true}, time.Second, true)
	if err != nil {
		t.Fatal(err)
	}
	ref := rows[0].ResultRef
	backend := &ResultBackend{Runtime: runtime, Scope: scope, Allowed: map[string]bool{"source": true}}
	path := "/results/" + info.ID + "/" + ref.SHA256
	data, err := backend.Read(t.Context(), &filesystem.ReadRequest{FilePath: path, Offset: 2, Limit: 1})
	if err != nil || data.Content != "second" {
		t.Fatal(data, err)
	}
	backend.Allowed = nil
	if _, err = backend.Read(t.Context(), &filesystem.ReadRequest{FilePath: path}); !errors.Is(err, ai.ErrTaskNotFound) {
		t.Fatal("source permission lost", err)
	}
	backend.Allowed = map[string]bool{"source": true}
	if _, err = backend.Read(t.Context(), &filesystem.ReadRequest{FilePath: "/etc/passwd"}); err == nil {
		t.Fatal("host path exposed")
	}
}

func TestContractChangesRotateSessionAndCannotReuseInvocation(t *testing.T) {
	tools := []ai.Tool{{Name: "generic", Contract: ai.ToolContract{Version: "1", ResultInputs: true, OutputSchema: map[string]any{"type": "object"}}}}
	old := capabilityFingerprint(tools, "scope")
	tools[0].Contract.Version = "2"
	if old == capabilityFingerprint(tools, "scope") {
		t.Fatal("contract lost session binding")
	}
	if desc := modelToolDescription(tools[0]); !strings.Contains(desc, `"version":"2"`) {
		t.Fatal(desc)
	}
}

// 模型驱动的完整引用流：原调用 → 原生 Eino 文件工具 → 引用消费，运行时没有业务顺序分支。
func TestEinoModelChoosesReferenceWorkflow(t *testing.T) {
	runtime, err := ai.NewTaskRuntime(t.Context(), ai.TaskConfig{MaxResultBytes: 128, GoalLimits: ai.TaskGoalLimits{ModelCalls: 10, ToolCalls: 10}})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	var sourceCalls, consumerCalls int
	state := &scriptedModelState{fn: func(messages []*schema.Message, tools []*schema.ToolInfo) (*schema.Message, error) {
		native := false
		for _, t := range tools {
			if t.Name == "task_result_file" {
				native = true
			}
		}
		if !native {
			return nil, fmt.Errorf("Eino native file adapter missing")
		}
		var ref ai.TaskResultRef
		for _, m := range messages {
			if m.Role == schema.Tool && m.ToolName == "source" {
				var result struct {
					Result *ai.TaskResultRef `json:"result"`
				}
				if json.Unmarshal([]byte(m.Content), &result) != nil || result.Result == nil {
					return nil, fmt.Errorf("missing immutable reference: %s", m.Content)
				}
				ref = *result.Result
			}
		}
		for i := len(messages) - 1; i >= 0; i-- {
			m := messages[i]
			if m.Role != schema.Tool {
				continue
			}
			switch m.ToolName {
			case "source":
				args, _ := json.Marshal(map[string]any{"file_path": "/results/" + ref.ID + "/" + ref.SHA256, "offset": 1, "limit": 1})
				return taskModelCall("file", "task_result_file", string(args)), nil
			case "task_result_file":
				args, _ := json.Marshal(map[string]any{"input": ai.TaskResultInput{ID: ref.ID, SHA256: ref.SHA256, Pointer: "/value"}})
				return taskModelCall("consumer", "consumer", string(args)), nil
			case "consumer":
				if m.Content != "verified" {
					return nil, fmt.Errorf("reference consumer failed: %s", m.Content)
				}
				return schema.AssistantMessage("verified from immutable result", nil), nil
			}
		}
		return taskModelCall("source", "source", `{}`), nil
	}}
	consumer := ai.Tool{Name: "consumer", Effect: ai.ToolEffectRead, LoadMode: ai.ToolLoadImmediate, Contract: ai.ToolContract{Version: "1", ResultInputs: true}, InputSchema: map[string]any{"type": "object", "properties": map[string]any{"input": ai.TaskResultInputSchema()}, "required": []string{"input"}}, ResolveResources: func(context.Context, json.RawMessage) ([]ai.TaskResourceRequest, error) {
		return []ai.TaskResourceRequest{{Key: "synthetic/object", Shared: true, IfVersion: "v1"}}, nil
	}, Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
		consumerCalls++
		var in struct {
			Input ai.TaskResultInput `json:"input"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return "", err
		}
		grants := ai.TaskResourceGrants(ctx)
		if len(grants) != 1 || grants[0].IfVersion != "v1" || grants[0].Fence < 1 {
			return "", fmt.Errorf("resource grant missing")
		}
		data, err := ai.ReadTaskResultInput(ctx, in.Input)
		if err != nil {
			return "", err
		}
		if string(data) != "9007199254740993" {
			return "", fmt.Errorf("integer precision lost")
		}
		return "verified", nil
	}}
	req := &ai.TurnRequest{SessionID: "reference-flow", InvocationScope: "reference-flow-turn", Tasks: runtime, UserText: "inspect immutable facts then verify", Tools: []ai.Tool{{Name: "source", Effect: ai.ToolEffectRead, LoadMode: ai.ToolLoadImmediate, Handler: func(context.Context, json.RawMessage) (string, error) {
		sourceCalls++
		return "{\n\"value\":9007199254740993,\n\"padding\":\"" + strings.Repeat("x", 1000) + "\"\n}", nil
	}}, consumer}}
	result, err := newNativeTestEngine(&scriptedModel{state: state}, nil).RunTurn(t.Context(), req)
	if err != nil || result == nil || result.Text != "verified from immutable result" || sourceCalls != 1 || consumerCalls != 1 {
		t.Fatal(result, err, sourceCalls, consumerCalls)
	}
}
