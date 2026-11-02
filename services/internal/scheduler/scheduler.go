// Package scheduler picks monitors that are due and publishes check.requested.
//
// Several replicas can run at once: FOR UPDATE SKIP LOCKED makes each one
// take a different set of rows, and a row stays locked until next_check_at
// is moved forward in the same transaction.
package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"pulse/internal/amqpx"
	"pulse/internal/message"
)

var (
	ticksTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "pulse_scheduler_ticks_total", Help: "Scheduler ticks."})
	publishedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "pulse_scheduler_published_total", Help: "check.requested messages confirmed by the broker."})
	publishErrorsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "pulse_scheduler_publish_errors_total", Help: "Failed check.requested publications."})
	tickDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name: "pulse_scheduler_tick_duration_seconds", Help: "Duration of one scheduler tick.", Buckets: prometheus.DefBuckets})
	dueMonitors = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "pulse_scheduler_due_monitors", Help: "Monitors due for a check at the start of the last tick (all replicas)."})
)

var tracer = otel.Tracer("pulse/scheduler")

// Publisher is implemented by *amqpx.Publisher.
type Publisher interface {
	Publish(ctx context.Context, exchange, key string, body []byte) error
}

// Scheduler runs the tick loop.
type Scheduler struct {
	DB    *pgxpool.Pool
	Pub   Publisher
	Tick  time.Duration
	Batch int
	Log   *slog.Logger
}

const (
	countDueSQL = `SELECT count(*) FROM monitors WHERE NOT paused AND next_check_at <= now()`

	selectDueSQL = `
SELECT id, url, method, timeout_ms, expected_status
FROM monitors
WHERE NOT paused AND next_check_at <= now()
ORDER BY next_check_at
LIMIT $1
FOR UPDATE SKIP LOCKED`

	rescheduleSQL = `
UPDATE monitors SET next_check_at = now() + interval_seconds * interval '1 second'
WHERE id = ANY($1)`
)

type dueMonitor struct {
	ID             int64
	URL            string
	Method         string
	TimeoutMS      int
	ExpectedStatus int
}

// Run ticks until ctx is cancelled. A tick in progress is not interrupted:
// it publishes and commits, then the loop exits.
func (s *Scheduler) Run(ctx context.Context) {
	s.Log.Info("scheduler started", "tick", s.Tick.String(), "batch", s.Batch)
	ticker := time.NewTicker(s.Tick)
	defer ticker.Stop()
	for {
		s.tick(context.WithoutCancel(ctx))
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Scheduler) tick(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	start := time.Now()
	ticksTotal.Inc()
	defer func() { tickDuration.Observe(time.Since(start).Seconds()) }()

	n, err := s.dispatch(ctx)
	if err != nil {
		s.Log.Error("scheduler tick failed", "published", n, "error", err.Error())
		return
	}
	if n > 0 {
		s.Log.Debug("checks published", "count", n)
	}
}

// dispatch returns how many checks were published and committed.
func (s *Scheduler) dispatch(ctx context.Context) (int, error) {
	var due int
	if err := s.DB.QueryRow(ctx, countDueSQL).Scan(&due); err != nil {
		return 0, fmt.Errorf("count due monitors: %w", err)
	}
	dueMonitors.Set(float64(due))
	if due == 0 {
		return 0, nil
	}

	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) // no-op after Commit

	rows, err := tx.Query(ctx, selectDueSQL, s.Batch)
	if err != nil {
		return 0, fmt.Errorf("select due monitors: %w", err)
	}
	monitors, err := pgx.CollectRows(rows, pgx.RowToStructByPos[dueMonitor])
	if err != nil {
		return 0, fmt.Errorf("read due monitors: %w", err)
	}

	// Publish one by one, each waits for its confirm. On the first failure
	// stop: the failed monitor and the rest keep their next_check_at and
	// are picked up on the next tick. Those already confirmed are committed,
	// so they are not published twice.
	published := make([]int64, 0, len(monitors))
	var pubErr error
	for _, m := range monitors {
		if err := s.publish(ctx, m); err != nil {
			publishErrorsTotal.Inc()
			pubErr = fmt.Errorf("publish check for monitor %d: %w", m.ID, err)
			break
		}
		published = append(published, m.ID)
	}
	if len(published) == 0 {
		return 0, pubErr // rollback via defer
	}

	if _, err := tx.Exec(ctx, rescheduleSQL, published); err != nil {
		return 0, fmt.Errorf("reschedule monitors: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		// Messages are already in the queue: these monitors get checked twice. Harmless.
		return 0, fmt.Errorf("commit: %w", err)
	}
	publishedTotal.Add(float64(len(published)))
	return len(published), pubErr
}

func (s *Scheduler) publish(ctx context.Context, m dueMonitor) error {
	ctx, span := tracer.Start(ctx, "publish "+amqpx.KeyCheckRequested,
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(attribute.Int64("pulse.monitor_id", m.ID)))
	defer span.End()

	body, err := json.Marshal(NewCheckRequested(m.ID, m.URL, m.Method, m.TimeoutMS, m.ExpectedStatus, time.Now()))
	if err != nil {
		return err
	}
	if err := s.Pub.Publish(ctx, amqpx.ExchangeChecks, amqpx.KeyCheckRequested, body); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	return nil
}

// NewCheckRequested builds the message for one monitor.
func NewCheckRequested(id int64, url, method string, timeoutMS, expected int, now time.Time) message.CheckRequested {
	return message.CheckRequested{
		RequestID:      message.NewRequestID(),
		MonitorID:      id,
		URL:            url,
		Method:         method,
		TimeoutMS:      timeoutMS,
		ExpectedStatus: expected,
		ScheduledAt:    message.Timestamp(now),
	}
}
