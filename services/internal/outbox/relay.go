package outbox

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
	"github.com/twmb/franz-go/pkg/kgo"

	"pulse/internal/kafkax"
)

var (
	pending = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "pulse_checker_outbox_pending", Help: "Rows in the outbox waiting to be published to Kafka."})
	publishedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "pulse_checker_outbox_published_total", Help: "Outbox records confirmed by Kafka (acks=all) and deleted."})
	publishErrorsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "pulse_checker_outbox_publish_errors_total", Help: "Outbox records that failed to publish (they stay in the outbox)."})
)

// maxBackoff caps the pause between attempts while Kafka is unavailable.
const maxBackoff = 30 * time.Second

// Producer is implemented by *kgo.Client.
type Producer interface {
	ProduceSync(ctx context.Context, rs ...*kgo.Record) kgo.ProduceResults
}

// Relay moves outbox rows to Kafka.
type Relay struct {
	DB       *pgxpool.Pool
	Producer Producer
	Interval time.Duration // OUTBOX_POLL_INTERVAL
	Batch    int           // OUTBOX_BATCH
	// Timeout bounds one publish (should match kgo.RecordDeliveryTimeout).
	// The selected rows stay locked for at most that long.
	Timeout time.Duration
	// EnsureTopic runs before the first publish; nil when the topic is
	// managed outside (KAFKA_AUTO_CREATE_TOPIC=false).
	EnsureTopic func(ctx context.Context) error
	Log         *slog.Logger
}

// Run works until ctx is cancelled. A batch in progress is finished
// (published and committed, or rolled back) before it returns.
func (r *Relay) Run(ctx context.Context) {
	r.Log.Info("outbox relay started", "interval", r.Interval.String(), "batch", r.Batch)
	topicReady := r.EnsureTopic == nil
	wait := time.Duration(0)
	failing := false
	for {
		select {
		case <-ctx.Done():
			r.Log.Info("outbox relay stopped")
			return
		case <-time.After(wait):
		}
		work := context.WithoutCancel(ctx)

		if !topicReady {
			tctx, cancel := context.WithTimeout(work, r.Timeout)
			err := r.EnsureTopic(tctx)
			cancel()
			if err != nil {
				wait = nextBackoff(wait, r.Interval)
				r.Log.Warn("kafka topic is not ready, outbox keeps growing", "retry_in", wait.String(), "error", err.Error())
				r.refreshPending(work)
				continue
			}
			topicReady = true
		}

		n, err := r.relayOnce(work)
		r.refreshPending(work)
		switch {
		case err != nil:
			wait = nextBackoff(wait, r.Interval)
			failing = true
			r.Log.Warn("outbox publish failed, rows stay in the outbox", "published", n,
				"retry_in", wait.String(), "error", err.Error())
		case n == r.Batch:
			wait = 0 // a full batch: there is probably more, do not sleep
		default:
			if failing {
				r.Log.Info("outbox publishing recovered", "published", n)
				failing = false
			}
			wait = r.Interval
		}
	}
}

func nextBackoff(cur, base time.Duration) time.Duration {
	return min(max(cur*2, base), maxBackoff)
}

type row struct {
	ID      int64
	Topic   string
	Key     string
	Payload string
	Headers string
}

// relayOnce publishes one batch and returns how many rows were published.
//
// The rows are locked with FOR UPDATE SKIP LOCKED for the whole publish:
// another checker replica skips them and takes the next ones, so two
// replicas never publish the same row. New inserts are not blocked.
func (r *Relay) relayOnce(ctx context.Context) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, r.Timeout+10*time.Second)
	defer cancel()

	tx, err := r.DB.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) // no-op after Commit

	rows, err := tx.Query(ctx, `
SELECT id, topic, msg_key, payload::text, headers::text
FROM outbox ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED`, r.Batch)
	if err != nil {
		return 0, fmt.Errorf("select outbox: %w", err)
	}
	batch, err := pgx.CollectRows(rows, pgx.RowToStructByPos[row])
	if err != nil {
		return 0, fmt.Errorf("read outbox: %w", err)
	}
	if len(batch) == 0 {
		return 0, nil
	}

	records, err := buildRecords(batch)
	if err != nil {
		return 0, err
	}
	pctx, pcancel := context.WithTimeout(ctx, r.Timeout)
	results := r.Producer.ProduceSync(pctx, records...)
	pcancel()

	// Delete only what Kafka confirmed. The rest stays for the next attempt.
	// franz-go fails all later records of a partition after a failed one, so
	// per-monitor order is kept.
	done, failed, firstErr := splitResults(batch, records, results)
	publishErrorsTotal.Add(float64(failed))
	if len(done) > 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM outbox WHERE id = ANY($1)`, done); err != nil {
			return 0, fmt.Errorf("delete published rows: %w (they will be published again)", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return 0, fmt.Errorf("commit: %w (published rows will be published again)", err)
		}
		publishedTotal.Add(float64(len(done)))
	}
	if firstErr != nil {
		return len(done), fmt.Errorf("%d of %d records not published: %w", failed, len(batch), firstErr)
	}
	return len(done), nil
}

func buildRecords(batch []row) ([]*kgo.Record, error) {
	records := make([]*kgo.Record, len(batch))
	for i, m := range batch {
		var headers map[string]string
		if err := json.Unmarshal([]byte(m.Headers), &headers); err != nil {
			return nil, fmt.Errorf("outbox row %d: headers: %w", m.ID, err)
		}
		records[i] = &kgo.Record{
			Topic:   m.Topic,
			Key:     []byte(m.Key),
			Value:   []byte(m.Payload),
			Headers: kafkax.RecordHeaders(headers),
		}
	}
	return records, nil
}

// splitResults returns ids of confirmed rows, the number of failed ones and
// the first error. records[i] was built from batch[i]; results refer to the
// same *kgo.Record pointers, in any order.
func splitResults(batch []row, records []*kgo.Record, results kgo.ProduceResults) (done []int64, failed int, firstErr error) {
	ids := make(map[*kgo.Record]int64, len(records))
	for i, rec := range records {
		ids[rec] = batch[i].ID
	}
	for _, res := range results {
		id, ok := ids[res.Record]
		if !ok {
			continue
		}
		if res.Err != nil {
			failed++
			if firstErr == nil {
				firstErr = res.Err
			}
			continue
		}
		done = append(done, id)
	}
	if missing := len(batch) - len(done) - failed; missing > 0 {
		failed += missing // should not happen: no result means not confirmed
	}
	return done, failed, firstErr
}

func (r *Relay) refreshPending(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var n int64
	if err := r.DB.QueryRow(ctx, `SELECT count(*) FROM outbox`).Scan(&n); err != nil {
		r.Log.Debug("outbox count failed", "error", err.Error())
		return
	}
	pending.Set(float64(n))
}
