package db

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/tracelog"
)

type Logger struct {
	l *slog.Logger
}

func NewDBLogger(logger *slog.Logger) *Logger {
	return &Logger{l: logger}
}

func (l *Logger) Log(ctx context.Context, level tracelog.LogLevel, msg string, data map[string]interface{}) {
	attrs := make([]slog.Attr, 0, len(data))
	for k, v := range data {
		attrs = append(attrs, slog.Any(k, v))
	}

	var lvl slog.Level

	switch level {
	case tracelog.LogLevelError:
		lvl = slog.LevelError
	case tracelog.LogLevelWarn:
		lvl = slog.LevelWarn
	case tracelog.LogLevelInfo:
		lvl = slog.LevelInfo
	case tracelog.LogLevelDebug:
		lvl = slog.LevelDebug
	case tracelog.LogLevelTrace:
		lvl = slog.LevelDebug - 1
	case tracelog.LogLevelNone:
		lvl = slog.LevelDebug - 2
	default:
		lvl = slog.LevelDebug
		attrs = append(attrs, slog.Any("INVALID_PGX_LOG_LEVEL", level))
	}

	l.l.LogAttrs(ctx, lvl, msg, attrs...)
}
