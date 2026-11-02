package scheduler

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"pulse/internal/message"
)

func TestNewCheckRequestedIsValidForChecker(t *testing.T) {
	now := time.Date(2026, 10, 12, 13, 0, 0, 999_000_000, time.FixedZone("MSK", 3*3600))
	body, err := json.Marshal(NewCheckRequested(42, "http://target:8090/", "GET", 5000, 200, now))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"scheduled_at":"2026-10-12T10:00:00Z"`) {
		t.Errorf("scheduled_at must be UTC without fractions: %s", body)
	}
	m, err := message.ParseCheckRequested(body)
	if err != nil {
		t.Fatalf("checker would reject the message: %v", err)
	}
	if m.MonitorID != 42 || m.RequestID == "" {
		t.Errorf("parsed = %+v", m)
	}
}
