package amqpx

import (
	"context"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

func TestTraceparentRoundTrip(t *testing.T) {
	prop := propagation.TraceContext{}
	traceID, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	spanID, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	ctx := trace.ContextWithSpanContext(context.Background(),
		trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled}))

	headers := amqp.Table{}
	prop.Inject(ctx, HeaderCarrier(headers))
	if got := headers["traceparent"]; got != "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01" {
		t.Fatalf("traceparent header = %v", got)
	}

	got := trace.SpanContextFromContext(prop.Extract(context.Background(), HeaderCarrier(headers)))
	if got.TraceID() != traceID || !got.IsRemote() {
		t.Fatalf("extracted span context = %+v", got)
	}

	// A message without headers must not panic.
	prop.Extract(context.Background(), HeaderCarrier(nil))
}
