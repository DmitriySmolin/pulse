package httpserver

import (
	"net/http"
	"net/http/pprof"
	"os"
	"strconv"
	"sync"
	"time"
)

// Chaos handlers for the labs (CONTRACT.md, "Fault injection").
func (s *Server) registerFaults() {
	s.mux.HandleFunc("POST /debug/fault/leak", s.faultLeak)
	s.mux.HandleFunc("POST /debug/fault/cpu", s.faultCPU)
	s.mux.HandleFunc("POST /debug/fault/crash", s.faultCrash)
	s.mux.HandleFunc("POST /debug/fault/ready", s.faultReady)

	s.mux.HandleFunc("/debug/pprof/", pprof.Index) // heap, goroutine, allocs, ...
	s.mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	s.mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	s.mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	s.mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
}

// leaked memory is never released: that is the point.
var leaked struct {
	sync.Mutex
	chunks [][]byte
	mb     int
}

func intParam(r *http.Request, name string, minV, maxV int) (int, bool) {
	n, err := strconv.Atoi(r.URL.Query().Get(name))
	return n, err == nil && n >= minV && n <= maxV
}

// POST /debug/fault/leak?mb=N — allocate and hold N MiB (cumulative).
func (s *Server) faultLeak(w http.ResponseWriter, r *http.Request) {
	mb, ok := intParam(r, "mb", 1, 16384)
	if !ok {
		WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "mb must be 1..16384"})
		return
	}
	chunk := make([]byte, mb<<20)
	for i := 0; i < len(chunk); i += 4096 {
		chunk[i] = 1 // touch every page so it really counts in RSS
	}
	leaked.Lock()
	leaked.chunks = append(leaked.chunks, chunk)
	leaked.mb += mb
	total := leaked.mb
	leaked.Unlock()

	s.log.Warn("fault: memory leaked", "mb", mb, "total_mb", total)
	WriteJSON(w, http.StatusOK, map[string]int{"leaked_mb": total})
}

// POST /debug/fault/cpu?seconds=N — burn one core for N seconds (in background).
func (s *Server) faultCPU(w http.ResponseWriter, r *http.Request) {
	sec, ok := intParam(r, "seconds", 1, 3600)
	if !ok {
		WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "seconds must be 1..3600"})
		return
	}
	go func() {
		deadline := time.Now().Add(time.Duration(sec) * time.Second)
		for x := 0; time.Now().Before(deadline); x++ {
			_ = x * x
		}
	}()
	s.log.Warn("fault: cpu burn started", "seconds", sec)
	WriteJSON(w, http.StatusAccepted, map[string]int{"cpu_seconds": sec})
}

// POST /debug/fault/crash — exit immediately with code 2, no graceful shutdown.
func (s *Server) faultCrash(http.ResponseWriter, *http.Request) {
	s.log.Error("fault: crash requested, exiting with code 2")
	os.Exit(2)
}

// POST /debug/fault/ready?value=false|true — force /readyz to fail or restore it.
func (s *Server) faultReady(w http.ResponseWriter, r *http.Request) {
	ready, err := strconv.ParseBool(r.URL.Query().Get("value"))
	if err != nil {
		WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "value must be true or false"})
		return
	}
	s.forcedNotReady.Store(!ready)
	s.log.Warn("fault: readiness override", "ready", ready)
	WriteJSON(w, http.StatusOK, map[string]bool{"ready": ready})
}
