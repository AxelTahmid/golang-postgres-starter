package slogx

import (
	"context"
	"log/slog"

	"github.com/AxelTahmid/tinker/pkg/ctxkeys"
)

// SlogFields is the context key under which AppendCtx accumulates attributes.
// It is an alias of the shared constant so callers need only this package.
const SlogFields = ctxkeys.SlogFields

type ContextHandler struct {
	slog.Handler
}

// Handle adds contextual attributes to the Record before calling the underlying.
// handler
func (h ContextHandler) Handle(ctx context.Context, r slog.Record) error {
	if attrs, ok := ctx.Value(SlogFields).([]slog.Attr); ok {
		for _, v := range attrs {
			r.AddAttrs(v)
		}
	}

	return h.Handler.Handle(ctx, r)
}

// AppendCtx adds an slog attribute to the provided context so that it will be
// included in any Record created with such context
// func AppendCtx(parent context.Context, attr slog.Attr) context.Context {
// 	if parent == nil {
// 		parent = context.Background()
// 	}

// 	if v, ok := parent.Value(SlogFields).([]slog.Attr); ok {
// 		v = append(v, attr)
// 		return context.WithValue(parent, SlogFields, v)
// 	}

// 	v := []slog.Attr{}
// 	v = append(v, attr)
// 	return context.WithValue(parent, SlogFields, v)
// }

// AppendCtx adds one or more slog attributes to the provided context so that they will.
// be included in any Record created with such context.
func AppendCtx(parent context.Context, attrs ...slog.Attr) context.Context {
	if parent == nil {
		parent = context.Background()
	}

	if len(attrs) == 0 {
		return parent
	}

	var v []slog.Attr

	if existingAttrs, ok := parent.Value(SlogFields).([]slog.Attr); ok {
		// Start with existing attributes
		v = existingAttrs
	} else {
		// Initialize empty slice
		v = []slog.Attr{}
	}

	// Append all new attributes
	v = append(v, attrs...)

	return context.WithValue(parent, SlogFields, v)
}
