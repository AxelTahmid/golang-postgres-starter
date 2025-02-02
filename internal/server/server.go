package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/go-chi/chi/v5"

	"github.com/AxelTahmid/tinker/config"
	"github.com/AxelTahmid/tinker/internal/db"
)

type Server struct {
	conf   *config.Config
	router *chi.Mux
	db     db.Postgres
	log    *slog.Logger
}

func NewServer(c *config.Config, db db.Postgres, l *slog.Logger) *Server {

	server := &Server{
		conf:   c,
		router: chi.NewRouter(),
		db:     db,
		log:    l,
	}

	server.routes()

	return server
}

func (s *Server) Start(ctx context.Context) {
	loggerLevel := slog.LevelDebug

	if s.conf.AppEnv == "production" {
		loggerLevel = slog.LevelWarn
	}

	logger := slog.NewLogLogger(slog.NewJSONHandler(os.Stdout, nil), loggerLevel)

	server := http.Server{
		Addr:         fmt.Sprintf(":%d", s.conf.Port),
		Handler:      s.router,
		TLSConfig:    s.conf.TLSOptions(),
		IdleTimeout:  s.conf.IdleTimeout,
		ReadTimeout:  s.conf.ReadTimeout,
		WriteTimeout: s.conf.WriteTimeout,
		ErrorLog:     logger,
	}

	shutdownComplete := handleShutdown(func() {
		log.Println("Starting server shutdown...")

		s.db.Close()
		log.Println("Closed database pool...")

		err := server.Shutdown(ctx)
		if err != nil {
			log.Printf("server.Shutdown failed: %v\n", err)
		}
		log.Println("Server shutdown gracefully")
	})

	log.Printf("Starting server on port %d\n", s.conf.Server.Port)
	err := server.ListenAndServeTLS("", "")

	if errors.Is(err, http.ErrServerClosed) {
		<-shutdownComplete
	} else {
		log.Printf("http.ListenAndServe failed: %v\n", err)
	}
}

func handleShutdown(onShutdownSignal func()) <-chan struct{} {
	shutdown := make(chan struct{})

	go func() {
		shutdownSignal := make(chan os.Signal, 1)
		signal.Notify(shutdownSignal, os.Interrupt, syscall.SIGTERM)

		<-shutdownSignal

		onShutdownSignal()
		close(shutdown)
	}()

	return shutdown
}
