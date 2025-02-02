package db

import (
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

func Text(v *string) pgtype.Text {
	if v == nil {
		return pgtype.Text{
			String: "",
			Valid:  false,
		}
	}
	return pgtype.Text{
		String: *v,
		Valid:  true,
	}
}

func Time(v *time.Time) pgtype.Timestamptz {
	if v == nil {
		return pgtype.Timestamptz{
			Time:             time.Time{},
			InfinityModifier: 0,
			Valid:            false,
		}
	}
	return pgtype.Timestamptz{
		Time:             *v,
		InfinityModifier: 0,
		Valid:            true,
	}
}

func LogPgErr(log *slog.Logger, err error, context map[string]any) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("item not found")
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		// Attach additional error details to the log
		l := log.With(
			"code", pgErr.Code,
			"message", pgErr.Message,
			"detail", pgErr.Detail,
			"hint", pgErr.Hint,
		)

		// Map PostgreSQL error codes to user-friendly messages
		switch pgErr.Code {
		case pgerrcode.UniqueViolation:
			l.Error("unique violation")
			return errors.New("resource already exists")
		case pgerrcode.ForeignKeyViolation:
			l.Error("foreign key violation")
			return errors.New("invalid foreign key reference")
		default:
			l.Error("unexpected database error")
			return errors.New("unexpected database error")
		}
	}

	// Log generic non-PostgreSQL errors
	log.With(context).Error("unexpected error", "error", err.Error())
	return errors.New("unexpected error ocurred")
}
