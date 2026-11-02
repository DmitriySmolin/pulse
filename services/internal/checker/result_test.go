package checker

import (
	"testing"
	"time"
)

func TestNewCheckResult(t *testing.T) {
	at := time.Date(2026, 10, 12, 10, 0, 5, 700_000_000, time.UTC)
	m := NewCheckResult(123, 1, Result{CheckedAt: at, StatusCode: 503, Latency: 41*time.Millisecond + 900*time.Microsecond,
		Err: "unexpected status 503, expected 200"})
	if m.CheckID != 123 || m.MonitorID != 1 || m.OK || *m.StatusCode != 503 || m.LatencyMS != 41 ||
		!m.CheckedAt.Equal(at.Truncate(time.Second)) || m.Error == "" {
		t.Errorf("check.result = %+v", m)
	}

	timeout := NewCheckResult(124, 1, Result{CheckedAt: at, Latency: 5 * time.Second, Err: "timeout after 5000ms"})
	if timeout.StatusCode != nil {
		t.Errorf("no response must give status_code null, got %d", *timeout.StatusCode)
	}

	ok := NewCheckResult(125, 1, Result{CheckedAt: at, OK: true, StatusCode: 200})
	if !ok.OK || ok.Error != "" {
		t.Errorf("ok check = %+v", ok)
	}
}
