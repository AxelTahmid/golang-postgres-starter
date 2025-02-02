package db

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AxelTahmid/tinker/config"
	"github.com/AxelTahmid/tinker/internal/db/sqlc"
)

type Postgres interface {
	Conn() *pgxpool.Pool
	Queries() *sqlc.Queries
	Ping(ctx context.Context) error
	Close()
}

type postgres struct {
	pool *pgxpool.Pool
	sqlc *sqlc.Queries
}

func NewPostgres(ctx context.Context, conf config.Database, logger *slog.Logger) (Postgres, error) {
	parsedConfig, err := parseConfig(conf, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to parse database config: %w", err)
	}

	dbPool, err := pgxpool.NewWithConfig(ctx, parsedConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool: %w", err)
	}

	return &postgres{
		pool: dbPool,
		sqlc: sqlc.New(dbPool),
	}, nil
}

func (p *postgres) Conn() *pgxpool.Pool {
	return p.pool
}
func (p *postgres) Queries() *sqlc.Queries {
	return p.sqlc
}
func (p *postgres) Ping(ctx context.Context) error {
	return p.pool.Ping(ctx)
}
func (p *postgres) Close() {
	p.pool.Close()
}

// type TransactionFunc func(ctx context.Context, tx pgx.Tx) error

// func (p *Postgres) WithTransaction(ctx context.Context, fn TransactionFunc) error {
// 	// Begin the transaction
// 	tx, err := p.pool.Begin(ctx)
// 	if err != nil {
// 		return fmt.Errorf("failed to begin transaction: %w", err)
// 	}

// 	// Ensure proper rollback or commit
// 	defer func() {
// 		if p := recover(); p != nil {
// 			tx.Rollback(ctx)
// 			panic(p)
// 		} else if err != nil {
// 			tx.Rollback(ctx)
// 		} else {
// 			err = tx.Commit(ctx)
// 		}
// 	}()

// 	return fn(ctx, tx)
// }
