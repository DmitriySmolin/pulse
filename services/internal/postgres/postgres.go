// Package postgres opens the pgx connection pool.
package postgres

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"pulse/internal/retry"
)

// Connect creates a pool and waits (with retries, bounded by ctx) until
// PostgreSQL answers. appName is shown in pg_stat_activity.application_name.
// Pool size can be tuned in the URL: ?pool_max_conns=10.
func Connect(ctx context.Context, url, appName string, log *slog.Logger) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("DATABASE_URL: %w", err)
	}
	cfg.ConnConfig.RuntimeParams["application_name"] = appName

	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		return nil, fmt.Errorf("postgres pool: %w", err)
	}
	if err := retry.Startup(ctx, log, "postgres", pool.Ping); err != nil {
		pool.Close()
		return nil, err
	}
	log.Info("connected to postgres", "host", cfg.ConnConfig.Host, "database", cfg.ConnConfig.Database)
	return pool, nil
}
