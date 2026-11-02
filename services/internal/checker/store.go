package checker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"pulse/internal/kafkax"
	"pulse/internal/message"
	"pulse/internal/outbox"
)

var errMonitorGone = errors.New("monitor not found")

type recorded struct {
	CheckID int64
	Event   *message.StatusChanged // set when the status changed
}

// record stores a check result in one transaction (CONTRACT.md, steps 3–5):
// the check, the monitor state, incidents and, with Kafka enabled, the
// check.result event in the outbox — all or nothing.
// The monitor row is locked with FOR UPDATE, so two checks of the same
// monitor (e.g. a redelivered message) are applied one after another.
func (s *Service) record(ctx context.Context, req message.CheckRequested, res Result) (recorded, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return recorded{}, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) // no-op after Commit

	var name, url, status string
	var failures int
	err = tx.QueryRow(ctx,
		`SELECT name, url, status, consecutive_failures FROM monitors WHERE id = $1 FOR UPDATE`,
		req.MonitorID).Scan(&name, &url, &status, &failures)
	if errors.Is(err, pgx.ErrNoRows) {
		return recorded{}, errMonitorGone
	}
	if err != nil {
		return recorded{}, fmt.Errorf("lock monitor: %w", err)
	}

	out := Decide(status, failures, res.OK, s.Threshold)

	var rec recorded
	err = tx.QueryRow(ctx, `
INSERT INTO checks (monitor_id, checked_at, ok, status_code, latency_ms, error)
VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		req.MonitorID, res.CheckedAt, res.OK, nullIfZero(res.StatusCode),
		int(res.Latency.Milliseconds()), nullIfZero(res.Err)).Scan(&rec.CheckID)
	if err != nil {
		return recorded{}, fmt.Errorf("insert check: %w", err)
	}

	if s.ResultsTopic != "" {
		ev := NewCheckResult(rec.CheckID, req.MonitorID, res)
		err := outbox.Add(ctx, tx, outbox.Message{
			Topic: s.ResultsTopic, Key: ev.Key(), Payload: ev, Headers: kafkax.TraceHeaders(ctx),
		})
		if err != nil {
			return recorded{}, err
		}
	}

	_, err = tx.Exec(ctx, `
UPDATE monitors SET last_checked_at = $2, consecutive_failures = $3, status = $4 WHERE id = $1`,
		req.MonitorID, res.CheckedAt, out.Failures, out.Status)
	if err != nil {
		return recorded{}, fmt.Errorf("update monitor: %w", err)
	}

	var incidentID *int64
	switch out.Incident {
	case IncidentOpen:
		id, err := openIncident(ctx, tx, req.MonitorID, res.CheckedAt, res.Err)
		if err != nil {
			return recorded{}, err
		}
		incidentID = &id
	case IncidentResolve:
		var id int64
		err := tx.QueryRow(ctx, `
UPDATE incidents SET resolved_at = $2 WHERE monitor_id = $1 AND resolved_at IS NULL RETURNING id`,
			req.MonitorID, res.CheckedAt).Scan(&id)
		switch {
		case err == nil:
			incidentID = &id
		case !errors.Is(err, pgx.ErrNoRows): // no open incident (e.g. closed by hand) is fine
			return recorded{}, fmt.Errorf("resolve incident: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return recorded{}, fmt.Errorf("commit: %w", err)
	}

	if out.Changed {
		rec.Event = &message.StatusChanged{
			MonitorID: req.MonitorID, MonitorName: name, URL: url,
			From: status, To: out.Status, At: message.Timestamp(res.CheckedAt),
			Error: res.Err, IncidentID: incidentID,
		}
	}
	return rec, nil
}

// openIncident relies on the unique partial index incidents_one_open_idx:
// if an open incident already exists, its id is reused.
func openIncident(ctx context.Context, tx pgx.Tx, monitorID int64, at time.Time, cause string) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, `
INSERT INTO incidents (monitor_id, started_at, cause) VALUES ($1, $2, $3)
ON CONFLICT (monitor_id) WHERE resolved_at IS NULL DO NOTHING
RETURNING id`, monitorID, at, cause).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx,
			`SELECT id FROM incidents WHERE monitor_id = $1 AND resolved_at IS NULL`, monitorID).Scan(&id)
	}
	if err != nil {
		return 0, fmt.Errorf("open incident: %w", err)
	}
	return id, nil
}

func (s *Service) setSnapshotKey(ctx context.Context, checkID int64, key string) error {
	_, err := s.DB.Exec(ctx, `UPDATE checks SET snapshot_key = $2 WHERE id = $1`, checkID, key)
	return err
}

// NewCheckResult builds the check.result Kafka message.
func NewCheckResult(checkID, monitorID int64, res Result) message.CheckResult {
	return message.CheckResult{
		CheckID:    checkID,
		MonitorID:  monitorID,
		CheckedAt:  message.Timestamp(res.CheckedAt),
		OK:         res.OK,
		StatusCode: nullIfZero(res.StatusCode),
		LatencyMS:  int(res.Latency.Milliseconds()),
		Error:      res.Err,
	}
}

// nullIfZero maps 0 / "" to SQL NULL.
func nullIfZero[T comparable](v T) *T {
	var zero T
	if v == zero {
		return nil
	}
	return &v
}
