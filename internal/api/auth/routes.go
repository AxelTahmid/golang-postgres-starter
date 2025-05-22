package auth

import (
	"github.com/go-chi/chi/v5"

	"github.com/AxelTahmid/tinker/internal/jwt"
)

func Routes(h *Handler) chi.Router {
	r := chi.NewRouter()

	r.Post("/login", h.Login)
	r.Post("/register", h.Register)

	r.With(jwt.AdminOnly).Post("/register/admin", h.RegisterAdmin)

	r.With(jwt.Authenticated).Get("/me", h.Me)
	r.With(jwt.RefreshFlow).Get("/refresh", h.Refresh)

	return r
}
