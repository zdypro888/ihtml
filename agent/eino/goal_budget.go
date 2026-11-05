package einoengine

import (
	"context"
	"io"
	"sync"
	"time"

	model "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	ai "github.com/zdypro888/ihtml/agent"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
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
	scope   string
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
func (m *goalBudgetModel) observe(ctx context.Context, started time.Time, message *schema.Message, err error) {
	row := ai.TaskObservation{Kind: "model_finished", State: "completed", TraceID: ai.TaskTraceID(ctx), DurationMS: time.Since(started).Milliseconds()}
	if err != nil {
		row.State = "failed"
		row.Code = ai.TaskFailureFrom(err, "handler_returned", true, "", nil).Code
	}
	if message != nil && message.ResponseMeta != nil && message.ResponseMeta.Usage != nil {
		row.InputTokens = int64(message.ResponseMeta.Usage.PromptTokens)
		row.OutputTokens = int64(message.ResponseMeta.Usage.CompletionTokens)
	}
	_ = m.runtime.RecordObservation(ctx, m.scope, row)
}
func (m *goalBudgetModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	if err := m.admit(ctx, input, opts); err != nil {
		return nil, err
	}
	ctx, span := ai.RuntimeSpan(ctx, "agent.model", attribute.String("model.name", m.request.Model))
	defer span.End()
	started := time.Now()
	message, err := m.inner.Generate(ctx, input, opts...)
	if err != nil {
		span.SetStatus(codes.Error, "model_call_failed")
	}
	m.observe(ctx, started, message, err)
	return message, err
}
func (m *goalBudgetModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if err := m.admit(ctx, input, opts); err != nil {
		return nil, err
	}
	ctx, span := ai.RuntimeSpan(ctx, "agent.model", attribute.String("model.name", m.request.Model))
	started := time.Now()
	reader, err := m.inner.Stream(ctx, input, opts...)
	if err != nil {
		span.SetStatus(codes.Error, "model_call_failed")
		span.End()
		m.observe(ctx, started, nil, err)
		return nil, err
	}
	var once sync.Once
	var mu sync.Mutex
	var stopMu sync.Mutex
	var stopObserve func() bool
	var usage *schema.Message
	finish := func(err error) {
		once.Do(func() {
			stopMu.Lock()
			if stopObserve != nil {
				stopObserve()
			}
			stopMu.Unlock()
			mu.Lock()
			value := usage
			mu.Unlock()
			if err != nil {
				span.SetStatus(codes.Error, "model_stream_incomplete")
			}
			m.observe(ctx, started, value, err)
			span.End()
		})
	}
	stopMu.Lock()
	stopObserve = context.AfterFunc(ctx, func() { finish(ctx.Err()) })
	stopMu.Unlock()
	return schema.StreamReaderWithConvert(reader, func(value *schema.Message) (*schema.Message, error) {
		if value != nil && value.ResponseMeta != nil && value.ResponseMeta.Usage != nil {
			mu.Lock()
			usage = value
			mu.Unlock()
		}
		return value, nil
	}, schema.WithOnEOF(func() (any, error) { finish(nil); return nil, io.EOF }), schema.WithErrWrapper(func(err error) error { finish(err); return err })), nil
}
