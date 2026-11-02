// Package target is a controllable "victim" for checks and chaos labs.
package target

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"pulse/internal/httpserver"
)

// Modes of GET /.
const (
	ModeUp    = "up"    // 200 "ok"
	ModeDown  = "down"  // 503 "down"
	ModeSlow  = "slow"  // 200 "ok" after SlowDelay (10s)
	ModeFlaky = "flaky" // 500 with probability FlakyRate (50%)
)

var (
	requestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "pulse_target_http_requests_total", Help: "Requests to target by route, method and status code."},
		[]string{"route", "method", "code"})
	requestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "pulse_target_http_request_duration_seconds", Help: "Target request duration by route.",
		Buckets: []float64{.005, .01, .05, .1, .25, .5, 1, 2.5, 5, 10, 30}}, []string{"route"})
)

// Server holds the current mode.
type Server struct {
	SlowDelay time.Duration
	FlakyRate float64
	Rand      func() float64 // replaceable in tests
	Log       *slog.Logger

	mu   sync.RWMutex
	mode string
}

// New returns a target in mode "up".
func New(log *slog.Logger) *Server {
	return &Server{SlowDelay: 10 * time.Second, FlakyRate: 0.5, Rand: rand.Float64, Log: log, mode: ModeUp}
}

// Router is what Register needs (*http.ServeMux or *httpserver.Server).
type Router interface {
	Handle(pattern string, h http.Handler)
}

// Register adds the routes from CONTRACT.md ("target"), each with metrics.
func (s *Server) Register(r Router) {
	routes := []struct {
		pattern, route string
		h              http.HandlerFunc
	}{
		{"GET /{$}", "/", s.root},
		{"GET /status/{code}", "/status/{code}", s.status},
		{"GET /delay/{ms}", "/delay/{ms}", s.delay},
		{"GET /flaky", "/flaky", s.flaky},
		{"GET /admin/mode", "/admin/mode", s.getMode},
		{"POST /admin/mode", "/admin/mode", s.setMode},
	}
	for _, rt := range routes {
		r.Handle(rt.pattern, instrument(rt.route, rt.h))
	}
}

func (s *Server) currentMode() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mode
}

func (s *Server) root(w http.ResponseWriter, r *http.Request) {
	switch s.currentMode() {
	case ModeDown:
		text(w, http.StatusServiceUnavailable, "down")
	case ModeSlow:
		if sleep(r.Context(), s.SlowDelay) {
			text(w, http.StatusOK, "ok")
		}
	case ModeFlaky:
		s.flakyAnswer(w, s.FlakyRate)
	default:
		text(w, http.StatusOK, "ok")
	}
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	// 1xx cannot be a final response in net/http, so the range starts at 200.
	code, err := strconv.Atoi(r.PathValue("code"))
	if err != nil || code < 200 || code > 599 {
		text(w, http.StatusBadRequest, "code must be 200..599")
		return
	}
	text(w, code, http.StatusText(code))
}

func (s *Server) delay(w http.ResponseWriter, r *http.Request) {
	ms, err := strconv.Atoi(r.PathValue("ms"))
	if err != nil || ms < 0 || ms > 60000 {
		text(w, http.StatusBadRequest, "ms must be 0..60000")
		return
	}
	if sleep(r.Context(), time.Duration(ms)*time.Millisecond) {
		text(w, http.StatusOK, "ok")
	}
}

func (s *Server) flaky(w http.ResponseWriter, r *http.Request) {
	rate := s.FlakyRate
	if v := r.URL.Query().Get("rate"); v != "" {
		var err error
		rate, err = strconv.ParseFloat(v, 64)
		if err != nil || rate < 0 || rate > 1 {
			text(w, http.StatusBadRequest, "rate must be 0..1")
			return
		}
	}
	s.flakyAnswer(w, rate)
}

func (s *Server) flakyAnswer(w http.ResponseWriter, rate float64) {
	if s.Rand() < rate {
		text(w, http.StatusInternalServerError, "flaky error")
		return
	}
	text(w, http.StatusOK, "ok")
}

func (s *Server) getMode(w http.ResponseWriter, _ *http.Request) {
	httpserver.WriteJSON(w, http.StatusOK, map[string]string{"mode": s.currentMode()})
}

func (s *Server) setMode(w http.ResponseWriter, r *http.Request) {
	mode := r.URL.Query().Get("mode")
	switch mode {
	case ModeUp, ModeDown, ModeSlow, ModeFlaky:
	default:
		httpserver.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "mode must be up|down|slow|flaky"})
		return
	}
	s.mu.Lock()
	prev := s.mode
	s.mode = mode
	s.mu.Unlock()
	s.Log.Info("mode changed", "from", prev, "to", mode)
	httpserver.WriteJSON(w, http.StatusOK, map[string]string{"mode": mode})
}

func text(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(body))
}

// sleep returns false if the client went away first.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// instrument counts requests with a fixed route label (no raw paths: low cardinality).
func instrument(route string, h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		h(rec, r)
		requestsTotal.WithLabelValues(route, r.Method, strconv.Itoa(rec.code)).Inc()
		requestDuration.WithLabelValues(route).Observe(time.Since(start).Seconds())
	})
}
