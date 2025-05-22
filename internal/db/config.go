package db

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/tracelog"

	"github.com/AxelTahmid/tinker/config"
)

// createRootPool creates a new connection pool for root/admin access.
func createRootPool(ctx context.Context, conf *config.Database, logger *slog.Logger) (*pgxpool.Pool, error) {
	poolConfig, err := parsePoolConfig(conf.RootUrl, conf, logger)
	if err != nil {
		return nil, err
	}

	// Set up root-specific callbacks.
	poolConfig.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		if err = setDBTimeZone(ctx, conn, conf.TimeZone, logger); err != nil {
			return err
		}
		return nil
		// return registerDataTypes(ctx, conn)
	}

	return pgxpool.NewWithConfig(ctx, poolConfig)
}

// createRiverPool creates a new connection pool for the river job queue.
func createRiverPool(ctx context.Context, conf *config.Database, logger *slog.Logger) (*pgxpool.Pool, error) {
	poolConfig, err := pgxpool.ParseConfig(conf.RiverUrl)
	if err != nil {
		return nil, fmt.Errorf("failed to parse river database URL: %w", err)
	}

	if poolConfig.ConnConfig.RuntimeParams["search_path"] == "" {
		return nil, errors.New("river database URL must include a search_path")
	}

	// maybe enable on development environments.
	// poolConfig.ConnConfig.Tracer = &tracelog.TraceLog{.
	// 	Logger:   NewDBLogger(logger),.
	// 	LogLevel: tracelog.LogLevelTrace,.
	// }.

	// Set basic pool configuration.
	// configurePoolSettings(poolConfig, conf).

	return pgxpool.NewWithConfig(ctx, poolConfig)
}

// parsePoolConfig creates a base connection pool configuration.
func parsePoolConfig(dbURL string, conf *config.Database, logger *slog.Logger) (*pgxpool.Config, error) {
	poolConfig, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse database URL: %w", err)
	}

	// Configure common pool settings.
	configurePoolSettings(poolConfig, conf)

	// Configure logging.
	poolConfig.ConnConfig.Tracer = &tracelog.TraceLog{
		Logger:   NewDBLogger(logger),
		LogLevel: tracelog.LogLevelTrace,
	}

	return poolConfig, nil
}

// configurePoolSettings applies common settings to a connection pool.
func configurePoolSettings(config *pgxpool.Config, conf *config.Database) {
	config.MaxConns = conf.PoolMax
	config.MinConns = conf.PoolMin
	config.MaxConnLifetime = conf.MaxConnLifetime
	config.MaxConnIdleTime = conf.MaxConnIdleTime
	config.HealthCheckPeriod = conf.HealthCheckPeriod
	config.ConnConfig.ConnectTimeout = conf.ConnectTimeout
}

// setDBTimeZone sets the timezone for a database connection.
func setDBTimeZone(ctx context.Context, conn *pgx.Conn, tz string, logger *slog.Logger) error {
	_, err := conn.Exec(ctx, "SET TIME ZONE "+pgx.Identifier{tz}.Sanitize())
	if err != nil {
		logger.ErrorContext(ctx, "failed to set timezone", "error", err, "timezone", tz)
		return fmt.Errorf("set timezone: %w", err)
	}

	logger.DebugContext(ctx, "timezone set", "timezone", tz)
	return nil
}
