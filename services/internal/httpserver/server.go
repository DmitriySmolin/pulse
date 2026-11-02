// Package httpserver is the service HTTP endpoint: /healthz, /readyz,
// /metrics and, with PULSE_FAULTS_ENABLED=1, the fault-injection handlers.
package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// CheckFunc is a readiness probe of one dependency.
type CheckFunc func(ctx context.Context) error

type readyCheck struct {
	name string
	fn   CheckFunc
}

// Server wraps http.Server with readiness state.
type Server struct {
	log *slog.Logger
	mux *http.ServeMux
	srv *http.Server

	mu     sync.Mutex
	checks []readyCheck

	started        atomic.Bool // dependencies are connected
	shuttingDown   atomic.Bool // SIGTERM received
	forcedNotReady atomic.Bool // /debug/fault/ready?value=false
}

// New registers the standard endpoints. Fault handlers are registered only
// when faults is true, otherwise those paths answer 404.
func New(addr string, faults bool, log *slog.Logger) *Server {
	s := &Server{log: log, mux: http.NewServeMux()}
	s.srv = &http.Server{
		Addr:              addr,
		Handler:           s.mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	s.mux.HandleFunc("GET /readyz", s.readyz)
	s.mux.Handle("GET /metrics", promhttp.Handler())
	if faults {
		s.registerFaults()
	}
	return s
}

// Handle adds an application route (net/http patterns, e.g. "GET /status/{code}").
func (s *Server) Handle(pattern string, h http.Handler) { s.mux.Handle(pattern, h) }

// AddReadyCheck adds a dependency to /readyz.
func (s *Server) AddReadyCheck(name string, fn CheckFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checks = append(s.checks, readyCheck{name, fn})
}

// SetStarted marks the end of startup: until then /readyz answers 503.
func (s *Server) SetStarted() { s.started.Store(true) }

// SetShuttingDown makes /readyz answer 503 so the load balancer stops sending traffic.
func (s *Server) SetShuttingDown() { s.shuttingDown.Store(true) }

// Start binds the port synchronously (so "address already in use" fails the
// start) and serves in the background.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.srv.Addr)
	if err != nil {
		return err
	}
	go func() {
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("http server failed", "error", err.Error())
		}
	}()
	return nil
}

// Shutdown stops accepting connections and waits for active requests.
func (s *Server) Shutdown(ctx context.Context) error { return s.srv.Shutdown(ctx) }

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	checks := map[string]string{}
	ready := true
	fail := func(name, reason string) {
		checks[name] = reason
		ready = false
	}
	if !s.started.Load() {
		fail("startup", "in progress")
	}
	if s.shuttingDown.Load() {
		fail("shutdown", "in progress")
	}
	if s.forcedNotReady.Load() {
		fail("fault", "forced not ready via /debug/fault/ready")
	}

	s.mu.Lock()
	list := append([]readyCheck(nil), s.checks...)
	s.mu.Unlock()
	for _, c := range list {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		err := c.fn(ctx)
		cancel()
		if err != nil {
			fail(c.name, err.Error())
		} else {
			checks[c.name] = "ok"
		}
	}

	if ready {
		WriteJSON(w, http.StatusOK, map[string]any{"status": "ready", "checks": checks})
		return
	}
	WriteJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready", "checks": checks})
}

// WriteJSON writes v as a JSON response.
func WriteJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
