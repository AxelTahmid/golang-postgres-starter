package middleware

import (
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"

	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/AxelTahmid/tinker/internal/httpx"
)

// Recovery converts panics to a sanitized problem response. Request bodies
// are deliberately never dumped because they can contain credentials.
func Recovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil && recovered != http.ErrAbortHandler {
				slog.ErrorContext(r.Context(), "panic recovered",
					"panic", recovered,
					"stack", string(debug.Stack()),
					"method", r.Method,
					"path", r.URL.Path,
					"request_id", chimiddleware.GetReqID(r.Context()),
				)
				httpx.Error(w, r, errors.New("panic recovered"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}
