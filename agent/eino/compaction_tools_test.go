package einoengine

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/schema"
	ai "github.com/zdypro888/ihtml/agent"
)

// 原生摘要真正移除 tool_search 消息后，已选工具仍须可见；不能靠提高循环上限掩盖死循环。
func TestNativeCompactionPreservesNewlySelectedTool(t *testing.T) {
	for _, managed := range []bool{false, true} {
		t.Run(fmt.Sprint(managed), func(t *testing.T) { testNativeCompactionSelection(t, managed) })
	}
}
func testNativeCompactionSelection(t *testing.T, managed bool) {
	phase, summaries, calls := 0, 0, 0
	state := &scriptedModelState{}
	state.fn = func(_ []*schema.Message, tools []*schema.ToolInfo) (*schema.Message, error) {
		if len(tools) == 0 {
			summaries++
			return schema.AssistantMessage("任务：读取 inspect_plan。工具选择及执行证据由当前宿主维护。", nil), nil
		}
		phase++
		switch phase {
		case 1:
			return schema.AssistantMessage("", []schema.ToolCall{{ID: "select-plan", Type: "function", Function: schema.FunctionCall{Name: "tool_search", Arguments: `{"query":"select:inspect_plan"}`}}}), nil
		case 2:
			visible := false
			for _, tool := range tools {
				visible = visible || tool.Name == "inspect_plan"
			}
			if !visible {
				return nil, fmt.Errorf("selected tool disappeared during compaction")
			}
			return schema.AssistantMessage("", []schema.ToolCall{{ID: "read-plan", Type: "function", Function: schema.FunctionCall{Name: "inspect_plan", Arguments: `{}`}}}), nil
		default:
			return schema.AssistantMessage("plan verified", nil), nil
		}
	}
	engine := newNativeTestEngine(&scriptedModel{state: state}, nil)
	if managed {
		engine.runtime = session.NewInMemoryStore[*schema.Message](nil)
	}
	engine.cfg.SummarizeAfterMessages = 1
	result, err := engine.RunTurn(context.Background(), &ai.TurnRequest{SessionID: "42", SessionCapability: "fixture", UserText: "inspect fixture plan", InvocationScope: "compaction-fixture", Tools: []ai.Tool{{Name: "inspect_plan", Effect: ai.ToolEffectRead, LoadMode: ai.ToolLoadDeferred, InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}, Handler: func(context.Context, json.RawMessage) (string, error) { calls++; return `{"state":"ready"}`, nil }}}})
	if err != nil || result == nil || result.FinishReason == "agent_error" || calls != 1 || summaries < 1 || phase != 3 || result.ToolExposure.ContextCompactions != summaries {
		t.Fatalf("native selection/compaction failed: err=%v calls=%d summaries=%d phase=%d", err, calls, summaries, phase)
	}
}
