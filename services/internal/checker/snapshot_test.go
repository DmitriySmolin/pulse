package checker

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"pulse/internal/message"
)

func TestSnapshot(t *testing.T) {
	if got := SnapshotKey(3, 1234); got != "3/1234.txt" {
		t.Errorf("SnapshotKey = %q", got)
	}

	req := message.CheckRequested{MonitorID: 3, URL: "http://target:8090/status/503", Method: "GET"}
	res := Result{
		CheckedAt: time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC), StatusCode: 503,
		Proto: "HTTP/1.1", Status: "503 Service Unavailable", Err: "unexpected status 503, expected 200",
		Header: http.Header{"X-B": {"2"}, "Content-Type": {"text/plain"}}, Body: []byte("down"),
	}
	got := string(FormatSnapshot(req, 1234, res))
	for _, want := range []string{"check_id: 1234\n", "error: unexpected status 503", "HTTP/1.1 503 Service Unavailable\n",
		"Content-Type: text/plain\nX-B: 2\n\ndown"} {
		if !strings.Contains(got, want) {
			t.Errorf("snapshot does not contain %q:\n%s", want, got)
		}
	}

	noResp := string(FormatSnapshot(req, 1, Result{Err: "timeout after 5000ms"}))
	if !strings.Contains(noResp, "(no response)") {
		t.Errorf("no-response snapshot:\n%s", noResp)
	}
}
