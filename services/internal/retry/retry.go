// Package retry waits for dependencies on service start.
package retry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

const (
	firstDelay   = 500 * time.Millisecond
	maxDelay     = 5 * time.Second
	attemptLimit = 5 * time.Second
)

// Startup calls fn with exponential backoff until it succeeds or ctx ends.
// The caller sets the overall deadline on ctx (app.StartupTimeout = 30s).
// If ctx is cancelled by a signal, context.Canceled is returned as is.
func Startup(ctx context.Context, log *slog.Logger, what string, fn func(context.Context) error) error {
	delay := firstDelay
	for attempt := 1; ; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, attemptLimit)
		err := fn(attemptCtx)
		cancel()
		if err == nil {
			if attempt > 1 {
				log.Info("dependency is available", "dependency", what, "attempts", attempt)
			}
			return nil
		}
		log.Warn("dependency is not available yet, retrying",
			"dependency", what, "attempt", attempt, "retry_in", delay.String(), "error", err.Error())

		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("%s is not available after %d attempts, giving up: %w", what, attempt, err)
			}
			return ctx.Err()
		case <-time.After(delay):
		}
		delay = min(delay*2, maxDelay)
	}
}
