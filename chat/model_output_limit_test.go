package chat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
	"testing"

	einoModel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type outputLimitModel struct {
	generate *schema.Message
	stream   func() *schema.StreamReader[*schema.Message]
}

func (m *outputLimitModel) WithTools(_ []*schema.ToolInfo) (einoModel.ToolCallingChatModel, error) {
	return m, nil
}

func (m *outputLimitModel) Generate(context.Context, []*schema.Message, ...einoModel.Option) (*schema.Message, error) {
	return m.generate, nil
}

func (m *outputLimitModel) Stream(context.Context, []*schema.Message, ...einoModel.Option) (*schema.StreamReader[*schema.Message], error) {
	if m.stream == nil {
		return schema.StreamReaderFromArray([]*schema.Message{m.generate}), nil
	}
	return m.stream(), nil
}

func requireOutputLimit(t *testing.T, err error, scope string, limit int64) {
	t.Helper()
	if !errors.Is(err, ErrModelOutputLimit) {
		t.Fatalf("error = %v, want ErrModelOutputLimit", err)
	}
	var typed *ModelOutputLimitError
	if !errors.As(err, &typed) || typed.Scope != scope || typed.Limit != limit {
		t.Fatalf("typed error = %#v, want scope=%q limit=%d", typed, scope, limit)
	}
}

func TestBudgetedModelRejectsOversizedGenerateContentAndToolArguments(t *testing.T) {
	const limit = int64(64 << 10)
	large := strings.Repeat("x", int(limit)+1)
	base64Data := large
	for name, message := range map[string]*schema.Message{
		"content":      schema.AssistantMessage(large, nil),
		"message name": {Name: large},
		"tool call id": {ToolCallID: large},
		"tool name":    {ToolName: large},
		"finish reason": {
			ResponseMeta: &schema.ResponseMeta{FinishReason: large},
		},
		"legacy multimodal URL": {
			MultiContent: []schema.ChatMessagePart{{
				Type: schema.ChatMessagePartTypeImageURL,
				ImageURL: &schema.ChatMessageImageURL{
					URL: large,
				},
			}},
		},
		"input multimodal bytes": {
			UserInputMultiContent: []schema.MessageInputPart{{
				Type: schema.ChatMessagePartTypeFileURL,
				File: &schema.MessageInputFile{MessagePartCommon: schema.MessagePartCommon{
					Base64Data: &base64Data,
				}},
			}},
		},
		"output multimodal bytes": {
			AssistantGenMultiContent: []schema.MessageOutputPart{{
				Type: schema.ChatMessagePartTypeImageURL,
				Image: &schema.MessageOutputImage{MessagePartCommon: schema.MessagePartCommon{
					Base64Data: &base64Data,
				}},
			}},
		},
		"message extra": {Extra: map[string]any{"payload": large}},
		"output part extra": {
			AssistantGenMultiContent: []schema.MessageOutputPart{{
				Extra: map[string]any{"payload": large},
			}},
		},
		"tool arguments": schema.AssistantMessage("", []schema.ToolCall{{
			ID: "call", Type: "function",
			Function: schema.FunctionCall{Name: "lookup", Arguments: strings.Repeat("x", int(limit))},
		}}),
		"tool extra": schema.AssistantMessage("", []schema.ToolCall{{
			Extra: map[string]any{"payload": large},
		}}),
	} {
		t.Run(name, func(t *testing.T) {
			budget := newTurnBudget(Config{
				MaxModelCallsPerTurn: 4, MaxTurnDuration: -1,
				MaxModelOutputBytesPerCall: limit, MaxModelOutputBytesPerTurn: 4 * limit,
			}, AgentModeFast, nil)
			_, err := withTurnBudget(&outputLimitModel{generate: message}, budget).Generate(context.Background(), nil)
			requireOutputLimit(t, err, ModelOutputLimitCallBytes, limit)
		})
	}
}

func TestBudgetedModelFailsClosedForUnsafeExtraValues(t *testing.T) {
	const limit = int64(64 << 10)
	cycle := map[string]any{}
	cycle["self"] = cycle
	for name, extra := range map[string]map[string]any{
		"cycle":          cycle,
		"function":       {"callback": func() {}},
		"invalid float":  {"value": math.NaN()},
		"invalid number": {"value": json.Number("+1")},
		"invalid JSON":   {"value": json.RawMessage("{")},
	} {
		t.Run(name, func(t *testing.T) {
			budget := newTurnBudget(Config{
				MaxModelCallsPerTurn: 1, MaxTurnDuration: -1,
				MaxModelOutputBytesPerCall: limit, MaxModelOutputBytesPerTurn: limit,
			}, AgentModeFast, nil)
			_, err := withTurnBudget(&outputLimitModel{generate: &schema.Message{Extra: extra}}, budget).Generate(context.Background(), nil)
			requireOutputLimit(t, err, ModelOutputLimitCallBytes, limit)
		})
	}
}

func TestBudgetedModelAcceptsBoundedJSONExtraWithoutMarshallingWholeMessage(t *testing.T) {
	const limit = int64(64 << 10)
	budget := newTurnBudget(Config{
		MaxModelCallsPerTurn: 1, MaxTurnDuration: -1,
		MaxModelOutputBytesPerCall: limit, MaxModelOutputBytesPerTurn: limit,
	}, AgentModeFast, nil)
	want := &schema.Message{
		Content: "ok",
		Extra: map[string]any{
			"nested": []any{true, nil, json.Number("12.5"), map[string]any{"bytes": []byte("abc")}},
		},
	}
	got, err := withTurnBudget(&outputLimitModel{generate: want}, budget).Generate(context.Background(), nil)
	if err != nil || got != want {
		t.Fatalf("bounded Extra output = %#v, %v", got, err)
	}
}

func TestBudgetedModelSharesOutputBytesAcrossCallsAndDerivedModels(t *testing.T) {
	const limit = int64(64 << 10)
	budget := newTurnBudget(Config{
		MaxModelCallsPerTurn: 4, MaxTurnDuration: -1,
		MaxModelOutputBytesPerCall: limit, MaxModelOutputBytesPerTurn: limit,
	}, AgentModeDeep, nil)
	model := withTurnBudget(&outputLimitModel{generate: schema.AssistantMessage(strings.Repeat("x", 40<<10), nil)}, budget)
	derived, err := model.WithTools(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := model.Generate(context.Background(), nil); err != nil {
		t.Fatalf("first call: %v", err)
	}
	_, err = derived.Generate(context.Background(), nil)
	requireOutputLimit(t, err, ModelOutputLimitTurnBytes, limit)
}

func TestBudgetedModelLimitsStreamEventsAndClosesProviderStream(t *testing.T) {
	var writer *schema.StreamWriter[*schema.Message]
	base := &outputLimitModel{stream: func() *schema.StreamReader[*schema.Message] {
		reader, streamWriter := schema.Pipe[*schema.Message](32)
		writer = streamWriter
		for range minMaxModelOutputEventsPerTurn + 1 {
			if writer.Send(schema.AssistantMessage("x", nil), nil) {
				panic("stream closed before test consumer started")
			}
		}
		return reader
	}}
	budget := newTurnBudget(Config{
		MaxModelCallsPerTurn: 4, MaxTurnDuration: -1,
		MaxModelOutputBytesPerCall:  minMaxModelOutputBytesPerCall,
		MaxModelOutputBytesPerTurn:  minMaxModelOutputBytesPerTurn,
		MaxModelOutputEventsPerTurn: minMaxModelOutputEventsPerTurn,
	}, AgentModeFast, nil)
	stream, err := withTurnBudget(base, budget).Stream(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	for index := 0; index < minMaxModelOutputEventsPerTurn; index++ {
		if message, err := stream.Recv(); err != nil || message == nil {
			t.Fatalf("chunk %d = %#v, %v", index, message, err)
		}
	}
	_, err = stream.Recv()
	requireOutputLimit(t, err, ModelOutputLimitTurnEvents, minMaxModelOutputEventsPerTurn)
	if writer == nil || !writer.Send(schema.AssistantMessage("late", nil), nil) {
		t.Fatal("underlying provider stream remained open after output-limit failure")
	}
}

func TestBudgetedModelAllowsExactOutputByteLimit(t *testing.T) {
	const limit = int64(64 << 10)
	budget := newTurnBudget(Config{
		MaxModelCallsPerTurn: 1, MaxTurnDuration: -1,
		MaxModelOutputBytesPerCall: limit, MaxModelOutputBytesPerTurn: limit,
	}, AgentModeFast, nil)
	want := strings.Repeat("x", int(limit))
	message, err := withTurnBudget(&outputLimitModel{generate: schema.AssistantMessage(want, nil)}, budget).Generate(context.Background(), nil)
	if err != nil || message == nil || message.Content != want {
		t.Fatalf("exact-limit output = %#v, %v", message, err)
	}
	if got := budget.outputBytes.Load(); got != limit {
		t.Fatalf("recorded output bytes = %d", got)
	}
}

func TestBudgetedStreamReturnsEOFNormallyWithinLimits(t *testing.T) {
	budget := newTurnBudget(Config{MaxModelCallsPerTurn: 1, MaxTurnDuration: -1}, AgentModeFast, nil)
	stream, err := withTurnBudget(&outputLimitModel{stream: func() *schema.StreamReader[*schema.Message] {
		return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("ok", nil)})
	}}, budget).Stream(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if _, err := stream.Recv(); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("stream EOF = %v", err)
	}
}
