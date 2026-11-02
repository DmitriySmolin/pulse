// Package telemetry configures OpenTelemetry tracing.
//
// The W3C trace context propagator is always installed, so an incoming
// traceparent is passed on even when this service does not export spans.
// Export (OTLP over HTTP) is enabled only when OTEL_EXPORTER_OTLP_ENDPOINT is set.
package telemetry

import (
	"context"
	"fmt"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Setup returns a shutdown function that flushes pending spans.
func Setup(ctx context.Context, endpoint, serviceName string) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))

	if endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}

	// OTEL_EXPORTER_OTLP_ENDPOINT is the base URL of the collector;
	// by the OTel spec traces go to <base>/v1/traces.
	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpointURL(strings.TrimRight(endpoint, "/")+"/v1/traces"))
	if err != nil {
		return nil, fmt.Errorf("otlp exporter: %w", err)
	}
	res, err := resource.Merge(resource.Default(),
		resource.NewSchemaless(attribute.String("service.name", serviceName)))
	if err != nil {
		return nil, fmt.Errorf("otel resource: %w", err)
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(res))
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}
