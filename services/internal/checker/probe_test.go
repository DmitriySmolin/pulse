package checker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pulse/internal/message"
)

func probeReq(url string) message.CheckRequested {
	return message.CheckRequested{MonitorID: 1, URL: url, Method: "GET", TimeoutMS: 500, ExpectedStatus: 200}
}

func TestProbe(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.UserAgent()
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write([]byte("ok"))
		case "/503":
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(strings.Repeat("x", 100<<10)))
		case "/slow":
			time.Sleep(2 * time.Second)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		}
	}))
	defer srv.Close()
	p := NewProber("PulseChecker/test")

	res := p.Probe(context.Background(), probeReq(srv.URL+"/ok"))
	if !res.OK || res.StatusCode != 200 || string(res.Body) != "ok" || gotUA != "PulseChecker/test" {
		t.Errorf("ok: %+v ua=%q", res, gotUA)
	}

	res = p.Probe(context.Background(), probeReq(srv.URL+"/503"))
	if res.OK || res.StatusCode != 503 || res.Err != "unexpected status 503, expected 200" || len(res.Body) != bodyLimit {
		t.Errorf("503: ok=%v code=%d err=%q body=%d", res.OK, res.StatusCode, res.Err, len(res.Body))
	}

	res = p.Probe(context.Background(), probeReq(srv.URL+"/slow"))
	if res.OK || res.StatusCode != 0 || res.Err != "timeout after 500ms" {
		t.Errorf("slow: %+v", res)
	}

	res = p.Probe(context.Background(), probeReq(srv.URL+"/loop"))
	if res.OK || !strings.Contains(res.Err, "stopped after 5 redirects") {
		t.Errorf("redirect loop: %+v", res)
	}

	res = p.Probe(context.Background(), probeReq("http://127.0.0.1:1/"))
	if res.OK || !strings.Contains(res.Err, "connect") || strings.Contains(res.Err, "127.0.0.1:1/\"") {
		t.Errorf("refused: %q", res.Err)
	}
}
