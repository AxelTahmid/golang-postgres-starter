package middleware

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

func Logger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		fn := func(rw http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(rw, r.ProtoMajor)
			start := time.Now()

			reqID := r.Context().Value(middleware.RequestIDKey)

			reqIDStr, ok := reqID.(string)
			if !ok {
				logger.LogAttrs(
					r.Context(),
					slog.LevelInfo,
					"request started",
					slog.String("request-id", "unknown"),
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.String("ip", r.RemoteAddr),
				)
			}

			// "bytes", ww.BytesWritten(),

			defer func() {
				logger.LogAttrs(
					r.Context(),
					slog.LevelInfo,
					"request completed",
					slog.String("request-id", reqIDStr),
					slog.Int("status", ww.Status()),
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.String("query", r.URL.RawQuery),
					slog.String("ip", r.RemoteAddr),
					slog.String("user-agent", r.UserAgent()),
					slog.Duration("latency-ms", time.Since(start)),
				)
			}()

			next.ServeHTTP(ww, r)
		}
		return http.HandlerFunc(fn)
	}
}
