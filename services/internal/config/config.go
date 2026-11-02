// Package config reads service settings from environment variables.
//
// Any variable can also be passed as a file: KEY_FILE=/run/secrets/key
// (Docker/Kubernetes secrets). Setting both KEY and KEY_FILE is an error.
// Empty values are treated as unset, so `KEY=` in compose falls back to the default.
//
// Errors are collected instead of returned one by one: on startup the service
// reports every problem at once (see Env.Err).
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Env is a collecting reader of configuration values.
type Env struct {
	lookup   func(string) (string, bool)
	readFile func(string) ([]byte, error)
	errs     []error
}

// FromOS reads the process environment.
func FromOS() *Env {
	return &Env{lookup: os.LookupEnv, readFile: os.ReadFile}
}

// FromMap reads vars instead of the process environment (for tests).
// KEY_FILE paths are still read from disk.
func FromMap(vars map[string]string) *Env {
	return &Env{
		lookup:   func(k string) (string, bool) { v, ok := vars[k]; return v, ok },
		readFile: os.ReadFile,
	}
}

// Fail records a configuration error found by the caller.
func (e *Env) Fail(err error) { e.errs = append(e.errs, err) }

// Err returns all collected errors joined, or nil.
func (e *Env) Err() error { return errors.Join(e.errs...) }

type state int

const (
	unset state = iota
	set
	invalid // an error has already been recorded
)

func (e *Env) nonEmpty(key string) (string, bool) {
	v, ok := e.lookup(key)
	return v, ok && v != ""
}

// get resolves KEY or KEY_FILE.
func (e *Env) get(key string) (string, state) {
	val, hasVal := e.nonEmpty(key)
	path, hasFile := e.nonEmpty(key + "_FILE")
	switch {
	case hasVal && hasFile:
		e.Fail(fmt.Errorf("%s and %s_FILE are both set: use only one of them", key, key))
		return "", invalid
	case hasFile:
		b, err := e.readFile(path)
		if err != nil {
			e.Fail(fmt.Errorf("%s_FILE: %w", key, err))
			return "", invalid
		}
		v := strings.TrimRight(string(b), "\r\n")
		if v == "" {
			e.Fail(fmt.Errorf("%s_FILE: file %s is empty", key, path))
			return "", invalid
		}
		return v, set
	case hasVal:
		return val, set
	}
	return "", unset
}

// String returns the value or def when the variable is unset.
func (e *Env) String(key, def string) string {
	if v, st := e.get(key); st == set {
		return v
	}
	return def
}

// Required returns the value and records an error when it is unset.
func (e *Env) Required(key string) string {
	v, st := e.get(key)
	if st == unset {
		e.Fail(fmt.Errorf("%s is required (or %s_FILE)", key, key))
	}
	return v
}

// Bool accepts 1/0, true/false, t/f (strconv.ParseBool).
func (e *Env) Bool(key string, def bool) bool {
	v, st := e.get(key)
	if st != set {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		e.Fail(fmt.Errorf("%s: %q is not a boolean (use true/false or 1/0)", key, v))
		return def
	}
	return b
}

// Int returns a positive integer (>= 1).
func (e *Env) Int(key string, def int) int {
	v, st := e.get(key)
	if st != set {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		e.Fail(fmt.Errorf("%s: %q must be a positive integer", key, v))
		return def
	}
	return n
}

// Duration returns a positive Go duration such as 5s, 10m, 1h30m.
func (e *Env) Duration(key string, def time.Duration) time.Duration {
	v, st := e.get(key)
	if st != set {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		e.Fail(fmt.Errorf("%s: %q must be a positive duration like 5s or 10m", key, v))
		return def
	}
	return d
}
