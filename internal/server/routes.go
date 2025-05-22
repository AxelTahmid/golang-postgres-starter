package server

import (
	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/AxelTahmid/tinker/internal/middleware"

	"github.com/AxelTahmid/tinker/internal/api/auth"
)

func (s *Server) routes() {
	// global middlewares
	s.router.Use(
		chimiddleware.RealIP,
		chimiddleware.RequestID,
		middleware.Logger(s.log),
		middleware.Recovery,
		chimiddleware.AllowContentType("application/json"),
		cors.Handler(s.conf.Cors.CorsOptions()),
		s.conf.Secure.SecureOptions().Handler,
		chimiddleware.GetHead,
		chimiddleware.Heartbeat("/ping"),
	)

	// Initialize handlers
	authHandler := auth.NewHandler(auth.NewService(auth.NewRepository(s.db)))

	// routes
	s.router.Handle("/metrics", promhttp.Handler())

	s.router.Route("/api/v1", func(r chi.Router) {
		r.Mount("/auth", auth.Routes(authHandler))
	})
}
