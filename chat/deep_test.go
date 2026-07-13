package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	einoModel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/zdypro888/ihtml"
)

type stubToolCallingModel struct {
	mu    sync.Mutex
	calls int
}

func (m *stubToolCallingModel) WithTools(_ []*schema.ToolInfo) (einoModel.ToolCallingChatModel, error) {
	return m, nil
}

func (m *stubToolCallingModel) Generate(context.Context, []*schema.Message, ...einoModel.Option) (*schema.Message, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	return schema.AssistantMessage("ok", nil), nil
}

func (m *stubToolCallingModel) Stream(context.Context, []*schema.Message, ...einoModel.Option) (*schema.StreamReader[*schema.Message], error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("ok", nil)}), nil
}

type dynamicToolSearchModelState struct {
	mu            sync.Mutex
	calls         int
	toolsPerCall  [][]string
	sawListResult bool
}

type dynamicToolSearchModel struct {
	state *dynamicToolSearchModelState
	tools []*schema.ToolInfo
}

func (m *dynamicToolSearchModel) WithTools(tools []*schema.ToolInfo) (einoModel.ToolCallingChatModel, error) {
	clone := *m
	clone.tools = append([]*schema.ToolInfo(nil), tools...)
	return &clone, nil
}

func (m *dynamicToolSearchModel) Generate(_ context.Context, input []*schema.Message, opts ...einoModel.Option) (*schema.Message, error) {
	return m.respond(input, opts...)
}

func (m *dynamicToolSearchModel) Stream(_ context.Context, input []*schema.Message, opts ...einoModel.Option) (*schema.StreamReader[*schema.Message], error) {
	response, err := m.respond(input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{response}), nil
}

func (m *dynamicToolSearchModel) respond(input []*schema.Message, opts ...einoModel.Option) (*schema.Message, error) {
	options := einoModel.GetCommonOptions(&einoModel.Options{
		Tools: append([]*schema.ToolInfo(nil), m.tools...),
	}, opts...)
	visible := make(map[string]bool, len(options.Tools))
	names := make([]string, 0, len(options.Tools))
	for _, info := range options.Tools {
		if info == nil {
			continue
		}
		visible[info.Name] = true
		names = append(names, info.Name)
	}
	if len(input) == 0 || input[len(input)-1] == nil {
		return nil, errors.New("dynamic tool-search model received no input")
	}
	last := input[len(input)-1]

	m.state.mu.Lock()
	defer m.state.mu.Unlock()
	m.state.calls++
	m.state.toolsPerCall = append(m.state.toolsPerCall, names)

	switch {
	case last.Role == schema.Tool && last.ToolName == "ui_item_list":
		if !visible["ui_item_list"] {
			return nil, fmt.Errorf("selected tool disappeared before final response: %v", visible)
		}
		if !strings.Contains(last.Content, `"count":1`) || !strings.Contains(last.Content, "alpha") {
			return nil, fmt.Errorf("ui_item_list was not executed against the test store: %s", last.Content)
		}
		m.state.sawListResult = true
		return schema.AssistantMessage("<p>已读取 alpha</p>", nil), nil
	case last.Role == schema.Tool && last.ToolName == "tool_search":
		if !visible["tool_search"] || !visible["ui_plan_update"] || !visible["ui_item_list"] {
			return nil, fmt.Errorf("deferred tool was not activated after tool_search: %v", visible)
		}
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID: "list-1", Type: "function",
			Function: schema.FunctionCall{Name: "ui_item_list", Arguments: `{}`},
		}}), nil
	default:
		if !visible["tool_search"] || !visible["ui_plan_update"] || visible["ui_item_list"] {
			return nil, fmt.Errorf("unexpected initial Deep tools: %v", visible)
		}
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID: "search-1", Type: "function",
			Function: schema.FunctionCall{Name: "tool_search", Arguments: `{"query":"select:ui_item_list"}`},
		}}), nil
	}
}

func TestSelectAgentModeFastDefaultAndDeepOptIn(t *testing.T) {
	mode, err := selectAgentMode(Config{}, "")
	if err != nil || mode != AgentModeFast {
		t.Fatalf("default mode = %q, %v", mode, err)
	}
	if _, err := selectAgentMode(Config{}, AgentModeDeep); err == nil {
		t.Fatal("deep must require a server-side feature flag")
	}
	mode, err = selectAgentMode(Config{EnableDeepAgent: true}, " DEEP ")
	if err != nil || mode != AgentModeDeep {
		t.Fatalf("enabled deep mode = %q, %v", mode, err)
	}
	if _, err := selectAgentMode(Config{EnableDeepAgent: true}, "unknown"); err == nil {
		t.Fatal("unknown agent mode accepted")
	}
}

func TestNewAppliesBoundedAgentDefaults(t *testing.T) {
	b := New(Config{})
	if b.cfg.MaxModelCallsPerTurn != 24 || b.cfg.MaxSubtasksPerTurn != 3 ||
		b.cfg.MaxModelRetries != 1 ||
		b.cfg.MaxToolCallsPerTurn != defaultMaxToolCallsPerTurn ||
		b.cfg.MaxToolCallsPerTool != defaultMaxToolCallsPerTool ||
		b.cfg.MaxRepeatedToolCalls != defaultMaxRepeatedToolCalls ||
		b.cfg.SummarizeAfterTokens != defaultSummarizeAfterTokens ||
		b.cfg.SummarizeAfterMessages != defaultSummarizeAfterMessages ||
		b.cfg.MaxProviderResponseBytes != defaultMaxProviderResponseBytes ||
		b.cfg.MaxModelOutputBytesPerCall != defaultMaxModelOutputBytesPerCall ||
		b.cfg.MaxModelOutputBytesPerTurn != defaultMaxModelOutputBytesPerTurn ||
		b.cfg.MaxModelOutputEventsPerTurn != defaultMaxModelOutputEventsPerTurn ||
		b.cfg.MaxAssistantMessageBytes != defaultMaxAssistantMessageBytes ||
		b.cfg.MaxReasoningBytesPerTurn != defaultMaxReasoningBytesPerTurn ||
		b.cfg.MaxTurnDuration != 2*time.Minute {
		t.Fatalf("agent defaults = model:%d retries:%d subtasks:%d tools:%d/%d/%d summary:%d/%d output:%d/%d/%d/%d visible:%d reasoning:%d duration:%s",
			b.cfg.MaxModelCallsPerTurn, b.cfg.MaxModelRetries, b.cfg.MaxSubtasksPerTurn,
			b.cfg.MaxToolCallsPerTurn, b.cfg.MaxToolCallsPerTool, b.cfg.MaxRepeatedToolCalls,
			b.cfg.SummarizeAfterTokens, b.cfg.SummarizeAfterMessages,
			b.cfg.MaxProviderResponseBytes, b.cfg.MaxModelOutputBytesPerCall,
			b.cfg.MaxModelOutputBytesPerTurn, b.cfg.MaxModelOutputEventsPerTurn,
			b.cfg.MaxAssistantMessageBytes, b.cfg.MaxReasoningBytesPerTurn,
			b.cfg.MaxTurnDuration)
	}
}

func TestModelsAdvertisesDeepAndApprovalCapability(t *testing.T) {
	b := New(Config{Provider: ProviderClaude, EnableDeepAgent: true})
	out, err := b.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(out.AgentModes) != 2 || out.AgentModes[0] != AgentModeFast || out.AgentModes[1] != AgentModeDeep {
		t.Fatalf("agent modes = %v", out.AgentModes)
	}
	if out.DeepApprovalResume != "manual_tool_only" {
		t.Fatalf("deep approval capability = %q", out.DeepApprovalResume)
	}
	if out.ProtocolVersion != ihtml.ChatProtocolVersion || out.Provider != ProviderClaude {
		t.Fatalf("protocol/provider = %d/%q", out.ProtocolVersion, out.Provider)
	}
}

func TestReadOnlySubagentToolSetExcludesEveryWrite(t *testing.T) {
	rt, _, _ := newTestRuntime(t)
	config := readOnlyToolsNodeConfig(rt)
	if len(config.Tools) == 0 {
		t.Fatal("read-only subagent has no tools")
	}
	for _, candidate := range config.Tools {
		info, err := candidate.Info(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		def, ok := findToolDef(info.Name)
		if !ok {
			t.Fatalf("unknown read-only tool %q", info.Name)
		}
		if def.Write {
			t.Fatalf("write tool %q leaked into a subagent", info.Name)
		}
	}
}

func TestBuildTurnAgentKeepsFastDefaultAndCreatesDeepAgent(t *testing.T) {
	rt, _, _ := newTestRuntime(t)
	model := &stubToolCallingModel{}
	budget := newTurnBudget(Config{MaxModelCallsPerTurn: 4, MaxSubtasksPerTurn: 2, MaxTurnDuration: -1}, AgentModeDeep, nil)
	fast, err := buildTurnAgent(context.Background(), AgentModeFast, "instruction", model, rt, budget, Config{MaxIterations: 2}, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := fast.Name(context.Background()); got != "ihtml_ui_agent" {
		t.Fatalf("fast agent name = %q", got)
	}
	deepAgent, err := buildTurnAgent(context.Background(), AgentModeDeep, "instruction", model, rt, budget, Config{MaxIterations: 2}, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := deepAgent.Name(context.Background()); got != "ihtml_deep_agent" {
		t.Fatalf("deep agent name = %q", got)
	}
	if _, err := buildTurnAgent(context.Background(), AgentModeDeep, "instruction", model, rt, budget, Config{MaxIterations: 2}, false); err == nil {
		t.Fatal("deep mode silently downgraded when tools were disabled")
	}
}

func TestDeepAgentSearchesAndExecutesDeferredSemanticTool(t *testing.T) {
	ctx := context.Background()
	rt, sink, svc := newTestRuntime(t)
	if err := svc.PutItems(ctx, "t1", []ihtml.Item{{
		ID: "alpha", Type: ihtml.ItemHTML, Content: "<p>alpha</p>",
	}}, "test", "seed"); err != nil {
		t.Fatal(err)
	}

	budgetCfg := Config{
		MaxModelCallsPerTurn: 8,
		MaxSubtasksPerTurn:   2,
		MaxToolCallsPerTurn:  8,
		MaxToolCallsPerTool:  4,
		MaxRepeatedToolCalls: 2,
		MaxTurnDuration:      -1,
	}
	budget := newTurnBudget(budgetCfg, AgentModeDeep, sink.emit)
	rt.mode = AgentModeDeep
	rt.budget = budget
	state := &dynamicToolSearchModelState{}
	model := withTurnBudget(&dynamicToolSearchModel{state: state}, budget)
	agent, err := buildTurnAgent(ctx, AgentModeDeep, "完成用户要求。", model, rt, budget, Config{
		MaxIterations:               8,
		MaxModelRetries:             -1,
		DisableContextSummarization: true,
	}, true)
	if err != nil {
		t.Fatal(err)
	}

	iter := agent.Run(ctx, &adk.AgentInput{
		Messages:        []*schema.Message{schema.UserMessage("列出当前 Item")},
		EnableStreaming: false,
	})
	var final string
	for event, ok := iter.Next(); ok; event, ok = iter.Next() {
		if event == nil {
			continue
		}
		if event.Err != nil {
			t.Fatalf("Deep agent event: %v", event.Err)
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		message := event.Output.MessageOutput.Message
		if message != nil && message.Role == schema.Assistant && len(message.ToolCalls) == 0 {
			final = message.Content
		}
	}

	state.mu.Lock()
	calls := state.calls
	toolsPerCall := append([][]string(nil), state.toolsPerCall...)
	sawListResult := state.sawListResult
	state.mu.Unlock()
	if final != "<p>已读取 alpha</p>" || calls != 3 || !sawListResult {
		t.Fatalf("final=%q calls=%d saw_list_result=%v tools=%v", final, calls, sawListResult, toolsPerCall)
	}
	if got := budget.snapshot().ToolCalls; got != 1 {
		t.Fatalf("semantic tool calls = %d; tool_search must not consume the host tool budget", got)
	}
	evidence := rt.timeline.completedEvidence()
	if len(evidence) != 1 || evidence[0].Tool != "ui_item_list" || !strings.Contains(string(evidence[0].Body), `"count":1`) {
		t.Fatalf("completed semantic-tool evidence = %+v", evidence)
	}
}

func TestTurnBudgetIsSharedAcrossWithToolsModels(t *testing.T) {
	var events []ihtml.ChatStreamEvent
	budget := newTurnBudget(Config{MaxModelCallsPerTurn: 1, MaxSubtasksPerTurn: 1, MaxTurnDuration: -1}, AgentModeDeep, func(ev ihtml.ChatStreamEvent) error {
		events = append(events, ev)
		return nil
	})
	model := withTurnBudget(&stubToolCallingModel{}, budget)
	derived, err := model.WithTools(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := derived.Generate(context.Background(), nil); err != nil {
		t.Fatalf("first model call: %v", err)
	}
	if _, err := model.Generate(context.Background(), nil); !errors.Is(err, errModelCallBudget) {
		t.Fatalf("second shared model call = %v", err)
	}
	if got := budget.snapshot().ModelCalls; got != 2 {
		t.Fatalf("recorded model calls = %d", got)
	}
	if len(events) != 2 || events[0].Type != "assistant_budget" || events[1].Agent == nil || events[1].Agent.Status != "error" {
		t.Fatalf("budget events = %+v", events)
	}
}

func TestSubtaskBudgetEmitsStructuredStages(t *testing.T) {
	var events []ihtml.ChatStreamEvent
	budget := newTurnBudget(Config{MaxModelCallsPerTurn: 4, MaxSubtasksPerTurn: 1, MaxTurnDuration: -1}, AgentModeDeep, func(ev ihtml.ChatStreamEvent) error {
		events = append(events, ev)
		return nil
	})
	id, err := budget.startSubtask("ui_inspector", "inspect", "检查页面")
	if err != nil || id == "" {
		t.Fatalf("first subtask = %q, %v", id, err)
	}
	budget.finishSubtask("ui_inspector", id, "inspect", "completed", "完成")
	if _, err := budget.startSubtask("ui_reviewer", "review", "复核页面"); !errors.Is(err, errSubtaskBudget) {
		t.Fatalf("second subtask = %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("subtask events = %d", len(events))
	}
	if events[0].Type != "assistant_agent" || events[0].Agent == nil || events[0].Agent.ParentTaskID != "root" || events[0].Agent.Status != "running" {
		t.Fatalf("start event = %+v", events[0])
	}
	if events[1].Agent.Status != "completed" || events[2].Agent.Status != "error" || events[2].Budget.Subtasks != 2 {
		t.Fatalf("terminal events = %+v", events[1:])
	}
}

func TestSubagentHandlerMapsLifecycleToStructuredEvents(t *testing.T) {
	var events []ihtml.ChatStreamEvent
	budget := newTurnBudget(Config{MaxModelCallsPerTurn: 4, MaxSubtasksPerTurn: 2, MaxTurnDuration: -1}, AgentModeDeep, func(ev ihtml.ChatStreamEvent) error {
		events = append(events, ev)
		return nil
	})
	handler := &subagentStageHandler{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		budget:                       budget, agent: "ui_inspector", phase: "inspect",
	}
	ctx, runCtx, err := handler.BeforeAgent(context.Background(), &adk.ChatModelAgentContext[*schema.Message]{
		AgentInput: &adk.AgentInput{Messages: []*schema.Message{schema.UserMessage("检查首页报错")}},
	})
	if err != nil || runCtx == nil {
		t.Fatalf("before subagent = %v", err)
	}
	if _, err := handler.AfterAgent(ctx, &adk.ChatModelAgentState{}); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Agent == nil || events[0].Agent.Summary != "检查首页报错" || events[1].Agent.Status != "completed" {
		t.Fatalf("subagent lifecycle events = %+v", events)
	}
}

func TestDeepPlanToolEmitsPlanAndExplicitPause(t *testing.T) {
	var events []ihtml.ChatStreamEvent
	tl := &timeline{mode: AgentModeDeep, emit: func(ev ihtml.ChatStreamEvent) error {
		events = append(events, ev)
		return nil
	}}
	planTool := &deepPlanTool{timeline: tl}
	out, err := planTool.InvokableRun(context.Background(), `{"todos":[{"id":"inspect","content":"检查现状","status":"in_progress"},{"id":"implement","content":"实现变更","status":"pending"}]}`)
	if err != nil || !strings.Contains(out, `"status":"ok"`) {
		t.Fatalf("plan update = %s, %v", out, err)
	}
	tl.pausePlan(deepApprovalPausedMessage)
	if len(events) != 2 || events[0].Type != "assistant_plan" || len(events[0].Plan) != 2 {
		t.Fatalf("plan events = %+v", events)
	}
	paused := events[1]
	if paused.Agent == nil || paused.Agent.Status != "paused" || paused.Agent.ApprovalResume != "manual_tool_only" || len(paused.Plan) != 2 {
		t.Fatalf("paused plan = %+v", paused)
	}
	if _, err := planTool.InvokableRun(context.Background(), `{"todos":[{"id":"a","content":"A","status":"in_progress"},{"id":"b","content":"B","status":"in_progress"}]}`); err == nil {
		t.Fatal("multiple active plan items accepted")
	}
}

func TestDeepPendingResponseCannotClaimCompletion(t *testing.T) {
	var events []ihtml.ChatStreamEvent
	tl := &timeline{mode: AgentModeDeep, emit: func(ev ihtml.ChatStreamEvent) error {
		events = append(events, ev)
		return nil
	}}
	resp := enforceDeepConfirmationPause(AgentModeDeep, collectResp{
		Message: "页面已经全部修改并验证完成", Streamed: true,
		ToolCalls: []ihtml.ProposedToolCall{{Name: "ui_item_put", RequiresConfirmation: true}},
	}, tl)
	if resp.Message != deepApprovalPausedMessage || resp.Streamed || resp.ContentFormat != ihtml.ChatContentFormatText {
		t.Fatalf("pending response was not forced to pause: %+v", resp)
	}
	if len(events) != 1 || events[0].Type != "assistant_plan" || events[0].Agent == nil || events[0].Agent.Status != "paused" {
		t.Fatalf("missing explicit paused plan event: %+v", events)
	}
	fast := enforceDeepConfirmationPause(AgentModeFast, collectResp{Message: "fast", ToolCalls: resp.ToolCalls}, tl)
	if fast.Message != "fast" {
		t.Fatalf("fast response changed: %+v", fast)
	}
}

func TestDeepConfirmationPausesFurtherToolsAndDoesNotClaimPlanComplete(t *testing.T) {
	rt, _, _ := newTestRuntime(t)
	rt.mode = AgentModeDeep
	rt.timeline.mode = AgentModeDeep
	rt.cfg.AllowUnconfirmedItemWrites = false

	deletePage := findTool(t, rt, "ui_page_delete")
	out, err := deletePage.InvokableRun(context.Background(), `{"name":"billing"}`)
	if err != nil || !strings.Contains(out, "pending_confirmation") {
		t.Fatalf("deep pending = %s, %v", out, err)
	}
	pending := rt.timeline.pendingCalls()
	if len(pending) != 1 || pending[0].AgentMode != AgentModeDeep || pending[0].ApprovalResume != "manual_tool_only" {
		t.Fatalf("deep confirmation metadata = %+v", pending)
	}
	list := findTool(t, rt, "ui_item_list")
	blocked, err := list.InvokableRun(context.Background(), `{}`)
	if err != nil || !strings.Contains(blocked, "confirmation_pending") {
		t.Fatalf("tool after confirmation boundary = %s, %v", blocked, err)
	}

	var confirmEvents []ihtml.ChatStreamEvent
	if err := executeConfirmedTool(context.Background(), rt, &pending[0], func(ev ihtml.ChatStreamEvent) error {
		confirmEvents = append(confirmEvents, ev)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	last := confirmEvents[len(confirmEvents)-1]
	if last.Type != "assistant_done" || !strings.Contains(last.Message, "不会自动恢复") || strings.Contains(last.Message, "整体任务已完成") {
		t.Fatalf("deep confirmation final = %+v", last)
	}
	foundStage := false
	for _, ev := range confirmEvents {
		if ev.Type == "assistant_agent" && ev.Agent != nil && ev.Agent.Phase == "approval" && ev.Agent.ApprovalResume == "manual_tool_only" {
			foundStage = true
		}
	}
	if !foundStage {
		raw, _ := json.Marshal(confirmEvents)
		t.Fatalf("missing structured approval stage: %s", raw)
	}
	if !confirmationReturnDirectly(rt.cfg)["ui_page_delete"] {
		t.Fatal("deep confirmation tool is not a return-direct boundary")
	}
}

func TestDeepReadToolDoesNotAdvertiseApprovalPause(t *testing.T) {
	rt, sink, _ := newTestRuntime(t)
	rt.mode = AgentModeDeep
	rt.timeline.mode = AgentModeDeep

	out, err := findTool(t, rt, "ui_item_list").InvokableRun(context.Background(), `{}`)
	if err != nil || !strings.Contains(out, `"status":"ok"`) {
		t.Fatalf("deep read = %s, %v", out, err)
	}
	for _, ev := range sink.events {
		if ev.ToolCall != nil && ev.ToolCall.ApprovalResume != "" {
			t.Fatalf("read-only tool incorrectly paused the Deep plan: %+v", ev.ToolCall)
		}
	}
}
