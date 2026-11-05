package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestRuntimeUsesStandardSpansWithoutPayload(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(previous)
	defer provider.Shutdown(context.Background())
	ctx, parent := RuntimeSpan(t.Context(), "host")
	r := taskRuntimeFixture(t, TaskConfig{Scope: "scope"})
	tool := Tool{Name: "arbitrary", Effect: ToolEffectRead, Handler: func(context.Context, json.RawMessage) (string, error) { return "private-output-marker", nil }}
	row, err := r.Start(ctx, "scope", "turn", "invocation", tool, json.RawMessage(`{"private":"private-input-marker"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.AwaitResult(t.Context(), "scope", row.ID, map[string]bool{tool.Name: true}); err != nil {
		t.Fatal(err)
	}
	r.Close()
	parent.End()
	deadline := time.Now().Add(time.Second)
	for len(recorder.Ended()) < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	found := false
	for _, span := range recorder.Ended() {
		if span.Name() == "agent.tool" {
			found = true
			if span.Parent().TraceID() != parent.SpanContext().TraceID() {
				t.Fatal("lost standard parent trace")
			}
		}
		for _, a := range span.Attributes() {
			if strings.Contains(a.Value.Emit(), "private-") {
				t.Fatal("payload in trace")
			}
		}
		for _, e := range span.Events() {
			for _, a := range e.Attributes {
				if strings.Contains(a.Value.Emit(), "private-") {
					t.Fatal("payload in event")
				}
			}
		}
	}
	if !found {
		t.Fatal("no ended tool span")
	}
}
