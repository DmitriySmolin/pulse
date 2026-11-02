package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func TestJSONFormat(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, "checker", slog.LevelInfo)

	traceID, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	spanID, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	ctx := trace.ContextWithSpanContext(context.Background(),
		trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled}))

	log.Debug("hidden")
	log.With("monitor_id", 1).InfoContext(ctx, "check recorded")

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("expected exactly one JSON line, got %q: %v", buf.String(), err)
	}
	want := map[string]any{
		"level":      "info",
		"msg":        "check recorded",
		"service":    "checker",
		"monitor_id": float64(1),
		"trace_id":   "4bf92f3577b34da6a3ce929d0e0e4736",
	}
	for k, v := range want {
		if line[k] != v {
			t.Errorf("%s = %v, want %v", k, line[k], v)
		}
	}
	if _, ok := line["time"]; !ok {
		t.Error("time field is missing")
	}
}

func TestParseLevel(t *testing.T) {
	if l, err := ParseLevel("WARN"); err != nil || l != slog.LevelWarn {
		t.Errorf("ParseLevel(WARN) = %v, %v", l, err)
	}
	if _, err := ParseLevel("verbose"); err == nil {
		t.Error("expected error for unknown level")
	}
}
