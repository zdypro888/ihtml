package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	einoModel "github.com/cloudwego/eino/components/model"
	einoTool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"github.com/zdypro888/ihtml"
)

type usageReportingModel struct{}

func (usageReportingModel) WithTools([]*schema.ToolInfo) (einoModel.ToolCallingChatModel, error) {
	return usageReportingModel{}, nil
}
func (usageReportingModel) Generate(context.Context, []*schema.Message, ...einoModel.Option) (*schema.Message, error) {
	message := schema.AssistantMessage("generated", nil)
	message.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}}
	return message, nil
}
func (usageReportingModel) Stream(context.Context, []*schema.Message, ...einoModel.Option) (*schema.StreamReader[*schema.Message], error) {
	first := schema.AssistantMessage("a", nil)
	first.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}}
	last := schema.AssistantMessage("b", nil)
	last.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 4, CompletionTokens: 3, TotalTokens: 7}}
	return schema.StreamReaderFromArray([]*schema.Message{first, last}), nil
}

func countingTool(rt *runtime, name string, calls *atomic.Int64) *semanticTool {
	return &semanticTool{rt: rt, def: toolDef{
		Name: name, Title: name, Risk: ihtml.ChatRiskLow,
		Execute: func(context.Context, *runtime, map[string]any) (any, string, error) {
			calls.Add(1)
			return map[string]any{"executed": true}, "executed", nil
		},
	}}
}

func decodedToolBudgetResult(t *testing.T, raw string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("tool result is not JSON: %q: %v", raw, err)
	}
	return out
}

func TestToolBudgetCanonicalizesJSONKeyOrderAndReturnsStableFailure(t *testing.T) {
	rt, sink, _ := newTestRuntime(t)
	rt.mode = AgentModeDeep
	rt.timeline.mode = AgentModeDeep
	rt.budget = newTurnBudget(Config{
		MaxToolCallsPerTurn:  -1,
		MaxToolCallsPerTool:  -1,
		MaxRepeatedToolCalls: 1,
		MaxTurnDuration:      -1,
	}, AgentModeDeep, sink.emit)
	var executed atomic.Int64
	tool := countingTool(rt, "canonical_read", &executed)

	first, err := tool.InvokableRun(context.Background(), `{"outer":{"b":2,"a":1},"z":3}`)
	if err != nil || decodedToolBudgetResult(t, first)["status"] != "ok" {
		t.Fatalf("first call = %s, %v", first, err)
	}
	second, err := tool.InvokableRun(context.Background(), `{"z":3,"outer":{"a":1,"b":2}}`)
	if err != nil {
		t.Fatalf("budget rejection must be a tool result, not an agent-fatal error: %v", err)
	}
	payload := decodedToolBudgetResult(t, second)
	if payload["status"] != toolBudgetExhaustedStatus || payload["code"] != toolBudgetExhaustedCode || payload["limit_kind"] != toolBudgetIdentical {
		t.Fatalf("unstable budget result = %#v", payload)
	}
	if executed.Load() != 1 || rt.budget.snapshot().ToolCalls != 1 {
		t.Fatalf("rejected duplicate executed: executions=%d budget=%+v", executed.Load(), rt.budget.snapshot())
	}

	var budgetError, rejectedTool bool
	sink.mu.Lock()
	for _, ev := range sink.events {
		if ev.Type == ihtml.ChatStreamAssistantBudget && ev.Agent != nil && ev.Agent.Status == "error" &&
			strings.Contains(ev.Agent.Summary, errToolCallBudget.Error()) && ev.Status == ev.Agent.Summary &&
			ev.Budget != nil && ev.Budget.ToolBudgetCode == ihtml.ChatToolBudgetExhaustedCode &&
			ev.Budget.ToolBudgetLimit == ihtml.ChatToolBudgetLimitIdentical && ev.Budget.ToolBudgetTool == "canonical_read" {
			budgetError = true
		}
		if ev.Type == ihtml.ChatStreamToolResult && ev.ToolName == "canonical_read" && ev.ToolStatus == ihtml.ChatToolError {
			rejectedTool = true
		}
	}
	sink.mu.Unlock()
	if !budgetError || !rejectedTool {
		t.Fatalf("missing structured rejection events: budget=%v tool=%v", budgetError, rejectedTool)
	}
}

func TestToolBudgetAdmissionIsAtomicUnderConcurrency(t *testing.T) {
	rt, sink, _ := newTestRuntime(t)
	rt.budget = newTurnBudget(Config{
		MaxToolCallsPerTurn:  7,
		MaxToolCallsPerTool:  -1,
		MaxRepeatedToolCalls: -1,
		MaxTurnDuration:      -1,
	}, AgentModeDeep, sink.emit)
	var executed atomic.Int64
	tool := countingTool(rt, "parallel_read", &executed)

	const attempts = 64
	var wg sync.WaitGroup
	var rejected atomic.Int64
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out, err := tool.InvokableRun(context.Background(), fmt.Sprintf(`{"request":%d}`, i))
			if err != nil {
				t.Errorf("call %d: %v", i, err)
				return
			}
			if strings.Contains(out, `"code":"`+toolBudgetExhaustedCode+`"`) {
				rejected.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if got := executed.Load(); got != 7 {
		t.Fatalf("atomic admission executed %d calls, want 7", got)
	}
	if got := rejected.Load(); got != attempts-7 {
		t.Fatalf("rejected %d calls, want %d", got, attempts-7)
	}
	if got := rt.budget.snapshot().ToolCalls; got != 7 {
		t.Fatalf("snapshot tool calls = %d, want 7", got)
	}
}

func findInvokableTool(t *testing.T, tools []einoTool.BaseTool, name string) einoTool.InvokableTool {
	t.Helper()
	for _, candidate := range tools {
		info, err := candidate.Info(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if info.Name != name {
			continue
		}
		invokable, ok := candidate.(einoTool.InvokableTool)
		if !ok {
			t.Fatalf("tool %q is not invokable", name)
		}
		return invokable
	}
	t.Fatalf("tool %q not found", name)
	return nil
}

func TestToolBudgetSharedAcrossRootAndSubagentWithToolsSets(t *testing.T) {
	rt, sink, _ := newTestRuntime(t)
	rt.budget = newTurnBudget(Config{
		MaxToolCallsPerTurn:  -1,
		MaxToolCallsPerTool:  1,
		MaxRepeatedToolCalls: -1,
		MaxTurnDuration:      -1,
	}, AgentModeDeep, sink.emit)
	// ADK 会分别把这两组工具绑定到根/子 Agent 的 WithTools
	// 派生模型。工具实例不同，但通过同一 runtime 共享预算。
	root := findInvokableTool(t, toolsNodeConfig(rt).Tools, "ui_item_list")
	subagent := findInvokableTool(t, readOnlyToolsNodeConfig(rt).Tools, "ui_item_list")
	if _, err := root.InvokableRun(context.Background(), `{}`); err != nil {
		t.Fatal(err)
	}
	out, err := subagent.InvokableRun(context.Background(), `{"offset":1}`)
	if err != nil {
		t.Fatal(err)
	}
	payload := decodedToolBudgetResult(t, out)
	if payload["code"] != toolBudgetExhaustedCode || payload["limit_kind"] != toolBudgetPerTool {
		t.Fatalf("subagent escaped root per-tool budget: %#v", payload)
	}
	if got := rt.budget.snapshot().ToolCalls; got != 1 {
		t.Fatalf("shared tool calls = %d, want 1", got)
	}
}

func TestInternalPlanUpdatesDoNotConsumeSemanticToolBudget(t *testing.T) {
	rt, sink, _ := newTestRuntime(t)
	rt.timeline.mode = AgentModeDeep
	rt.budget = newTurnBudget(Config{
		MaxToolCallsPerTurn:  1,
		MaxToolCallsPerTool:  -1,
		MaxRepeatedToolCalls: -1,
		MaxTurnDuration:      -1,
	}, AgentModeDeep, sink.emit)
	plan := &deepPlanTool{timeline: rt.timeline}
	for i := 0; i < 3; i++ {
		out, err := plan.InvokableRun(context.Background(), fmt.Sprintf(`{"todos":[{"id":"p%d","content":"step","status":"pending"}]}`, i))
		if err != nil || !strings.Contains(out, `"status":"ok"`) {
			t.Fatalf("plan update %d = %s, %v", i, out, err)
		}
	}
	if got := rt.budget.snapshot().ToolCalls; got != 0 {
		t.Fatalf("internal plan consumed semantic budget: %d", got)
	}
	var executed atomic.Int64
	semantic := countingTool(rt, "one_real_tool", &executed)
	if out, err := semantic.InvokableRun(context.Background(), `{}`); err != nil || decodedToolBudgetResult(t, out)["status"] != "ok" {
		t.Fatalf("semantic call after plans = %s, %v", out, err)
	}
	if out, err := semantic.InvokableRun(context.Background(), `{"next":true}`); err != nil || decodedToolBudgetResult(t, out)["code"] != toolBudgetExhaustedCode {
		t.Fatalf("second semantic call was not limited = %s, %v", out, err)
	}
}

func TestNegativeToolBudgetsAreUnlimited(t *testing.T) {
	b := newTurnBudget(Config{
		MaxToolCallsPerTurn: -1, MaxToolCallsPerTool: -2,
		MaxRepeatedToolCalls: -3, MaxTurnDuration: -1,
	}, AgentModeFast, nil)
	for i := 0; i < 100; i++ {
		if err := b.takeToolCall(context.Background(), "same", map[string]any{"same": true}); err != nil {
			t.Fatalf("negative limit rejected call %d: %v", i, err)
		}
	}
	if got := b.snapshot().ToolCalls; got != 100 {
		t.Fatalf("unlimited tool calls = %d", got)
	}
}

func TestIdenticalWritesExecuteOnlyOnceEvenWhenReadRepetitionIsUnlimited(t *testing.T) {
	rt, sink, _ := newTestRuntime(t)
	rt.budget = newTurnBudget(Config{
		MaxToolCallsPerTurn: -1, MaxToolCallsPerTool: -1,
		MaxRepeatedToolCalls: -1, MaxTurnDuration: -1,
	}, AgentModeFast, sink.emit)
	var executed atomic.Int64
	write := &semanticTool{rt: rt, def: toolDef{
		Name: "canonical_write", Title: "write", Risk: ihtml.ChatRiskLow, Write: true,
		Execute: func(context.Context, *runtime, map[string]any) (any, string, error) {
			executed.Add(1)
			return map[string]any{"ok": true}, "done", nil
		},
	}}
	first, err := write.InvokableRun(context.Background(), `{"b":2,"a":1}`)
	if err != nil || !strings.Contains(first, `"status":"ok"`) {
		t.Fatalf("first write = %s, %v", first, err)
	}
	second, err := write.InvokableRun(context.Background(), `{"a":1,"b":2}`)
	if err != nil || !strings.Contains(second, `"code":"`+toolBudgetExhaustedCode+`"`) {
		t.Fatalf("duplicate write = %s, %v", second, err)
	}
	if executed.Load() != 1 {
		t.Fatalf("identical write executed %d times", executed.Load())
	}
	if !toolsNodeConfig(rt).ExecuteSequentially {
		t.Fatal("root semantic tools must execute sequentially")
	}
}

func TestBudgetedModelAggregatesGenerateAndCumulativeStreamUsage(t *testing.T) {
	budget := newTurnBudget(Config{MaxModelCallsPerTurn: -1, MaxTurnDuration: -1}, AgentModeFast, nil)
	model := withTurnBudget(usageReportingModel{}, budget)
	if _, err := model.Generate(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	stream, err := model.Stream(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	for {
		_, err = stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	usage := budget.usageSnapshot()
	if usage == nil || usage.InputTokens != 7 || usage.OutputTokens != 5 || usage.TotalTokens != 12 {
		t.Fatalf("aggregated usage = %+v", usage)
	}
}
