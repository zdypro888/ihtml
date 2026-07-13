package chat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	einoModel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/zdypro888/ihtml"
)

var (
	errModelCallBudget = errors.New("本轮模型调用预算已用完")
	errSubtaskBudget   = errors.New("本轮子任务预算已用完")
	errToolCallBudget  = errors.New("本轮工具调用安全预算已用完")
)

const (
	defaultMaxToolCallsPerTurn  = 32
	defaultMaxToolCallsPerTool  = 8
	defaultMaxRepeatedToolCalls = 2

	defaultMaxProviderResponseBytes    int64 = 8 << 20
	minMaxProviderResponseBytes        int64 = 1 << 20
	maxMaxProviderResponseBytes        int64 = 64 << 20
	defaultMaxModelOutputBytesPerCall  int64 = 1 << 20
	minMaxModelOutputBytesPerCall      int64 = 64 << 10
	maxMaxModelOutputBytesPerCall      int64 = 16 << 20
	defaultMaxModelOutputBytesPerTurn  int64 = 4 << 20
	minMaxModelOutputBytesPerTurn      int64 = 64 << 10
	maxMaxModelOutputBytesPerTurn      int64 = 64 << 20
	defaultMaxModelOutputEventsPerTurn       = 4096
	minMaxModelOutputEventsPerTurn           = 16
	maxMaxModelOutputEventsPerTurn           = 65536
	defaultMaxAssistantMessageBytes    int64 = 256 << 10
	minMaxAssistantMessageBytes        int64 = 16 << 10
	maxMaxAssistantMessageBytes        int64 = 512 << 10
	defaultMaxReasoningBytesPerTurn    int64 = 256 << 10
	minMaxReasoningBytesPerTurn        int64 = 16 << 10
	maxMaxReasoningBytesPerTurn        int64 = 768 << 10
	maxReasoningBytesPerSegment        int64 = 64 << 10

	toolBudgetExhaustedStatus = ihtml.ChatToolBudgetStatusExhausted
	toolBudgetExhaustedCode   = ihtml.ChatToolBudgetExhaustedCode

	toolBudgetTotal     = ihtml.ChatToolBudgetLimitTotal
	toolBudgetPerTool   = ihtml.ChatToolBudgetLimitPerTool
	toolBudgetIdentical = ihtml.ChatToolBudgetLimitIdentical
)

// ErrModelOutputLimit is the stable sentinel for a locally enforced model
// output boundary. It deliberately does not expose provider response content.
var ErrModelOutputLimit = errors.New("chat: model output limit exceeded")

const (
	ModelOutputLimitCallBytes             = "call_bytes"
	ModelOutputLimitTurnBytes             = "turn_bytes"
	ModelOutputLimitTurnEvents            = "turn_events"
	ModelOutputLimitProviderResponseBytes = "provider_response_bytes"
)

// ModelOutputLimitError identifies which local model-output boundary rejected
// an otherwise successful provider response. Callers should use errors.As or
// errors.Is(err, ErrModelOutputLimit), not match Error text.
type ModelOutputLimitError struct {
	Scope string
	Limit int64
}

func (e *ModelOutputLimitError) Error() string {
	if e == nil {
		return ErrModelOutputLimit.Error()
	}
	return fmt.Sprintf("%s: %s limit %d", ErrModelOutputLimit, e.Scope, e.Limit)
}

func (e *ModelOutputLimitError) Unwrap() error { return ErrModelOutputLimit }

// toolBudgetError 是语义工具返回给 Agent 的稳定限额错误。
// kind 会同时出现在 JSON 结果中，宿主无需匹配中文文案。
type toolBudgetError struct {
	kind  string
	tool  string
	limit int64
}

func (e *toolBudgetError) Error() string {
	if e == nil {
		return errToolCallBudget.Error()
	}
	switch e.kind {
	case toolBudgetTotal:
		return fmt.Sprintf("%s：总调用数已达上限 %d", errToolCallBudget, e.limit)
	case toolBudgetPerTool:
		return fmt.Sprintf("%s：工具 %q 调用数已达上限 %d", errToolCallBudget, e.tool, e.limit)
	case toolBudgetIdentical:
		return fmt.Sprintf("%s：工具 %q 的完全相同参数已达上限 %d", errToolCallBudget, e.tool, e.limit)
	default:
		return errToolCallBudget.Error()
	}
}

func (e *toolBudgetError) Unwrap() error { return errToolCallBudget }

// turnBudget 被一轮的根 Agent、所有只读子 Agent 和它们派生出的 WithTools
// 模型共享，防止一次 deep 请求绕过按 turn 计数的外层配额。
type turnBudget struct {
	mode                  string
	maxModels             int64
	maxSubtasks           int64
	maxTools              int64
	maxPerTool            int64
	maxRepeated           int64
	maxOutputBytesPerCall int64
	maxOutputBytesPerTurn int64
	maxOutputEvents       int64
	deadline              time.Time
	emit                  func(ihtml.ChatStreamEvent) error

	modelCalls   atomic.Int64
	subtasks     atomic.Int64
	taskSeq      atomic.Int64
	outputBytes  atomic.Int64
	outputEvents atomic.Int64

	// 三层工具限额必须在同一个临界区内判定和扣减，否则并发
	// 子 Agent 可能同时穿过不同层的上限。exhausted 一旦设置便锁存，
	// 防止 Agent 在触发某一层限额后换参数继续绕过。
	toolMu    sync.Mutex
	toolCalls int64
	perTool   map[string]int64
	repeated  map[[32]byte]int64
	exhausted *toolBudgetError

	usageMu  sync.Mutex
	usage    ihtml.ChatUsage
	hasUsage bool
}

func newTurnBudget(cfg Config, mode string, emit func(ihtml.ChatStreamEvent) error) *turnBudget {
	maxModels := int64(cfg.MaxModelCallsPerTurn)
	if maxModels < 0 {
		maxModels = 0
	}
	maxSubtasks := int64(cfg.MaxSubtasksPerTurn)
	if maxSubtasks < 0 {
		maxSubtasks = 0
	}
	maxTools := int64(cfg.MaxToolCallsPerTurn)
	if maxTools < 0 {
		maxTools = 0
	}
	maxPerTool := int64(cfg.MaxToolCallsPerTool)
	if maxPerTool < 0 {
		maxPerTool = 0
	}
	maxRepeated := int64(cfg.MaxRepeatedToolCalls)
	if maxRepeated < 0 {
		maxRepeated = 0
	}
	maxOutputBytesPerCall := cfg.MaxModelOutputBytesPerCall
	if maxOutputBytesPerCall < minMaxModelOutputBytesPerCall || maxOutputBytesPerCall > maxMaxModelOutputBytesPerCall {
		maxOutputBytesPerCall = defaultMaxModelOutputBytesPerCall
	}
	maxOutputBytesPerTurn := cfg.MaxModelOutputBytesPerTurn
	if maxOutputBytesPerTurn < minMaxModelOutputBytesPerTurn || maxOutputBytesPerTurn > maxMaxModelOutputBytesPerTurn {
		maxOutputBytesPerTurn = defaultMaxModelOutputBytesPerTurn
	}
	if maxOutputBytesPerTurn < maxOutputBytesPerCall {
		maxOutputBytesPerCall = defaultMaxModelOutputBytesPerCall
		maxOutputBytesPerTurn = defaultMaxModelOutputBytesPerTurn
	}
	maxOutputEvents := int64(cfg.MaxModelOutputEventsPerTurn)
	if maxOutputEvents < minMaxModelOutputEventsPerTurn || maxOutputEvents > maxMaxModelOutputEventsPerTurn {
		maxOutputEvents = defaultMaxModelOutputEventsPerTurn
	}
	duration := cfg.MaxTurnDuration
	if duration < 0 {
		duration = 0
	}
	if cfg.RequestTimeout > 0 && (duration == 0 || cfg.RequestTimeout < duration) {
		duration = cfg.RequestTimeout
	}
	b := &turnBudget{
		mode: mode, maxModels: maxModels, maxSubtasks: maxSubtasks,
		maxTools: maxTools, maxPerTool: maxPerTool, maxRepeated: maxRepeated,
		maxOutputBytesPerCall: maxOutputBytesPerCall,
		maxOutputBytesPerTurn: maxOutputBytesPerTurn,
		maxOutputEvents:       maxOutputEvents,
		emit:                  emit, perTool: make(map[string]int64), repeated: make(map[[32]byte]int64),
	}
	if duration > 0 {
		b.deadline = time.Now().Add(duration)
	}
	return b
}

func (b *turnBudget) snapshot() *ihtml.ChatTurnBudget {
	if b == nil {
		return nil
	}
	b.toolMu.Lock()
	toolCalls := b.toolCalls
	var toolBudgetCode, toolBudgetLimit, toolBudgetTool string
	if b.exhausted != nil {
		toolBudgetCode = toolBudgetExhaustedCode
		toolBudgetLimit = b.exhausted.kind
		toolBudgetTool = b.exhausted.tool
	}
	b.toolMu.Unlock()
	out := &ihtml.ChatTurnBudget{
		ModelCalls:           b.modelCalls.Load(),
		MaxModelCalls:        b.maxModels,
		Subtasks:             b.subtasks.Load(),
		MaxSubtasks:          b.maxSubtasks,
		ToolCalls:            toolCalls,
		MaxToolCalls:         b.maxTools,
		MaxToolCallsPerTool:  b.maxPerTool,
		MaxRepeatedToolCalls: b.maxRepeated,
		ToolBudgetCode:       toolBudgetCode,
		ToolBudgetLimit:      toolBudgetLimit,
		ToolBudgetTool:       toolBudgetTool,
	}
	if !b.deadline.IsZero() {
		out.DeadlineUnixMS = b.deadline.UnixMilli()
	}
	return out
}

// takeToolCall 在真正工具校验或执行之前原子地扣减一次额度。
// args 已由 encoding/json 解析；有界流式指纹递归排序对象键，但不会
// 构造或保留一份可能因 HTML escaping 膨胀数倍的 canonical JSON。
func (b *turnBudget) takeToolCall(ctx context.Context, name string, args map[string]any) error {
	return b.takeToolCallWithPolicy(ctx, name, args, false)
}

func (b *turnBudget) takeToolCallWithPolicy(ctx context.Context, name string, args map[string]any, write bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b == nil {
		return nil
	}
	repeatedKey, err := canonicalToolArgumentDigest(name, args)
	if err != nil {
		return fmt.Errorf("fingerprint tool arguments: %w", err)
	}

	b.toolMu.Lock()
	if b.exhausted != nil {
		exhausted := *b.exhausted
		b.toolMu.Unlock()
		b.emitBudget("error", exhausted.Error())
		return &exhausted
	}
	var exhausted *toolBudgetError
	switch {
	case b.maxTools > 0 && b.toolCalls >= b.maxTools:
		exhausted = &toolBudgetError{kind: toolBudgetTotal, tool: name, limit: b.maxTools}
	case b.maxPerTool > 0 && b.perTool[name] >= b.maxPerTool:
		exhausted = &toolBudgetError{kind: toolBudgetPerTool, tool: name, limit: b.maxPerTool}
	case write && b.repeated[repeatedKey] >= 1:
		// A repeated read can be useful for polling or verification, but the same
		// canonical write must never execute twice in one turn—even when the host
		// disables the configurable read repetition limit.
		exhausted = &toolBudgetError{kind: toolBudgetIdentical, tool: name, limit: 1}
	case !write && b.maxRepeated > 0 && b.repeated[repeatedKey] >= b.maxRepeated:
		exhausted = &toolBudgetError{kind: toolBudgetIdentical, tool: name, limit: b.maxRepeated}
	}
	if exhausted != nil {
		b.exhausted = exhausted
		b.toolMu.Unlock()
		b.emitBudget("error", exhausted.Error())
		return exhausted
	}
	b.toolCalls++
	b.perTool[name]++
	b.repeated[repeatedKey]++
	n := b.toolCalls
	b.toolMu.Unlock()

	b.emitBudget("running", fmt.Sprintf("工具调用 %d：%s", n, name))
	return nil
}

func (b *turnBudget) emitBudget(status, summary string) {
	if b == nil || b.emit == nil {
		return
	}
	_ = b.emit(ihtml.ChatStreamEvent{
		Type:       "assistant_budget",
		ModelReady: true,
		Status:     summary,
		Agent: &ihtml.ChatAgentStage{
			Mode: b.mode, Agent: "root", Phase: "budget", Status: status, Summary: summary,
		},
		Budget: b.snapshot(),
	})
}

func (b *turnBudget) takeModelCall(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b == nil {
		return nil
	}
	if !b.deadline.IsZero() && time.Now().After(b.deadline) {
		b.emitBudget("error", "本轮总时间预算已用完")
		return context.DeadlineExceeded
	}
	n := b.modelCalls.Add(1)
	if b.maxModels > 0 && n > b.maxModels {
		b.emitBudget("error", errModelCallBudget.Error())
		return errModelCallBudget
	}
	b.emitBudget("running", fmt.Sprintf("模型调用 %d", n))
	return nil
}

func reserveBounded(counter *atomic.Int64, amount, limit int64) bool {
	if counter == nil || amount < 0 || limit <= 0 || amount > limit {
		return false
	}
	for {
		current := counter.Load()
		if current < 0 || current > limit-amount {
			return false
		}
		if counter.CompareAndSwap(current, current+amount) {
			return true
		}
	}
}

func (b *turnBudget) takeModelOutputEvent() error {
	if b == nil {
		return nil
	}
	if !reserveBounded(&b.outputEvents, 1, b.maxOutputEvents) {
		return &ModelOutputLimitError{Scope: ModelOutputLimitTurnEvents, Limit: b.maxOutputEvents}
	}
	return nil
}

func (b *turnBudget) takeModelOutputBytes(callBytes *atomic.Int64, amount int64) error {
	if b == nil || amount == 0 {
		return nil
	}
	if amount < 0 || !reserveBounded(callBytes, amount, b.maxOutputBytesPerCall) {
		return &ModelOutputLimitError{Scope: ModelOutputLimitCallBytes, Limit: b.maxOutputBytesPerCall}
	}
	if !reserveBounded(&b.outputBytes, amount, b.maxOutputBytesPerTurn) {
		return &ModelOutputLimitError{Scope: ModelOutputLimitTurnBytes, Limit: b.maxOutputBytesPerTurn}
	}
	return nil
}

func (b *turnBudget) takeModelOutput(message *schema.Message, callBytes *atomic.Int64) error {
	if b == nil || message == nil {
		return nil
	}
	if err := b.takeModelOutputEvent(); err != nil {
		return err
	}
	callRemaining := b.maxOutputBytesPerCall - callBytes.Load()
	if callRemaining <= 0 {
		return &ModelOutputLimitError{Scope: ModelOutputLimitCallBytes, Limit: b.maxOutputBytesPerCall}
	}
	turnRemaining := b.maxOutputBytesPerTurn - b.outputBytes.Load()
	if turnRemaining <= 0 {
		return &ModelOutputLimitError{Scope: ModelOutputLimitTurnBytes, Limit: b.maxOutputBytesPerTurn}
	}
	estimateLimit := callRemaining
	limitScope, limitValue := ModelOutputLimitCallBytes, b.maxOutputBytesPerCall
	if turnRemaining < estimateLimit {
		estimateLimit = turnRemaining
		limitScope, limitValue = ModelOutputLimitTurnBytes, b.maxOutputBytesPerTurn
	}
	amount, err := modelMessageOutputBytes(message, estimateLimit)
	if err != nil {
		if errors.Is(err, errModelOutputEstimateExceeded) {
			return &ModelOutputLimitError{Scope: limitScope, Limit: limitValue}
		}
		// Arbitrary SDK model implementations can attach values that encoding/json
		// cannot safely encode (including cycles). Treat those as rejected model
		// output instead of attempting a best-effort undercount.
		return &ModelOutputLimitError{Scope: ModelOutputLimitCallBytes, Limit: b.maxOutputBytesPerCall}
	}
	return b.takeModelOutputBytes(callBytes, amount)
}

func (b *turnBudget) recordUsage(usage *ihtml.ChatUsage) {
	if b == nil || usage == nil {
		return
	}
	b.usageMu.Lock()
	addChatUsage(&b.usage, usage)
	b.hasUsage = true
	b.usageMu.Unlock()
}

func (b *turnBudget) usageSnapshot() *ihtml.ChatUsage {
	if b == nil {
		return nil
	}
	b.usageMu.Lock()
	defer b.usageMu.Unlock()
	if !b.hasUsage {
		return nil
	}
	out := b.usage
	return &out
}

func (b *turnBudget) startSubtask(agent, phase, summary string) (string, error) {
	if b == nil {
		return "", nil
	}
	n := b.subtasks.Add(1)
	id := fmt.Sprintf("subtask_%d", b.taskSeq.Add(1))
	if b.maxSubtasks > 0 && n > b.maxSubtasks {
		b.emitAgent(agent, id, phase, "error", errSubtaskBudget.Error())
		return "", errSubtaskBudget
	}
	b.emitAgent(agent, id, phase, "running", summary)
	return id, nil
}

func (b *turnBudget) finishSubtask(agent, id, phase, status, summary string) {
	if b == nil {
		return
	}
	b.emitAgent(agent, id, phase, status, summary)
}

func (b *turnBudget) emitAgent(agent, taskID, phase, status, summary string) {
	if b == nil || b.emit == nil {
		return
	}
	_ = b.emit(ihtml.ChatStreamEvent{
		Type:       "assistant_agent",
		ModelReady: true,
		Agent: &ihtml.ChatAgentStage{
			Mode: b.mode, Agent: agent, TaskID: taskID, ParentTaskID: "root",
			Phase: phase, Status: status, Summary: summary,
		},
		Budget: b.snapshot(),
	})
}

// budgetedChatModel 保留 ToolCallingChatModel 的不可变 WithTools 语义；所有
// 派生模型仍引用同一 turnBudget，因此并行子 Agent 无法各自重置额度。
type budgetedChatModel struct {
	base   einoModel.ToolCallingChatModel
	budget *turnBudget
}

func withTurnBudget(base einoModel.ToolCallingChatModel, budget *turnBudget) einoModel.ToolCallingChatModel {
	return &budgetedChatModel{base: base, budget: budget}
}

func (m *budgetedChatModel) WithTools(tools []*schema.ToolInfo) (einoModel.ToolCallingChatModel, error) {
	next, err := m.base.WithTools(tools)
	if err != nil {
		return nil, err
	}
	return &budgetedChatModel{base: next, budget: m.budget}, nil
}

func (m *budgetedChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...einoModel.Option) (*schema.Message, error) {
	if err := m.budget.takeModelCall(ctx); err != nil {
		return nil, err
	}
	message, err := m.base.Generate(ctx, input, opts...)
	var callBytes atomic.Int64
	if limitErr := m.budget.takeModelOutput(message, &callBytes); limitErr != nil {
		return nil, limitErr
	}
	if message != nil && message.ResponseMeta != nil {
		m.budget.recordUsage(normalizedUsage(message.ResponseMeta.Usage))
	}
	return message, err
}

func (m *budgetedChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einoModel.Option) (*schema.StreamReader[*schema.Message], error) {
	if err := m.budget.takeModelCall(ctx); err != nil {
		return nil, err
	}
	stream, err := m.base.Stream(ctx, input, opts...)
	if err != nil || stream == nil {
		return stream, err
	}
	reader, writer := schema.Pipe[*schema.Message](1)
	go func() {
		defer stream.Close()
		defer writer.Close()
		var observed ihtml.ChatUsage
		var callBytes atomic.Int64
		for {
			message, recvErr := stream.Recv()
			if errors.Is(recvErr, io.EOF) {
				return
			}
			if recvErr != nil {
				writer.Send(nil, recvErr)
				return
			}
			if limitErr := m.budget.takeModelOutput(message, &callBytes); limitErr != nil {
				writer.Send(nil, limitErr)
				return
			}
			if message != nil && message.ResponseMeta != nil {
				if usage := normalizedUsage(message.ResponseMeta.Usage); usage != nil {
					delta := ihtml.ChatUsage{
						InputTokens:       max(0, usage.InputTokens-observed.InputTokens),
						OutputTokens:      max(0, usage.OutputTokens-observed.OutputTokens),
						TotalTokens:       max(0, usage.TotalTokens-observed.TotalTokens),
						CachedInputTokens: max(0, usage.CachedInputTokens-observed.CachedInputTokens),
						ReasoningTokens:   max(0, usage.ReasoningTokens-observed.ReasoningTokens),
					}
					observed.InputTokens = max(observed.InputTokens, usage.InputTokens)
					observed.OutputTokens = max(observed.OutputTokens, usage.OutputTokens)
					observed.TotalTokens = max(observed.TotalTokens, usage.TotalTokens)
					observed.CachedInputTokens = max(observed.CachedInputTokens, usage.CachedInputTokens)
					observed.ReasoningTokens = max(observed.ReasoningTokens, usage.ReasoningTokens)
					m.budget.recordUsage(&delta)
				}
			}
			if writer.Send(message, nil) {
				return
			}
		}
	}()
	return reader, nil
}
