package server

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	m "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/jwalton/gchalk"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/AxelTahmid/tinker/internal/db"
	mc "github.com/AxelTahmid/tinker/internal/middleware"
	"github.com/AxelTahmid/tinker/internal/utils/util"

	"github.com/AxelTahmid/tinker/internal/api/auth"
)

func (s *Server) routes() {
	// global middlewares
	s.router.Use(m.RealIP)
	s.router.Use(m.RequestID)
	s.router.Use(mc.Logger(s.log))
	s.router.Use(mc.Recovery)
	s.router.Use(m.AllowContentType("application/json"))
	s.router.Use(cors.Handler(s.conf.CorsOptions()))
	s.router.Use(s.conf.SecureOptions().Handler)
	s.router.Use(m.GetHead)
	s.router.Use(m.Heartbeat("/ping"))

	// Initialize handlers
	authHandler := auth.NewHandler(auth.NewService(auth.NewRepository(s.db)))

	// routes
	s.router.Handle("/metrics", promhttp.Handler())

	s.router.With(db.SetTenantContext(s.db.Queries())).Route("/api/v1", func(r chi.Router) {
		r.Mount("/auth", auth.Routes(authHandler))
	})
}

func (s *Server) PrintAllRegisteredRoutes(exceptions ...string) {
	exceptions = append(exceptions, "/metrics")

	walkFunc := func(method string, path string, handler http.Handler, middlewares ...func(http.Handler) http.Handler) error {
		for _, val := range exceptions {
			if strings.HasPrefix(path, val) {
				return nil
			}
		}

		switch method {
		case "GET":
			fmt.Printf("%s", gchalk.Green(fmt.Sprintf("%-8s", method)))
		case "POST":
			fmt.Printf("%s", gchalk.Blue(fmt.Sprintf("%-8s", method)))
		case "PUT", "PATCH":
			fmt.Printf("%s", gchalk.Yellow(fmt.Sprintf("%-8s", method)))
		case "DELETE":
			fmt.Printf("%s", gchalk.Red(fmt.Sprintf("%-8s", method)))
		default:
			fmt.Printf("%s", gchalk.White(fmt.Sprintf("%-8s", method)))
		}

		// fmt.Printf("%-25s %60s %d middlewares\n", path, util.GetHandler(util.GetModName(), handler), len(middlewares)-9)
		fmt.Printf("%s", util.StrPad(path, 25, "-", "RIGHT"))
		fmt.Printf("%s\n", util.StrPad(util.GetHandler(util.GetModName(), handler), 60, "-", "LEFT"))

		return nil
	}

	if err := chi.Walk(s.router, walkFunc); err != nil {
		fmt.Print(err)
	}
}
