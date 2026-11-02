// Package message defines the JSON messages from CONTRACT.md ("RabbitMQ", "Kafka").
package message

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Monitor statuses.
const (
	StatusUnknown = "unknown"
	StatusUp      = "up"
	StatusDown    = "down"
)

// CheckRequested is published by scheduler to pulse.checks / check.requested.
type CheckRequested struct {
	RequestID      string    `json:"request_id"`
	MonitorID      int64     `json:"monitor_id"`
	URL            string    `json:"url"`
	Method         string    `json:"method"`
	TimeoutMS      int       `json:"timeout_ms"`
	ExpectedStatus int       `json:"expected_status"`
	ScheduledAt    time.Time `json:"scheduled_at"`
}

// StatusChanged is published by checker to pulse.events / status.changed.
type StatusChanged struct {
	MonitorID   int64     `json:"monitor_id"`
	MonitorName string    `json:"monitor_name"`
	URL         string    `json:"url"`
	From        string    `json:"from"`
	To          string    `json:"to"`
	At          time.Time `json:"at"`
	Error       string    `json:"error,omitempty"`
	IncidentID  *int64    `json:"incident_id,omitempty"`
}

// CheckResult is the Kafka message check.result (CONTRACT.md, "Kafka"):
// written by checker to the outbox, relayed to pulse.check-results with
// key = monitor_id, consumed by aggregator.
type CheckResult struct {
	CheckID    int64     `json:"check_id"`
	MonitorID  int64     `json:"monitor_id"`
	CheckedAt  time.Time `json:"checked_at"`
	OK         bool      `json:"ok"`
	StatusCode *int      `json:"status_code"` // null when there was no HTTP response
	LatencyMS  int       `json:"latency_ms"`
	Error      string    `json:"error,omitempty"`
}

// Key is the Kafka record key: all results of a monitor go to one partition,
// so they are consumed in order.
func (m CheckResult) Key() string { return fmt.Sprint(m.MonitorID) }

// Hour is the uptime_hourly bucket: date_trunc('hour', checked_at) in UTC.
func (m CheckResult) Hour() time.Time { return m.CheckedAt.UTC().Truncate(time.Hour) }

// ParseCheckResult decodes and validates a check.result body.
func ParseCheckResult(body []byte) (CheckResult, error) {
	var m CheckResult
	if err := json.Unmarshal(body, &m); err != nil {
		return m, fmt.Errorf("decode check.result: %w", err)
	}
	var errs []error
	if m.CheckID <= 0 {
		errs = append(errs, errors.New("check_id must be positive"))
	}
	if m.MonitorID <= 0 {
		errs = append(errs, errors.New("monitor_id must be positive"))
	}
	if m.CheckedAt.IsZero() {
		errs = append(errs, errors.New("checked_at is missing"))
	}
	if m.LatencyMS < 0 {
		errs = append(errs, errors.New("latency_ms is negative"))
	}
	if err := errors.Join(errs...); err != nil {
		return m, fmt.Errorf("invalid check.result: %w", err)
	}
	return m, nil
}

// ParseCheckRequested decodes and validates a check.requested body.
func ParseCheckRequested(body []byte) (CheckRequested, error) {
	var m CheckRequested
	if err := json.Unmarshal(body, &m); err != nil {
		return m, fmt.Errorf("decode check.requested: %w", err)
	}
	var errs []error
	if m.MonitorID <= 0 {
		errs = append(errs, errors.New("monitor_id must be positive"))
	}
	if m.URL == "" {
		errs = append(errs, errors.New("url is empty"))
	}
	if m.Method != "GET" && m.Method != "HEAD" {
		errs = append(errs, fmt.Errorf("method %q is not GET or HEAD", m.Method))
	}
	if m.TimeoutMS <= 0 {
		errs = append(errs, errors.New("timeout_ms must be positive"))
	}
	if m.ExpectedStatus < 100 || m.ExpectedStatus > 599 {
		errs = append(errs, fmt.Errorf("expected_status %d is out of 100..599", m.ExpectedStatus))
	}
	if err := errors.Join(errs...); err != nil {
		return m, fmt.Errorf("invalid check.requested: %w", err)
	}
	return m, nil
}

// ParseStatusChanged decodes and validates a status.changed body.
func ParseStatusChanged(body []byte) (StatusChanged, error) {
	var m StatusChanged
	if err := json.Unmarshal(body, &m); err != nil {
		return m, fmt.Errorf("decode status.changed: %w", err)
	}
	if m.MonitorID <= 0 {
		return m, errors.New("invalid status.changed: monitor_id must be positive")
	}
	if !validStatus(m.From) || !validStatus(m.To) {
		return m, fmt.Errorf("invalid status.changed: from=%q to=%q", m.From, m.To)
	}
	return m, nil
}

func validStatus(s string) bool {
	return s == StatusUnknown || s == StatusUp || s == StatusDown
}

// Timestamp formats times in messages as in the contract: UTC, whole seconds.
func Timestamp(t time.Time) time.Time { return t.UTC().Truncate(time.Second) }

// NewRequestID returns a random UUID v4.
func NewRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // never fails since Go 1.24
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
