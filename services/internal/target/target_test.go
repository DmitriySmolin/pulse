package target

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func newTarget(t *testing.T) (*Server, *http.ServeMux) {
	t.Helper()
	s := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.SlowDelay = 50 * time.Millisecond
	mux := http.NewServeMux()
	s.Register(mux)
	return s, mux
}

func do(mux *http.ServeMux, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestRootModes(t *testing.T) {
	s, mux := newTarget(t)

	if rec := do(mux, "GET", "/"); rec.Code != 200 || rec.Body.String() != "ok" {
		t.Fatalf("default mode: %d %q", rec.Code, rec.Body)
	}

	if rec := do(mux, "POST", "/admin/mode?mode=down"); rec.Code != 200 {
		t.Fatalf("set mode: %d", rec.Code)
	}
	if rec := do(mux, "GET", "/"); rec.Code != 503 {
		t.Fatalf("down mode: %d", rec.Code)
	}
	if rec := do(mux, "GET", "/admin/mode"); !strings.Contains(rec.Body.String(), `"down"`) {
		t.Fatalf("get mode: %s", rec.Body)
	}

	do(mux, "POST", "/admin/mode?mode=slow")
	start := time.Now()
	if rec := do(mux, "GET", "/"); rec.Code != 200 || time.Since(start) < s.SlowDelay {
		t.Fatalf("slow mode: %d after %v", rec.Code, time.Since(start))
	}

	do(mux, "POST", "/admin/mode?mode=flaky")
	s.Rand = func() float64 { return 0.1 }
	if rec := do(mux, "GET", "/"); rec.Code != 500 {
		t.Fatalf("flaky (unlucky): %d", rec.Code)
	}
	s.Rand = func() float64 { return 0.9 }
	if rec := do(mux, "GET", "/"); rec.Code != 200 {
		t.Fatalf("flaky (lucky): %d", rec.Code)
	}

	if rec := do(mux, "POST", "/admin/mode?mode=broken"); rec.Code != 400 {
		t.Fatalf("invalid mode: %d", rec.Code)
	}
	if rec := do(mux, "GET", "/unknown"); rec.Code != 404 {
		t.Fatalf("unknown path: %d", rec.Code)
	}
}

func TestStatusDelayFlaky(t *testing.T) {
	s, mux := newTarget(t)
	cases := []struct {
		path string
		code int
	}{
		{"/status/503", 503},
		{"/status/204", 204},
		{"/status/99", 400},
		{"/status/abc", 400},
		{"/delay/20", 200},
		{"/delay/-1", 400},
		{"/flaky?rate=0", 200},
		{"/flaky?rate=1", 500},
		{"/flaky?rate=2", 400},
	}
	s.Rand = func() float64 { return 0.5 }
	for _, c := range cases {
		if rec := do(mux, "GET", c.path); rec.Code != c.code {
			t.Errorf("GET %s = %d, want %d", c.path, rec.Code, c.code)
		}
	}
}

func TestMetrics(t *testing.T) {
	_, mux := newTarget(t)
	before := testutil.ToFloat64(requestsTotal.WithLabelValues("/status/{code}", "GET", "418"))
	do(mux, "GET", "/status/418")
	after := testutil.ToFloat64(requestsTotal.WithLabelValues("/status/{code}", "GET", "418"))
	if after-before != 1 {
		t.Errorf("counter delta = %v, want 1", after-before)
	}
}
