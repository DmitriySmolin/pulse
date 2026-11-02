package message

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestParseCheckRequestedFromContract(t *testing.T) {
	body := `{"request_id":"uuid","monitor_id":1,"url":"http://target:8090/","method":"GET",
 "timeout_ms":5000,"expected_status":200,"scheduled_at":"2026-10-12T10:00:00Z"}`
	m, err := ParseCheckRequested([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if m.MonitorID != 1 || m.URL != "http://target:8090/" || m.TimeoutMS != 5000 ||
		!m.ScheduledAt.Equal(time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("parsed = %+v", m)
	}
}

func TestParseCheckRequestedInvalid(t *testing.T) {
	cases := map[string]string{
		"not json":   `{"monitor_id":`,
		"no monitor": `{"url":"http://x","method":"GET","timeout_ms":1,"expected_status":200}`,
		"bad method": `{"monitor_id":1,"url":"http://x","method":"POST","timeout_ms":1,"expected_status":200}`,
		"bad status": `{"monitor_id":1,"url":"http://x","method":"GET","timeout_ms":1,"expected_status":700}`,
		"no timeout": `{"monitor_id":1,"url":"http://x","method":"GET","expected_status":200}`,
	}
	for name, body := range cases {
		if _, err := ParseCheckRequested([]byte(body)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestStatusChangedRoundTrip(t *testing.T) {
	id := int64(7)
	in := StatusChanged{MonitorID: 1, MonitorName: "target", URL: "http://target:8090/", From: "up", To: "down",
		At: Timestamp(time.Date(2026, 10, 12, 10, 0, 5, 123, time.UTC)), Error: "timeout after 5000ms", IncidentID: &id}
	body, _ := json.Marshal(in)
	want := `{"monitor_id":1,"monitor_name":"target","url":"http://target:8090/","from":"up","to":"down",` +
		`"at":"2026-10-12T10:00:05Z","error":"timeout after 5000ms","incident_id":7}`
	if string(body) != want {
		t.Fatalf("json:\n got %s\nwant %s", body, want)
	}
	out, err := ParseStatusChanged(body)
	if err != nil || *out.IncidentID != 7 || out.To != "down" {
		t.Fatalf("parsed = %+v, %v", out, err)
	}

	recovered, _ := json.Marshal(StatusChanged{MonitorID: 1, From: "down", To: "up", At: in.At})
	if strings.Contains(string(recovered), "error") || strings.Contains(string(recovered), "incident_id") {
		t.Errorf("empty error/incident must be omitted: %s", recovered)
	}

	if _, err := ParseStatusChanged([]byte(`{"monitor_id":1,"from":"up","to":"sideways"}`)); err == nil {
		t.Error("expected error for unknown status")
	}
}

func TestCheckResultFromContract(t *testing.T) {
	code := 503
	in := CheckResult{CheckID: 123, MonitorID: 1, CheckedAt: Timestamp(time.Date(2026, 10, 12, 10, 0, 5, 999, time.UTC)),
		OK: false, StatusCode: &code, LatencyMS: 41, Error: "unexpected status 503"}
	body, _ := json.Marshal(in)
	want := `{"check_id":123,"monitor_id":1,"checked_at":"2026-10-12T10:00:05Z","ok":false,` +
		`"status_code":503,"latency_ms":41,"error":"unexpected status 503"}`
	if string(body) != want {
		t.Fatalf("json:\n got %s\nwant %s", body, want)
	}
	out, err := ParseCheckResult(body)
	if err != nil || out.CheckID != 123 || *out.StatusCode != 503 || out.Key() != "1" {
		t.Fatalf("parsed = %+v, %v", out, err)
	}

	// No HTTP response (timeout, DNS): status_code is null; ok checks have no error field.
	noResp, _ := json.Marshal(CheckResult{CheckID: 1, MonitorID: 2, CheckedAt: in.CheckedAt, OK: true})
	if !strings.Contains(string(noResp), `"status_code":null`) || strings.Contains(string(noResp), `"error"`) {
		t.Errorf("unexpected json: %s", noResp)
	}
}

func TestCheckResultHour(t *testing.T) {
	cases := map[string]string{
		"2026-10-12T10:59:59Z":      "2026-10-12T10:00:00Z",
		"2026-10-12T10:00:00Z":      "2026-10-12T10:00:00Z",
		"2026-10-12T13:30:00+03:00": "2026-10-12T10:00:00Z", // converted to UTC
		"2026-10-12T23:59:00-05:30": "2026-10-13T05:00:00Z", // half-hour zones too
	}
	for in, want := range cases {
		at, _ := time.Parse(time.RFC3339, in)
		if got := (CheckResult{CheckedAt: at}).Hour().Format(time.RFC3339); got != want {
			t.Errorf("Hour(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestParseCheckResultInvalid(t *testing.T) {
	cases := map[string]string{
		"not json":   `{"check_id":`,
		"no check":   `{"monitor_id":1,"checked_at":"2026-10-12T10:00:05Z","ok":true,"latency_ms":1}`,
		"no monitor": `{"check_id":1,"checked_at":"2026-10-12T10:00:05Z","ok":true,"latency_ms":1}`,
		"no time":    `{"check_id":1,"monitor_id":1,"ok":true,"latency_ms":1}`,
		"bad time":   `{"check_id":1,"monitor_id":1,"checked_at":"yesterday","ok":true}`,
		"negative":   `{"check_id":1,"monitor_id":1,"checked_at":"2026-10-12T10:00:05Z","ok":true,"latency_ms":-1}`,
	}
	for name, body := range cases {
		if _, err := ParseCheckResult([]byte(body)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestNewRequestID(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	a, b := NewRequestID(), NewRequestID()
	if !re.MatchString(a) || a == b {
		t.Fatalf("bad ids: %s %s", a, b)
	}
}
