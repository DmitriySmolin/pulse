package kafkax

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/twmb/franz-go/pkg/kgo"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"pulse/internal/config"
)

func load(t *testing.T, vars map[string]string) (Config, error) {
	t.Helper()
	env := config.FromMap(vars)
	c := Load(env, "checker")
	return c, env.Err()
}

func TestLoadDisabledByDefault(t *testing.T) {
	c, err := load(t, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if c.Enabled() || c.Topic != "pulse.check-results" || c.Group != "pulse-aggregator" ||
		c.ClientID != "checker" || !c.AutoCreateTopic || c.TLS {
		t.Errorf("defaults = %+v", c)
	}
}

func TestLoadBrokersAndSASL(t *testing.T) {
	pw := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(pw, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := load(t, map[string]string{
		"KAFKA_BROKERS":           " kafka-0:9092, kafka-1:9092 ,,",
		"KAFKA_SASL_MECHANISM":    "scram-sha-512",
		"KAFKA_USERNAME":          "pulse",
		"KAFKA_PASSWORD_FILE":     pw,
		"KAFKA_AUTO_CREATE_TOPIC": "false",
		"KAFKA_CLIENT_ID":         "checker-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.Brokers, []string{"kafka-0:9092", "kafka-1:9092"}) {
		t.Errorf("brokers = %q", c.Brokers)
	}
	if c.SASLMechanism != SASLScramSHA512 || c.Password != "s3cret" || c.AutoCreateTopic || c.ClientID != "checker-1" {
		t.Errorf("config = %+v", c)
	}
	opts, err := c.ClientOpts()
	if err != nil || len(opts) != 3 { // seeds, client id, sasl
		t.Errorf("opts = %d, %v", len(opts), err)
	}
}

func TestLoadInvalid(t *testing.T) {
	cases := map[string]struct {
		vars map[string]string
		want string
	}{
		"unknown mechanism": {map[string]string{"KAFKA_BROKERS": "k:9092", "KAFKA_SASL_MECHANISM": "PLAIN",
			"KAFKA_USERNAME": "u", "KAFKA_PASSWORD": "p"}, "not supported"},
		"scram without password": {map[string]string{"KAFKA_BROKERS": "k:9092", "KAFKA_SASL_MECHANISM": "SCRAM-SHA-512",
			"KAFKA_USERNAME": "u"}, "needs KAFKA_USERNAME and KAFKA_PASSWORD"},
		"password without mechanism": {map[string]string{"KAFKA_BROKERS": "k:9092", "KAFKA_PASSWORD": "p"},
			"KAFKA_SASL_MECHANISM is empty"},
		"scheme in broker": {map[string]string{"KAFKA_BROKERS": "kafka://k:9092"}, "without a scheme"},
		"ca without tls":   {map[string]string{"KAFKA_BROKERS": "k:9092", "KAFKA_CA_FILE": "/ca.pem"}, "KAFKA_TLS"},
		"both password forms": {map[string]string{"KAFKA_BROKERS": "k:9092", "KAFKA_SASL_MECHANISM": "SCRAM-SHA-512",
			"KAFKA_USERNAME": "u", "KAFKA_PASSWORD": "p", "KAFKA_PASSWORD_FILE": "/pw"}, "both set"},
	}
	for name, tc := range cases {
		_, err := load(t, tc.vars)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to contain %q", name, err, tc.want)
		}
	}
}

func TestClientOptsTLS(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.pem")
	_ = os.WriteFile(bad, []byte("not a certificate"), 0o600)

	c := Config{Brokers: []string{"k:9093"}, ClientID: "x", TLS: true}
	if opts, err := c.ClientOpts(); err != nil || len(opts) != 3 { // seeds, client id, tls
		t.Errorf("system roots: %d opts, %v", len(opts), err)
	}
	c.CAFile = bad
	if _, err := c.ClientOpts(); err == nil || !strings.Contains(err.Error(), "no PEM certificates") {
		t.Errorf("bad CA: %v", err)
	}
	c.CAFile = filepath.Join(dir, "missing.pem")
	if _, err := c.ClientOpts(); err == nil {
		t.Error("missing CA: expected error")
	}
}

func TestTopicSettings(t *testing.T) {
	cases := []struct {
		brokers int
		rf      int16
		minISR  int
	}{{0, 1, 1}, {1, 1, 1}, {2, 2, 1}, {3, 3, 2}, {5, 3, 2}}
	for _, tc := range cases {
		rf, isr := TopicSettings(tc.brokers)
		if rf != tc.rf || isr != tc.minISR {
			t.Errorf("TopicSettings(%d) = %d, %d; want %d, %d", tc.brokers, rf, isr, tc.rf, tc.minISR)
		}
	}
	cfg := TopicConfigs(2)
	if *cfg["retention.ms"] != "604800000" || *cfg["min.insync.replicas"] != "2" {
		t.Errorf("configs: retention=%s min.isr=%s", *cfg["retention.ms"], *cfg["min.insync.replicas"])
	}
}

func TestTraceHeadersRoundTrip(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	tid, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	sid, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: tid, SpanID: sid, TraceFlags: trace.FlagsSampled}))

	h := TraceHeaders(ctx)
	if h["traceparent"] != "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01" {
		t.Fatalf("headers = %v", h)
	}
	rec := &kgo.Record{Headers: RecordHeaders(h)}
	got := trace.SpanContextFromContext(ExtractContext(context.Background(), rec))
	if got.TraceID() != tid || got.SpanID() != sid || !got.IsRemote() {
		t.Errorf("extracted %v", got)
	}
	if len(TraceHeaders(context.Background())) != 0 {
		t.Error("no span: expected no headers")
	}
}
