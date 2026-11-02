package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeEnv(vars map[string]string) *Env { return FromMap(vars) }

func writeSecret(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDefaultsAndValues(t *testing.T) {
	e := fakeEnv(map[string]string{
		"TICK":    "250ms",
		"BATCH":   "7",
		"DECLARE": "false",
		"EMPTY":   "",
	})
	if got := e.Duration("TICK", time.Second); got != 250*time.Millisecond {
		t.Errorf("Duration = %v", got)
	}
	if got := e.Int("BATCH", 100); got != 7 {
		t.Errorf("Int = %d", got)
	}
	if got := e.Bool("DECLARE", true); got {
		t.Errorf("Bool = %v", got)
	}
	if got := e.String("EMPTY", "def"); got != "def" {
		t.Errorf("empty value must fall back to default, got %q", got)
	}
	if got := e.Int("MISSING", 42); got != 42 {
		t.Errorf("default Int = %d", got)
	}
	if err := e.Err(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFileForm(t *testing.T) {
	path := writeSecret(t, "postgres://u:p@db/pulse\n")
	e := fakeEnv(map[string]string{"DATABASE_URL_FILE": path})
	if got := e.Required("DATABASE_URL"); got != "postgres://u:p@db/pulse" {
		t.Errorf("value from file = %q (trailing newline must be trimmed)", got)
	}
	if err := e.Err(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBothFormsIsError(t *testing.T) {
	path := writeSecret(t, "x")
	e := fakeEnv(map[string]string{"AMQP_URL": "amqp://a", "AMQP_URL_FILE": path})
	e.Required("AMQP_URL")
	err := e.Err()
	if err == nil || !strings.Contains(err.Error(), "both set") {
		t.Fatalf("expected conflict error, got %v", err)
	}
	if strings.Contains(err.Error(), "required") {
		t.Errorf("conflict must not also be reported as missing: %v", err)
	}
}

func TestFileErrors(t *testing.T) {
	empty := writeSecret(t, "\n")
	e := fakeEnv(map[string]string{
		"A_FILE": "/nonexistent/secret",
		"B_FILE": empty,
	})
	e.Required("A")
	e.String("B", "def")
	err := e.Err()
	if err == nil || !strings.Contains(err.Error(), "A_FILE") || !strings.Contains(err.Error(), "B_FILE: file") {
		t.Fatalf("expected both file errors, got %v", err)
	}
}

func TestInvalidValuesAreCollected(t *testing.T) {
	e := fakeEnv(map[string]string{
		"WORKERS": "0",
		"TICK":    "soon",
		"FLAG":    "maybe",
	})
	e.Int("WORKERS", 8)
	e.Duration("TICK", time.Second)
	e.Bool("FLAG", false)
	e.Required("DATABASE_URL")
	err := e.Err()
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"WORKERS", "TICK", "FLAG", "DATABASE_URL is required"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}
