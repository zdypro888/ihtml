package einoengine

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	ai "github.com/zdypro888/ihtml/agent"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestModelStreamTelemetryPreservesEOFAndCancellation(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(previous)
	defer provider.Shutdown(context.Background())
	r, err := ai.NewTaskRuntime(t.Context(), ai.TaskConfig{Scope: "scope"})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx, err := r.BindContext(t.Context(), "scope")
	if err != nil {
		t.Fatal(err)
	}
	inner := &scriptedModel{state: &scriptedModelState{fn: func([]*schema.Message, []*schema.ToolInfo) (*schema.Message, error) {
		m := schema.AssistantMessage("private-output", nil)
		m.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 7, CompletionTokens: 3}}
		return m, nil
	}}}
	m := &goalBudgetModel{inner: inner, runtime: r, scope: "scope"}
	stream, err := m.Stream(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	message, err := stream.Recv()
	if err != nil || message.Content != "private-output" {
		t.Fatal(message, err)
	}
	if _, err = stream.Recv(); err != io.EOF {
		t.Fatal("EOF became nil chunk", err)
	}
	stream.Close()
	cancelCtx, cancel := context.WithCancel(ctx)
	stream, err = m.Stream(cancelCtx, nil)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	stream.Close()
	deadline := time.Now().Add(time.Second)
	for len(recorder.Ended()) < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(recorder.Ended()) != 2 {
		t.Fatal("unterminated stream span", len(recorder.Ended()))
	}
	if _, err = m.Stream(cancelCtx, nil); err == nil || !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
