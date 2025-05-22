package db

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AxelTahmid/tinker/config"
	"github.com/AxelTahmid/tinker/internal/db/sqlc"
)

// Common errors that can be checked with errors.Is .
var (
	ErrNotInitialized = errors.New("database connection not yet initialized")
	ErrNoTenantID     = errors.New("no tenant ID in context")
)

// Pool represents a database connection pool with basic operations.
type Pool interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Ping(ctx context.Context) error
	Close()
}

// Store provides access to database operations with transaction support.
type Store interface {
	Queries() *sqlc.Queries
	Pool() Pool
	WithTransaction(ctx context.Context, fn TransactionFunc) error
	Ping(ctx context.Context) error
	Close()
}

// PostgresStore implements the Store interface with PostgreSQL.
type PostgresStore struct {
	pool   *pgxpool.Pool
	sqlc   *sqlc.Queries
	logger *slog.Logger
}

// RootStore provides root access to the database (bypassing tenant isolation).
type RootStore interface {
	Store
	// Additional root-specific methods could go here.
}

// PostgresRootStore implements the RootStore interface.
type PostgresRootStore struct {
	PostgresStore
}

// RiverStore provides access to the River job queue database.
type RiverStore interface {
	Pool() Pool
	Ping(ctx context.Context) error
	Close()
}

// PostgresRiverStore implements the RiverStore interface.
type PostgresRiverStore struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// DB provides access to all database stores.
type DB interface {
	RootStore() RootStore
	RiverStore() RiverStore
	Queue() QueueClient
	Ping(ctx context.Context) error
	Close()
	StartQueue(ctx context.Context, serverConf *config.Server) error
	StopQueue(ctx context.Context) error
}

// PostgresDB implements the DB interface for PostgreSQL.
type PostgresDB struct {
	rootStore  *PostgresRootStore
	riverStore *PostgresRiverStore
	queue      *RiverQueue
	logger     *slog.Logger
}

// New creates a new DB instance.
func New(ctx context.Context, conf *config.Database, logger *slog.Logger) (DB, error) {
	logger.DebugContext(ctx, "db: initializing database connections")

	// Create root connection pool.
	rootPool, err := createRootPool(ctx, conf, logger)
	if err != nil {
		// Close tenant connection if root connection fails.
		return nil, fmt.Errorf("failed to create root connection pool: %w", err)
	}
	logger.DebugContext(ctx, "db: created root connection pool")

	// Create river connection pool.
	riverPool, err := createRiverPool(ctx, conf, logger)
	if err != nil {
		// Close other connections if river connection fails.
		rootPool.Close()
		return nil, fmt.Errorf("failed to create river connection pool: %w", err)
	}
	logger.DebugContext(ctx, "db: created river connection pool")

	rootStore := &PostgresRootStore{
		PostgresStore: PostgresStore{
			pool:   rootPool,
			sqlc:   sqlc.New(rootPool),
			logger: logger,
		},
	}

	riverStore := &PostgresRiverStore{
		pool:   riverPool,
		logger: logger,
	}

	// Return the DB instance.
	return &PostgresDB{
		rootStore:  rootStore,
		riverStore: riverStore,
		logger:     logger,
	}, nil
}

// RootStore returns the root store.
func (db *PostgresDB) RootStore() RootStore {
	return db.rootStore
}

// RiverStore returns the river queue store.
func (db *PostgresDB) RiverStore() RiverStore {
	return db.riverStore
}

// Queue returns the queue client.
func (db *PostgresDB) Queue() QueueClient {
	return db.queue
}

// Ping verifies all database connections are working.
func (db *PostgresDB) Ping(ctx context.Context) error {
	if err := db.rootStore.Ping(ctx); err != nil {
		return fmt.Errorf("root store: %w", err)
	}

	if err := db.riverStore.Ping(ctx); err != nil {
		return fmt.Errorf("river store: %w", err)
	}

	return nil
}

// StartQueue initializes and starts the job queue.
func (db *PostgresDB) StartQueue(ctx context.Context, serverConf *config.Server) error {
	var err error
	db.queue, err = NewQueue(ctx, db.riverStore.pool, serverConf, db.logger)
	if err != nil {
		return fmt.Errorf("failed to initialize queue: %w", err)
	}

	return db.queue.Start(ctx)
}

// StopQueue stops the job queue.
func (db *PostgresDB) StopQueue(ctx context.Context) error {
	if db.queue == nil {
		return nil // Already stopped or never started.
	}

	return db.queue.Stop(ctx)
}

// Close closes all database connections.
func (db *PostgresDB) Close() {
	// Try to stop the queue.
	if db.queue != nil {
		_ = db.queue.Stop(context.Background())
	}

	db.rootStore.Close()
	db.riverStore.Close()
	db.logger.Debug("db: all database connections closed")
}

// Pool returns the underlying connection pool.
func (s *PostgresStore) Pool() Pool {
	return s.pool
}

// Queries returns the SQLC queries.
func (s *PostgresStore) Queries() *sqlc.Queries {
	return s.sqlc
}

// Ping verifies the database connection is working.
func (s *PostgresStore) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// Close closes the database connection.
func (s *PostgresStore) Close() {
	s.pool.Close()
}

// Pool returns the underlying connection pool.
func (s *PostgresRiverStore) Pool() Pool {
	return s.pool
}

// Ping verifies the database connection is working.
func (s *PostgresRiverStore) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// Close closes the database connection.
func (s *PostgresRiverStore) Close() {
	s.pool.Close()
}

// TransactionFunc is a function that executes within a transaction.
type TransactionFunc func(ctx context.Context, tx pgx.Tx) error

// WithTransaction executes the given function within a transaction.
func (s *PostgresStore) WithTransaction(ctx context.Context, fn TransactionFunc) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	defer func() {
		if p := recover(); p != nil {
			if rollbackErr := tx.Rollback(ctx); rollbackErr != nil {
				s.logger.ErrorContext(ctx, "failed to rollback transaction after panic",
					"panic", p, "rollback_error", rollbackErr)
			}
			panic(p) // Re-panic after rollback.
		} else if err != nil {
			if rollbackErr := tx.Rollback(ctx); rollbackErr != nil {
				s.logger.ErrorContext(ctx, "failed to rollback transaction after error",
					"error", err, "rollback_error", rollbackErr)
			}
		} else {
			if commitErr := tx.Commit(ctx); commitErr != nil {
				err = fmt.Errorf("failed to commit transaction: %w", commitErr)
			}
		}
	}()

	return fn(ctx, tx)
}
