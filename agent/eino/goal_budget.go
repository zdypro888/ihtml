package einoengine

import (
	"context"

	model "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	ai "github.com/zdypro888/ihtml/agent"
)

// ModelBudgetRequest 保留 Eino 原始消息、工具及选项，让宿主复用供应商 tokenizer/报价。
// QuoteModel 必须覆盖输出上限、缓存、隐藏输入以及客户端重试的最大费用；估算不能声明 Quoted。
type ModelBudgetRequest struct {
	Model           string
	Messages        []*schema.Message
	Tools           []*schema.ToolInfo
	Options         []model.Option
	MaxOutputTokens int
}
type goalBudgetModel struct {
	inner   model.ToolCallingChatModel
	runtime *ai.TaskRuntime
	quote   func(context.Context, ModelBudgetRequest) (ai.TaskGoalCharge, error)
	request ModelBudgetRequest
}

func (m *goalBudgetModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	inner, err := m.inner.WithTools(tools)
	if err != nil {
		return nil, err
	}
	copy := *m
	copy.inner = inner
	copy.request.Tools = append([]*schema.ToolInfo(nil), tools...)
	return &copy, nil
}
func (m *goalBudgetModel) admit(ctx context.Context, input []*schema.Message, opts []model.Option) error {
	amount := ai.TaskGoalCharge{ModelCalls: 1}
	if m.quote != nil {
		request := m.request
		request.Messages = input
		request.Options = opts
		var err error
		amount, err = m.quote(ctx, request)
		if err != nil {
			return err
		}
		amount.ModelCalls = max(amount.ModelCalls, 1)
	}
	return m.runtime.ChargeGoal(ctx, amount)
}
func (m *goalBudgetModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	if err := m.admit(ctx, input, opts); err != nil {
		return nil, err
	}
	return m.inner.Generate(ctx, input, opts...)
}
func (m *goalBudgetModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if err := m.admit(ctx, input, opts); err != nil {
		return nil, err
	}
	return m.inner.Stream(ctx, input, opts...)
}
