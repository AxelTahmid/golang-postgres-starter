package slogx

import (
	"context"
	"log/slog"
)

// SplitLevelHandler routes records at or above threshold to the high handler
// and everything below it to the low handler. It exists so ERROR records can
// go to stderr while INFO/WARN stay on stdout, letting a process supervisor
// capture the two streams into separate files.
type SplitLevelHandler struct {
	low       slog.Handler
	high      slog.Handler
	threshold slog.Level
}

// NewSplitLevelHandler builds a handler that sends records below threshold to
// low and everything at or above it to high.
func NewSplitLevelHandler(low, high slog.Handler, threshold slog.Level) *SplitLevelHandler {
	return &SplitLevelHandler{low: low, high: high, threshold: threshold}
}

func (h *SplitLevelHandler) handlerFor(level slog.Level) slog.Handler {
	if level >= h.threshold {
		return h.high
	}
	return h.low
}

func (h *SplitLevelHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.handlerFor(level).Enabled(ctx, level)
}

func (h *SplitLevelHandler) Handle(ctx context.Context, r slog.Record) error {
	return h.handlerFor(r.Level).Handle(ctx, r)
}

func (h *SplitLevelHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &SplitLevelHandler{
		low:       h.low.WithAttrs(attrs),
		high:      h.high.WithAttrs(attrs),
		threshold: h.threshold,
	}
}

func (h *SplitLevelHandler) WithGroup(name string) slog.Handler {
	return &SplitLevelHandler{
		low:       h.low.WithGroup(name),
		high:      h.high.WithGroup(name),
		threshold: h.threshold,
	}
}
