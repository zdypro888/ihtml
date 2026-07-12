package chat

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	einoModel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/zdypro888/ihtml"
)

var (
	errModelCallBudget = errors.New("本轮模型调用预算已用完")
	errSubtaskBudget   = errors.New("本轮子任务预算已用完")
)

// turnBudget 被一轮的根 Agent、所有只读子 Agent 和它们派生出的 WithTools
// 模型共享，防止一次 deep 请求绕过按 turn 计数的外层配额。
type turnBudget struct {
	mode        string
	maxModels   int64
	maxSubtasks int64
	deadline    time.Time
	emit        func(ihtml.ChatStreamEvent) error

	modelCalls atomic.Int64
	subtasks   atomic.Int64
	taskSeq    atomic.Int64
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
	duration := cfg.MaxTurnDuration
	if duration < 0 {
		duration = 0
	}
	if cfg.RequestTimeout > 0 && (duration == 0 || cfg.RequestTimeout < duration) {
		duration = cfg.RequestTimeout
	}
	b := &turnBudget{mode: mode, maxModels: maxModels, maxSubtasks: maxSubtasks, emit: emit}
	if duration > 0 {
		b.deadline = time.Now().Add(duration)
	}
	return b
}

func (b *turnBudget) snapshot() *ihtml.ChatTurnBudget {
	if b == nil {
		return nil
	}
	out := &ihtml.ChatTurnBudget{
		ModelCalls:    b.modelCalls.Load(),
		MaxModelCalls: b.maxModels,
		Subtasks:      b.subtasks.Load(),
		MaxSubtasks:   b.maxSubtasks,
	}
	if !b.deadline.IsZero() {
		out.DeadlineUnixMS = b.deadline.UnixMilli()
	}
	return out
}

func (b *turnBudget) emitBudget(status, summary string) {
	if b == nil || b.emit == nil {
		return
	}
	_ = b.emit(ihtml.ChatStreamEvent{
		Type:       "assistant_budget",
		ModelReady: true,
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
	return m.base.Generate(ctx, input, opts...)
}

func (m *budgetedChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einoModel.Option) (*schema.StreamReader[*schema.Message], error) {
	if err := m.budget.takeModelCall(ctx); err != nil {
		return nil, err
	}
	return m.base.Stream(ctx, input, opts...)
}
