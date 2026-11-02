package kafkax

import (
	"context"

	"github.com/twmb/franz-go/pkg/kgo"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// TraceHeaders returns the W3C trace context of ctx (traceparent, tracestate)
// as a map: the checker stores it in outbox.headers, the relay turns it into
// Kafka record headers.
func TraceHeaders(ctx context.Context) map[string]string {
	h := map[string]string{}
	otel.GetTextMapPropagator().Inject(ctx, propagation.MapCarrier(h))
	return h
}

// RecordHeaders converts a header map to Kafka record headers.
func RecordHeaders(h map[string]string) []kgo.RecordHeader {
	out := make([]kgo.RecordHeader, 0, len(h))
	for k, v := range h {
		out = append(out, kgo.RecordHeader{Key: k, Value: []byte(v)})
	}
	return out
}

// ExtractContext continues the trace whose traceparent came in the record headers.
func ExtractContext(ctx context.Context, r *kgo.Record) context.Context {
	h := make(propagation.MapCarrier, len(r.Headers))
	for _, kv := range r.Headers {
		h[kv.Key] = string(kv.Value)
	}
	return otel.GetTextMapPropagator().Extract(ctx, h)
}
