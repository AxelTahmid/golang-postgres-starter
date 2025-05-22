package db

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Domain-specific error types that clients can check with errors.Is.
var (
	ErrNotFound            = errors.New("resource not found")
	ErrAlreadyExists       = errors.New("resource already exists")
	ErrForeignKeyViolation = errors.New("invalid reference to another resource")
	ErrDatabaseError       = errors.New("database error")
)

// WrapDBError converts database errors into domain-specific errors.
func WrapDBError(ctx context.Context, err error) error {
	slog.ErrorContext(ctx, "db: error", slog.String("error", err.Error()))

	if err == nil {
		return nil
	}

	// Handle "not found" error.
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	}

	// Handle PostgreSQL-specific errors.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case pgerrcode.UniqueViolation:
			return fmt.Errorf("%w: %w", ErrAlreadyExists, err)
		case pgerrcode.ForeignKeyViolation:
			return fmt.Errorf("%w: %w", ErrForeignKeyViolation, err)
		default:
			return fmt.Errorf("%w: %w (code: %s)", ErrDatabaseError, err, pgErr.Code)
		}
	}

	// Handle any other errors.
	return fmt.Errorf("unhandled error: %w", err)
}

// IsNotFoundError checks if the error is a "not found" error.
func IsNotFoundError(err error) bool {
	return errors.Is(err, ErrNotFound) || errors.Is(err, pgx.ErrNoRows)
}

// IsAlreadyExistsError checks if the error is an "already exists" error.
func IsAlreadyExistsError(err error) bool {
	return errors.Is(err, ErrAlreadyExists)
}

// IsForeignKeyViolationError checks if the error is a foreign key violation.
func IsForeignKeyViolationError(err error) bool {
	return errors.Is(err, ErrForeignKeyViolation)
}

// IsDatabaseError checks if the error is a general database error.
func IsDatabaseError(err error) bool {
	return errors.Is(err, ErrDatabaseError)
}
