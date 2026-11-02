package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestServer(faults bool) *Server {
	return New(":0", faults, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func call(t *testing.T, s *Server, method, path string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

func TestHealthz(t *testing.T) {
	code, body := call(t, newTestServer(false), "GET", "/healthz")
	if code != 200 || body["status"] != "ok" {
		t.Fatalf("healthz = %d %v", code, body)
	}
}

func TestReadyzLifecycle(t *testing.T) {
	s := newTestServer(false)
	dbErr := error(nil)
	s.AddReadyCheck("postgres", func(context.Context) error { return dbErr })

	if code, body := call(t, s, "GET", "/readyz"); code != 503 || body["status"] != "not_ready" {
		t.Fatalf("before start: %d %v", code, body)
	}

	s.SetStarted()
	code, body := call(t, s, "GET", "/readyz")
	if code != 200 || body["status"] != "ready" || body["checks"].(map[string]any)["postgres"] != "ok" {
		t.Fatalf("started: %d %v", code, body)
	}

	dbErr = errors.New("connection refused")
	code, body = call(t, s, "GET", "/readyz")
	if code != 503 || body["checks"].(map[string]any)["postgres"] != "connection refused" {
		t.Fatalf("db down: %d %v", code, body)
	}

	dbErr = nil
	s.SetShuttingDown()
	if code, _ := call(t, s, "GET", "/readyz"); code != 503 {
		t.Fatalf("shutting down must be 503, got %d", code)
	}
}

func TestFaultsDisabledAre404(t *testing.T) {
	s := newTestServer(false)
	for _, p := range []string{"/debug/fault/ready?value=false", "/debug/fault/crash", "/debug/pprof/"} {
		if code, _ := call(t, s, "POST", p); code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", p, code)
		}
	}
}

func TestFaultReadyOverride(t *testing.T) {
	s := newTestServer(true)
	s.SetStarted()
	if code, _ := call(t, s, "POST", "/debug/fault/ready?value=false"); code != 200 {
		t.Fatalf("fault endpoint = %d", code)
	}
	if code, _ := call(t, s, "GET", "/readyz"); code != 503 {
		t.Fatalf("forced not ready: readyz = %d", code)
	}
	call(t, s, "POST", "/debug/fault/ready?value=true")
	if code, _ := call(t, s, "GET", "/readyz"); code != 200 {
		t.Fatalf("restored: readyz = %d", code)
	}
	if code, _ := call(t, s, "POST", "/debug/fault/leak?mb=0"); code != 400 {
		t.Fatalf("leak with mb=0 must be 400, got %d", code)
	}
}
