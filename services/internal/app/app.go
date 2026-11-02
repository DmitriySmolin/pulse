// Package app is the common service lifecycle: config check, logger,
// tracing, HTTP endpoints, signal handling and graceful shutdown.
//
//	start ─► /healthz 200, /readyz 503 (startup) ─► run() connects deps ─► SetStarted ─► /readyz 200
//	SIGTERM ─► /readyz 503, ctx cancelled ─► run() finishes current work ─► exit 0
//	(at most SHUTDOWN_TIMEOUT; a second SIGTERM/Ctrl+C kills the process immediately)
package app

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"pulse/internal/config"
	"pulse/internal/httpserver"
	"pulse/internal/logging"
	"pulse/internal/telemetry"
)

// StartupTimeout bounds how long a service waits for its dependencies on start.
const StartupTimeout = 30 * time.Second

// Base holds the settings every Go service has.
type Base struct {
	Service         string
	HTTPAddr        string
	ShutdownTimeout time.Duration
	LogLevel        slog.Level
	FaultsEnabled   bool
	OTelEndpoint    string
	OTelServiceName string
}

// LoadBase reads the common variables.
func LoadBase(env *config.Env, service, defaultAddr string) Base {
	level, err := logging.ParseLevel(env.String("LOG_LEVEL", "info"))
	if err != nil {
		env.Fail(err)
	}
	return Base{
		Service:         service,
		HTTPAddr:        env.String("HTTP_ADDR", defaultAddr),
		ShutdownTimeout: env.Duration("SHUTDOWN_TIMEOUT", 25*time.Second),
		LogLevel:        level,
		FaultsEnabled:   env.Bool("PULSE_FAULTS_ENABLED", false),
		OTelEndpoint:    env.String("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
		OTelServiceName: env.String("OTEL_SERVICE_NAME", service),
	}
}

// App is what a service's run function gets.
type App struct {
	Log  *slog.Logger
	HTTP *httpserver.Server
}

// RunFunc connects dependencies, calls a.HTTP.SetStarted() and works until
// ctx is cancelled (SIGTERM). It must return only after the current work is
// done. A non-nil error (e.g. a dependency that never came up) means exit 1.
type RunFunc func(ctx context.Context, a *App) error

// Run never returns: it exits the process with 0 or 1.
//
// `<binary> healthcheck [readyz]` does not start the service: it requests
// /healthz (or /readyz) of the instance already running in the same container
// and exits 0 or 1. Distroless and scratch images have no curl, so this is
// what a Docker HEALTHCHECK can call.
func Run(env *config.Env, base Base, run RunFunc) {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		path := "/healthz"
		if len(os.Args) > 2 && os.Args[2] == "readyz" {
			path = "/readyz"
		}
		os.Exit(probeSelf(base.HTTPAddr, path))
	}

	log := logging.New(os.Stdout, base.Service, base.LogLevel)
	slog.SetDefault(log)
	if err := env.Err(); err != nil {
		log.Error("invalid configuration", "error", err.Error())
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	shutdownTracing, err := telemetry.Setup(ctx, base.OTelEndpoint, base.OTelServiceName)
	if err != nil {
		log.Error("tracing setup failed", "error", err.Error())
		os.Exit(1)
	}

	srv := httpserver.New(base.HTTPAddr, base.FaultsEnabled, log)
	if err := srv.Start(); err != nil {
		log.Error("http listen failed", "addr", base.HTTPAddr, "error", err.Error())
		os.Exit(1)
	}
	log.Info("service starting", "http_addr", base.HTTPAddr,
		"faults_enabled", base.FaultsEnabled, "tracing", base.OTelEndpoint != "")

	done := make(chan error, 1)
	go func() { done <- run(ctx, &App{Log: log, HTTP: srv}) }()

	var runErr error
	deadline := time.Now().Add(3 * time.Second) // budget for the final HTTP/tracing cleanup
	select {
	case runErr = <-done: // finished without a signal: startup failure or fatal error
	case <-ctx.Done():
		stop() // a second signal now terminates the process immediately
		srv.SetShuttingDown()
		log.Info("shutdown signal received, finishing current work", "timeout", base.ShutdownTimeout.String())
		deadline = time.Now().Add(base.ShutdownTimeout)
		select {
		case runErr = <-done:
		case <-time.After(time.Until(deadline)):
			log.Warn("shutdown timeout exceeded, exiting with work in progress " +
				"(unacknowledged messages will be redelivered)")
		}
	}

	// Let in-flight HTTP requests finish (target's slow mode), but not past the deadline.
	if minimum := time.Now().Add(time.Second); deadline.Before(minimum) {
		deadline = minimum
	}
	cleanupCtx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	if err := srv.Shutdown(cleanupCtx); err != nil {
		log.Warn("http shutdown", "error", err.Error())
	}
	if err := shutdownTracing(cleanupCtx); err != nil {
		log.Warn("tracing shutdown", "error", err.Error())
	}

	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		log.Error("service failed", "error", runErr.Error())
		os.Exit(1)
	}
	log.Info("service stopped")
}
