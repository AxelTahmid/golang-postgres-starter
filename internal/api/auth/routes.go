package auth

import (
	"github.com/go-chi/chi/v5"

	"github.com/AxelTahmid/tinker/internal/jwt"
)

func Routes(h *Handler) chi.Router {
	r := chi.NewRouter()

	r.Post("/login", h.login)
	r.Post("/register", h.register)

	r.With(jwt.Authenticated).Get("/me", h.me)
	r.With(jwt.RefreshFlow).Get("/refresh", h.refresh)

	return r
}
