// Package aggregator consumes check.result from Kafka and maintains hourly
// uptime in PostgreSQL (CONTRACT.md, "Kafka").
//
// Delivery is at-least-once: the outbox relay and consumer group rebalances
// both produce duplicates. The consumer is idempotent instead of "exactly once":
//
//	poll ─► BEGIN
//	        for each record: INSERT aggregated_checks(check_id) ON CONFLICT DO NOTHING
//	                         inserted? upsert uptime_hourly : duplicate
//	        COMMIT ─► commit offsets ─► AllowRebalance ─► poll ...
//
// The offset is committed only after the database commit. A crash between the
// two re-delivers the batch, and aggregated_checks turns it into no-ops.
// Auto-commit is off: it would commit offsets of records that were polled but
// not yet saved, and a crash would lose them.
package aggregator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/twmb/franz-go/pkg/kgo"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"pulse/internal/kafkax"
	"pulse/internal/message"
)

var (
	consumedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "pulse_aggregator_consumed_total", Help: "check.result records processed and committed to PostgreSQL (duplicates included)."})
	duplicatesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "pulse_aggregator_duplicates_total", Help: "check.result records skipped because check_id was already aggregated."})
	processingErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "pulse_aggregator_processing_errors_total", Help: "Processing errors by stage."}, []string{"stage"})
)

func init() {
	for _, s := range []string{"decode", "db", "commit"} {
		processingErrorsTotal.WithLabelValues(s)
	}
}

var tracer = otel.Tracer("pulse/aggregator")

const maxRetryDelay = 30 * time.Second

// Aggregator is the consumer loop. Client must be created with
// ClientOpts() + the options from GroupOpts.
type Aggregator struct {
	DB      *pgxpool.Pool
	Client  *kgo.Client
	MaxPoll int // records per batch (one DB transaction)
	Log     *slog.Logger

	pending offsets // processed (saved in PostgreSQL) but not yet committed to Kafka
}

// GroupOpts are the consumer group options. The callbacks are bound to a,
// so create the Aggregator first, then the client, then set a.Client.
func (a *Aggregator) GroupOpts(group, topic string) []kgo.Opt {
	return []kgo.Opt{
		kgo.ConsumerGroup(group),
		kgo.ConsumeTopics(topic),
		kgo.DisableAutoCommit(),
		// Rebalances wait until the polled batch is saved and committed
		// (AllowRebalance), so we never commit for partitions we no longer own.
		// Trade-off: a batch must be processed faster than the rebalance
		// timeout (60s by default), hence the bounded MaxPoll.
		kgo.BlockRebalanceOnPoll(),
		kgo.OnPartitionsRevoked(a.onRevoked),
		kgo.OnPartitionsLost(a.onLost),
		kgo.OnPartitionsAssigned(func(_ context.Context, _ *kgo.Client, assigned map[string][]int32) {
			a.Log.Info("partitions assigned", "partitions", assigned)
		}),
		// A new group reads the topic from the beginning (7 days of retention).
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	}
}

// Run polls until ctx is cancelled. The batch in progress is finished.
func (a *Aggregator) Run(ctx context.Context) {
	for {
		fetches := a.Client.PollRecords(ctx, a.MaxPoll)
		if fetches.IsClientClosed() {
			return
		}
		fetches.EachError(func(topic string, partition int32, err error) {
			if errors.Is(err, context.Canceled) {
				return
			}
			a.Log.Warn("kafka fetch error", "topic", topic, "partition", partition, "error", err.Error())
		})
		if records := fetches.Records(); len(records) > 0 {
			if a.processWithRetry(ctx, records) {
				a.commitPending(context.WithoutCancel(ctx), nil)
			}
		}
		a.Client.AllowRebalance()
		if ctx.Err() != nil {
			return
		}
	}
}

// Close commits what is left and leaves the group.
func (a *Aggregator) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a.commitPending(ctx, nil)
	a.Client.CloseAllowingRebalance() // leaving the group triggers onRevoked
}

// processWithRetry retries a failed batch (e.g. PostgreSQL is down) until it
// succeeds or shutdown starts. On shutdown the batch is abandoned without a
// commit: Kafka re-delivers it to whoever gets the partition.
func (a *Aggregator) processWithRetry(ctx context.Context, records []*kgo.Record) bool {
	delay := time.Second
	for attempt := 1; ; attempt++ {
		res, err := a.process(context.WithoutCancel(ctx), records)
		if err == nil {
			consumedTotal.Add(float64(len(records)))
			duplicatesTotal.Add(float64(res.duplicates))
			a.pending.add(records)
			a.Log.Debug("batch aggregated", "records", len(records), "duplicates", res.duplicates, "invalid", res.invalid)
			return true
		}
		processingErrorsTotal.WithLabelValues("db").Inc()
		a.Log.Error("batch not aggregated, retrying", "records", len(records), "attempt", attempt,
			"retry_in", delay.String(), "error", err.Error())
		select {
		case <-ctx.Done():
			a.Log.Warn("shutdown during retry: batch left uncommitted, it will be re-delivered")
			return false
		case <-time.After(delay):
		}
		delay = min(delay*2, maxRetryDelay)
	}
}

type batchResult struct{ duplicates, invalid int }

// process saves one batch in one transaction.
func (a *Aggregator) process(ctx context.Context, records []*kgo.Record) (batchResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var res batchResult

	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return res, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) // no-op after Commit

	for _, r := range records {
		m, err := message.ParseCheckResult(r.Value)
		if err != nil {
			// A poison message is never going to succeed: count, log and skip it,
			// otherwise it would block the partition forever.
			res.invalid++
			processingErrorsTotal.WithLabelValues("decode").Inc()
			a.Log.Error("invalid check.result skipped", "partition", r.Partition, "offset", r.Offset, "error", err.Error())
			continue
		}
		dup, err := a.apply(ctx, tx, r, m)
		if err != nil {
			return res, fmt.Errorf("check_id %d (partition %d, offset %d): %w", m.CheckID, r.Partition, r.Offset, err)
		}
		if dup {
			res.duplicates++
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return res, fmt.Errorf("commit: %w", err)
	}
	return res, nil
}

const (
	markAggregatedSQL = `INSERT INTO aggregated_checks (check_id) VALUES ($1) ON CONFLICT DO NOTHING`

	// The monitor may have been deleted after the check: then there is nothing
	// to aggregate (and the foreign key would fail), so the row is skipped.
	upsertHourSQL = `
INSERT INTO uptime_hourly AS u (monitor_id, hour, checks, ok, latency_sum_ms, latency_max_ms)
SELECT $1::bigint, $2::timestamptz, 1, $3::int, $4::int, $4::int
WHERE EXISTS (SELECT 1 FROM monitors WHERE id = $1::bigint)
ON CONFLICT (monitor_id, hour) DO UPDATE SET
  checks         = u.checks + 1,
  ok             = u.ok + EXCLUDED.ok,
  latency_sum_ms = u.latency_sum_ms + EXCLUDED.latency_sum_ms,
  latency_max_ms = GREATEST(u.latency_max_ms, EXCLUDED.latency_max_ms)`
)

// Delta is what one check adds to its uptime_hourly row.
type Delta struct {
	MonitorID int64
	Hour      time.Time
	OK        int // 1 or 0
	LatencyMS int
}

// DeltaOf converts a check.result into its contribution.
func DeltaOf(m message.CheckResult) Delta {
	d := Delta{MonitorID: m.MonitorID, Hour: m.Hour(), LatencyMS: m.LatencyMS}
	if m.OK {
		d.OK = 1
	}
	return d
}

// apply returns dup=true if the check was aggregated before.
func (a *Aggregator) apply(ctx context.Context, tx pgx.Tx, r *kgo.Record, m message.CheckResult) (dup bool, err error) {
	ctx, span := tracer.Start(kafkax.ExtractContext(ctx, r), "pulse.check-results process",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(attribute.Int64("pulse.check_id", m.CheckID), attribute.Int64("pulse.monitor_id", m.MonitorID),
			attribute.Int("messaging.kafka.partition", int(r.Partition)), attribute.Int64("messaging.kafka.offset", r.Offset)))
	defer span.End()

	tag, err := tx.Exec(ctx, markAggregatedSQL, m.CheckID)
	if err != nil {
		return false, fmt.Errorf("mark aggregated: %w", err)
	}
	if tag.RowsAffected() == 0 {
		span.SetAttributes(attribute.Bool("pulse.duplicate", true))
		return true, nil
	}
	d := DeltaOf(m)
	if _, err := tx.Exec(ctx, upsertHourSQL, d.MonitorID, d.Hour, d.OK, d.LatencyMS); err != nil {
		return false, fmt.Errorf("upsert uptime_hourly: %w", err)
	}
	return false, nil
}
