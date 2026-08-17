package server

import (
	"net/http"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/AxelTahmid/tinker/internal/api/auth"
	"github.com/AxelTahmid/tinker/internal/clientip"
	"github.com/AxelTahmid/tinker/internal/httpx"
	"github.com/AxelTahmid/tinker/internal/middleware"
)

// Routes declares the complete API tree. Build compiles request binding,
// validation, response writing, errors, guards, routing, and OpenAPI from the
// same immutable operation plans.
func (s *Server) Routes() *httpx.Group {
	authHandler := auth.NewHandler(auth.NewService(auth.NewRepository(s.db)))
	return routeTree(authHandler)
}

func routeTree(authHandler *auth.Handler) *httpx.Group {
	root := httpx.NewGroup(httpx.Defaults{})
	root.HandleInfra(http.MethodGet, "/metrics", promhttp.Handler())
	root.HandleInfra(http.MethodGet, "/docs/json", http.HandlerFunc(openAPIJSON))
	root.HandleInfra(http.MethodGet, "/docs/ui", http.HandlerFunc(openAPIUI))

	v1 := root.Sub("/api/v1")
	v1.Mount("/auth", authHandler.Routes())
	return root
}

func (s *Server) BuildHandler() (http.Handler, error) {
	app, err := s.Routes().Build()
	if err != nil {
		return nil, err
	}
	return s.wrapTransport(app), nil
}

func (s *Server) wrapTransport(app http.Handler) http.Handler {
	clientAddr := clientip.NewResolver(s.conf.Server.TrustedProxyPrefixes())
	middlewares := []func(http.Handler) http.Handler{
		clientAddr.Middleware,
		chimiddleware.RequestID,
		middleware.Logger(s.log),
		middleware.Recovery,
		cors.Handler(s.conf.Cors.CorsOptions()),
		s.conf.Secure.SecureOptions().Handler,
		chimiddleware.Heartbeat("/ping"),
	}
	handler := app
	for i := len(middlewares) - 1; i >= 0; i-- {
		handler = middlewares[i](handler)
	}
	return handler
}

// RouterForDocs builds the real route declarations over inert handler
// dependencies. No request is executed, so generation requires no env, keys,
// database, or network.
func RouterForDocs() *httpx.Application {
	if err := httpx.InitValidator(); err != nil {
		panic(err)
	}
	return routeTree(auth.NewHandler(nil)).MustBuild()
}
