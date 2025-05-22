package db

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"

	"github.com/AxelTahmid/tinker/config"
	"github.com/AxelTahmid/tinker/internal/db/jobs"
)

// Common queue-related errors.
var (
	ErrQueueNotInitialized = errors.New("job queue not initialized")
	ErrUnknownAction       = errors.New("unknown action type")
)

// QueueClient provides an interface for job queue operations.
type QueueClient interface {
	// Job management.
	Insert(ctx context.Context, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error)
	InsertTx(
		ctx context.Context,
		tx pgx.Tx,
		args river.JobArgs,
		opts *river.InsertOpts,
	) (*rivertype.JobInsertResult, error)

	// Queue management.
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// RiverQueue implements the QueueClient interface using River.
type RiverQueue struct {
	client *river.Client[pgx.Tx]
	logger *slog.Logger
}

// NewQueue creates a new queue client.
func NewQueue(ctx context.Context, pool *pgxpool.Pool, conf *config.Server, logger *slog.Logger) (*RiverQueue, error) {
	logger.DebugContext(ctx, "queue: initializing job queue")

	// Register all workers.
	workers := river.NewWorkers()

	// Register Helcim workers
	logger.DebugContext(ctx, "queue: registering job workers")
	jobs.RegisterWorkers(workers)

	// Define queue configurations.
	queueConfig := map[string]river.QueueConfig{
		jobs.QueueDefault:  {MaxWorkers: jobs.MaxDefaultWorkers},
		jobs.QueueHelcim:   {MaxWorkers: jobs.MaxHelcimWorkers},
		jobs.QueuePayments: {MaxWorkers: jobs.MaxPaymentsWorkers},
	}

	// Create the client.
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Logger:   logger,
		TestOnly: conf.AppEnv != "production",
		Workers:  workers,
		Queues:   queueConfig,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create river client: %w", err)
	}

	queue := &RiverQueue{
		client: client,
		logger: logger,
	}

	return queue, nil
}

// Start starts the queue processor.
func (q *RiverQueue) Start(ctx context.Context) error {
	q.logger.InfoContext(ctx, "queue: starting job processor")
	return q.client.Start(ctx)
}

// Stop stops the queue processor.
func (q *RiverQueue) Stop(ctx context.Context) error {
	q.logger.InfoContext(ctx, "queue: stopping job processor")
	return q.client.Stop(ctx)
}

// Insert adds a job to the queue.
func (q *RiverQueue) Insert(
	ctx context.Context,
	args river.JobArgs,
	opts *river.InsertOpts,
) (*rivertype.JobInsertResult, error) {
	return q.client.Insert(ctx, args, opts)
}

// InsertTx adds a txn job to the queue.
func (q *RiverQueue) InsertTx(
	ctx context.Context,
	tx pgx.Tx,
	args river.JobArgs,
	opts *river.InsertOpts,
) (*rivertype.JobInsertResult, error) {
	return q.client.InsertTx(ctx, tx, args, opts)
}
