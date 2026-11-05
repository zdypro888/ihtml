package agent

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

var runtimeMeter = otel.Meter("github.com/zdypro888/ihtml/agent")
var observationCount, _ = runtimeMeter.Int64Counter("ihtml.agent.events")
var observationDuration, _ = runtimeMeter.Float64Histogram("ihtml.agent.duration", metric.WithUnit("ms"))
var modelTokens, _ = runtimeMeter.Int64Counter("ihtml.agent.model.tokens", metric.WithUnit("{token}"))

func captureWaitTrace(ctx context.Context, row *TaskWaitRecord) {
	row.TraceParent, row.TraceState = captureTrace(ctx)
}
func captureTrace(ctx context.Context) (string, string) {
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	return carrier["traceparent"], carrier["tracestate"]
}
func restoreWaitTrace(ctx context.Context, row TaskWaitRecord) context.Context {
	return restoreTrace(ctx, row.TraceParent, row.TraceState)
}
func restoreTrace(ctx context.Context, parent, state string) context.Context {
	return propagation.TraceContext{}.Extract(ctx, propagation.MapCarrier{"traceparent": parent, "tracestate": state})
}

// RuntimeSpan 接入宿主标准 OTel provider；未配置 provider 时是 no-op，不建立私有 exporter 或日志系统。
func RuntimeSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return otel.Tracer("github.com/zdypro888/ihtml/agent").Start(ctx, name, trace.WithAttributes(attrs...))
}
func observeTelemetry(ctx context.Context, row TaskObservation) {
	attrs := []attribute.KeyValue{attribute.String("event.kind", row.Kind), attribute.String("execution.state", row.State), attribute.String("failure.code", row.Code)}
	if row.Tool != "" {
		attrs = append(attrs, attribute.String("tool.name", row.Tool))
	}
	observationCount.Add(ctx, 1, metric.WithAttributes(attrs...))
	if row.InputTokens > 0 {
		modelTokens.Add(ctx, row.InputTokens, metric.WithAttributes(attribute.String("direction", "input")))
	}
	if row.OutputTokens > 0 {
		modelTokens.Add(ctx, row.OutputTokens, metric.WithAttributes(attribute.String("direction", "output")))
	}
	if row.DurationMS > 0 {
		observationDuration.Record(ctx, float64(row.DurationMS), metric.WithAttributes(attrs...))
	}
	trace.SpanFromContext(ctx).AddEvent(row.Kind, trace.WithAttributes(attrs...))
}
