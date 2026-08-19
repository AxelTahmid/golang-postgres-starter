// Package ctxkeys defines the shared context key constants used across the
// pkg/ and internal/ layers. Keeping them in one leaf package prevents import
// cycles where a pkg/ package would otherwise have to import internal/, and
// keeps the full set of request-scoped values greppable in one place.
package ctxkeys

// ContextKey is the type used for every context value in this project. Using
// a named type rather than a bare string prevents key collisions with
// third-party libraries that store values on the same context.
type ContextKey string

const (
	// AuthCtxKey holds the parsed JWT claims published by the bearer guards.
	// Handlers read identity from here rather than from client-supplied
	// request fields, which can be spoofed.
	AuthCtxKey ContextKey = "ctx:auth-user"

	// SlogFields holds the []slog.Attr accumulated by slogx.AppendCtx and
	// replayed onto every record logged with the context.
	SlogFields ContextKey = "ctx:slog-fields"
)
