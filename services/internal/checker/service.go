// Package checker executes check.requested messages: HTTP probe, result in
// PostgreSQL, incidents, status.changed event and an optional S3 snapshot.
package checker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"

	"pulse/internal/amqpx"
	"pulse/internal/message"
)

var (
	checksTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "pulse_checker_checks_total", Help: "Executed checks by result."}, []string{"result"})
	targetDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name: "pulse_checker_target_duration_seconds", Help: "Response time of checked targets.",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30}})
	processingErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "pulse_checker_processing_errors_total", Help: "Processing errors by stage."}, []string{"stage"})
	inflight = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "pulse_checker_inflight", Help: "Messages being processed right now."})
	statusChanges = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "pulse_checker_status_changes_total", Help: "Monitor status changes by new status."}, []string{"to"})
)

func init() {
	// Export zero values so dashboards and alerts see the series from the start.
	for _, r := range []string{"ok", "fail"} {
		checksTotal.WithLabelValues(r)
	}
	for _, s := range []string{"decode", "db", "publish", "snapshot"} {
		processingErrors.WithLabelValues(s)
	}
	for _, s := range []string{message.StatusUp, message.StatusDown} {
		statusChanges.WithLabelValues(s)
	}
}

var tracer = otel.Tracer("pulse/checker")

// Publisher is implemented by *amqpx.Publisher.
type Publisher interface {
	Publish(ctx context.Context, exchange, key string, body []byte) error
}

// Service is the message handler.
type Service struct {
	DB        *pgxpool.Pool
	Pub       Publisher
	Prober    *Prober
	Snapshots *Snapshots // nil: snapshots are disabled
	Threshold int        // CHECKER_FAILURE_THRESHOLD
	// ResultsTopic is the Kafka topic for check.result; empty when Kafka is
	// disabled (no KAFKA_BROKERS), then nothing is written to the outbox.
	ResultsTopic string
	Log          *slog.Logger
}

// Handle is an amqpx.Handler. Returning nil means ack, so it returns nil
// only after the check is committed.
func (s *Service) Handle(ctx context.Context, d amqp.Delivery) error {
	inflight.Inc()
	defer inflight.Dec()

	req, err := message.ParseCheckRequested(d.Body)
	if err != nil {
		processingErrors.WithLabelValues("decode").Inc()
		return fmt.Errorf("%w: %v", amqpx.ErrReject, err)
	}
	log := s.Log.With("monitor_id", req.MonitorID, "request_id", req.RequestID)

	res := s.Prober.Probe(ctx, req)
	targetDuration.Observe(res.Latency.Seconds())
	if res.OK {
		checksTotal.WithLabelValues("ok").Inc()
	} else {
		checksTotal.WithLabelValues("fail").Inc()
	}

	rec, err := s.record(ctx, req, res)
	if errors.Is(err, errMonitorGone) {
		log.InfoContext(ctx, "monitor was deleted, check skipped")
		return nil
	}
	if err != nil {
		processingErrors.WithLabelValues("db").Inc()
		return fmt.Errorf("record check: %w", err)
	}
	// Failed checks are logged at info: they are what an operator searches for
	// (by monitor_id / trace_id). Successful ones would only add volume.
	level := slog.LevelDebug
	if !res.OK {
		level = slog.LevelInfo
	}
	log.Log(ctx, level, "check recorded", "check_id", rec.CheckID, "ok", res.OK,
		"status_code", res.StatusCode, "latency_ms", res.Latency.Milliseconds(), "error", res.Err)

	if ev := rec.Event; ev != nil {
		statusChanges.WithLabelValues(ev.To).Inc()
		log.InfoContext(ctx, "monitor status changed", "from", ev.From, "to", ev.To,
			"incident_id", ev.IncidentID, "error", ev.Error)
		s.publishEvent(ctx, log, *ev)
	}
	if !res.OK && s.Snapshots != nil {
		s.saveSnapshot(ctx, log, req, rec.CheckID, res)
	}
	return nil
}

// publishEvent runs after the commit. If RabbitMQ is unavailable for all
// attempts, the event is lost (the check itself is saved). check.result for
// Kafka goes through the transactional outbox and cannot be lost this way;
// status.changed could use the same pattern, at the cost of one more relay.
func (s *Service) publishEvent(ctx context.Context, log *slog.Logger, ev message.StatusChanged) {
	body, err := json.Marshal(ev)
	if err != nil {
		log.ErrorContext(ctx, "marshal status.changed", "error", err.Error())
		return
	}
	for attempt := 1; attempt <= 3; attempt++ {
		err = s.Pub.Publish(ctx, amqpx.ExchangeEvents, amqpx.KeyStatusChanged, body)
		if err == nil {
			return
		}
		if attempt < 3 {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
	}
	processingErrors.WithLabelValues("publish").Inc()
	log.ErrorContext(ctx, "status.changed was not published, notification lost", "error", err.Error())
}

// saveSnapshot failures are only a warning: the check is already saved.
func (s *Service) saveSnapshot(ctx context.Context, log *slog.Logger, req message.CheckRequested, checkID int64, res Result) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	key := SnapshotKey(req.MonitorID, checkID)
	err := s.Snapshots.Put(ctx, key, FormatSnapshot(req, checkID, res))
	if err == nil {
		err = s.setSnapshotKey(ctx, checkID, key)
	}
	if err != nil {
		processingErrors.WithLabelValues("snapshot").Inc()
		log.WarnContext(ctx, "snapshot not saved", "check_id", checkID, "key", key, "error", err.Error())
	}
}
