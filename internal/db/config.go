package db

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/AxelTahmid/tinker/config"
	"github.com/AxelTahmid/tinker/internal/utils/ctxkeys"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/tracelog"
)

func parseConfig(conf config.Database, logger *slog.Logger) (*pgxpool.Config, error) {
	dbConfig, err := pgxpool.ParseConfig(conf.Url)
	if err != nil {
		return nil, fmt.Errorf("failed to parse database URL: %w", err)
	}

	dbConfig.MaxConns = conf.PoolMax
	dbConfig.MinConns = conf.PoolMin
	dbConfig.MaxConnLifetime = conf.MaxConnLifetime
	dbConfig.MaxConnIdleTime = conf.MaxConnIdleTime
	dbConfig.HealthCheckPeriod = conf.HealthCheckPeriod
	dbConfig.ConnConfig.ConnectTimeout = conf.ConnectTimeout

	dbConfig.ConnConfig.Tracer = &tracelog.TraceLog{
		Logger:   NewDBLogger(logger),
		LogLevel: tracelog.LogLevelTrace,
	}

	dbConfig.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		return setDBTimeZone(ctx, conn, conf.TimeZone, logger)
	}

	dbConfig.BeforeAcquire = func(ctx context.Context, conn *pgx.Conn) bool {
		return handleTenantAcquisition(ctx, conn, logger)
	}

	dbConfig.AfterRelease = func(conn *pgx.Conn) bool {
		return handleTenantRelease(conn, logger)
	}

	return dbConfig, nil
}

func setDBTimeZone(ctx context.Context, conn *pgx.Conn, tz string, logger *slog.Logger) error {
	if _, err := conn.Exec(ctx, fmt.Sprintf("SET TIME ZONE %s", pgx.Identifier{tz}.Sanitize())); err != nil {
		logger.Error("Failed to set timezone", "error", err, "timezone", tz)
		return fmt.Errorf("set timezone: %w", err)
	}
	logger.Debug("Timezone set", "timezone", tz)
	return nil
}

func handleTenantAcquisition(ctx context.Context, conn *pgx.Conn, logger *slog.Logger) bool {
	tenantID := ctx.Value(ctxkeys.TenantID)
	if tenantID == nil {
		logger.Warn("No tenant ID in context during connection acquisition")
		// return false
	}
	_, err := conn.Exec(ctx,
		"SELECT SET_CONFIG('app.current_tenant', $1, FALSE);",
		tenantID,
	)
	if err != nil {
		logger.Error("Failed to set tenant ID", "error", err, "tenantID", tenantID)
		// return false
	}
	return true
}

func handleTenantRelease(conn *pgx.Conn, logger *slog.Logger) bool {
	if _, err := conn.Exec(context.Background(), "RESET app.current_tenant;"); err != nil {
		logger.Error("Failed to reset tenant ID", "error", err)
		return false
	}
	return true
}
