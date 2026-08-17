package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/AxelTahmid/tinker/config"
	"github.com/AxelTahmid/tinker/internal/db"
)

type Server struct {
	conf *config.Config
	db   db.DB
	log  *slog.Logger
}

func NewServer(conf *config.Config, database db.DB, logger *slog.Logger) *Server {
	return &Server{conf: conf, db: database, log: logger}
}

func (s *Server) Start(ctx context.Context) {
	handler, err := s.BuildHandler()
	if err != nil {
		s.log.ErrorContext(ctx, "failed to build HTTP application", "error", err)
		return
	}

	loggerLevel := slog.LevelDebug
	if s.conf.Server.AppEnv == "production" {
		loggerLevel = slog.LevelWarn
	}
	server := http.Server{
		Addr:         fmt.Sprintf(":%d", s.conf.Server.Port),
		Handler:      handler,
		TLSConfig:    s.conf.Server.TLSOptions(),
		IdleTimeout:  s.conf.Server.IdleTimeout,
		ReadTimeout:  s.conf.Server.ReadTimeout,
		WriteTimeout: s.conf.Server.WriteTimeout,
		ErrorLog:     slog.NewLogLogger(s.log.Handler(), loggerLevel),
	}

	shutdownComplete := handleShutdown(func() {
		s.log.InfoContext(ctx, "starting server shutdown")
		if err := s.db.StopQueue(ctx); err != nil {
			s.log.ErrorContext(ctx, "failed to stop job queue", "error", err)
		}
		s.db.Close()
		if err := server.Shutdown(ctx); err != nil {
			s.log.ErrorContext(ctx, "server shutdown failed", "error", err)
		}
	})

	s.log.InfoContext(ctx, "server starting", "port", s.conf.Server.Port)
	err = server.ListenAndServeTLS("", "")
	if errors.Is(err, http.ErrServerClosed) {
		<-shutdownComplete
		return
	}
	s.log.ErrorContext(ctx, "HTTP server failed", "error", err)
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
