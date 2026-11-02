package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSelfURL(t *testing.T) {
	cases := map[string]string{
		":8082":          "http://127.0.0.1:8082/healthz",
		"0.0.0.0:8082":   "http://127.0.0.1:8082/healthz",
		"[::]:8082":      "http://127.0.0.1:8082/healthz",
		"127.0.0.1:8090": "http://127.0.0.1:8090/healthz",
		"10.0.0.5:8080":  "http://10.0.0.5:8080/healthz",
	}
	for addr, want := range cases {
		got, err := selfURL(addr, "/healthz")
		if err != nil || got != want {
			t.Errorf("selfURL(%q) = %q, %v; want %q", addr, got, err, want)
		}
	}
	if _, err := selfURL("8082", "/healthz"); err == nil {
		t.Error("selfURL without a port separator must fail")
	}
}

func TestProbeSelf(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	if code := probeSelf(addr, "/healthz"); code != 0 {
		t.Errorf("healthz: exit %d, want 0", code)
	}
	if code := probeSelf(addr, "/readyz"); code != 1 {
		t.Errorf("readyz 503: exit %d, want 1", code)
	}
	if code := probeSelf("127.0.0.1:1", "/healthz"); code != 1 {
		t.Errorf("closed port: exit %d, want 1", code)
	}
}
