package middleware

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/AxelTahmid/tinker/internal/utils"
)

// Logger middleware creates a handler that logs information about each HTTP request.
// It logs when a request starts and completes, including details like request ID,
// method, path, status code, latency, and more.
func Logger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		fn := func(rw http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(rw, r.ProtoMajor)
			start := time.Now()

			// Extract request ID, defaulting to "unknown" if not present or not a string
			reqID := r.Context().Value(middleware.RequestIDKey)
			reqIDStr, ok := reqID.(string)
			if !ok {
				reqIDStr = "unknown"
			}

			// Common attributes used in both log entries
			commonAttrs := []slog.Attr{
				slog.String("request-id", reqIDStr),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("ip", r.RemoteAddr),
				slog.String("host", r.Host),
				slog.String("query", r.URL.RawQuery),
				slog.String("user-agent", r.UserAgent()),
			}

			ctx := utils.AppendCtx(r.Context(), commonAttrs...)

			// ContextHandler injects the common attributes into records logged
			// with this context, so do not pass them a second time.
			logger.LogAttrs(ctx, slog.LevelInfo, "request started")

			// Defer the completion log
			defer func() {
				// Log request completion
				logger.LogAttrs(
					ctx,
					slog.LevelInfo,
					"request completed",
					slog.Int("status", ww.Status()),
					slog.Int("bytes", ww.BytesWritten()),
					slog.Int64("latency-ms", time.Since(start).Milliseconds()),
				)
			}()

			// Add request ID to context
			// Process the request
			next.ServeHTTP(ww, r.WithContext(ctx))
		}
		return http.HandlerFunc(fn)
	}
}
