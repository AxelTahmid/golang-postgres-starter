package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/lmittmann/tint"

	"github.com/AxelTahmid/tinker/config"
	"github.com/AxelTahmid/tinker/internal/server"
	"github.com/AxelTahmid/tinker/pkg/buildinfo"
	"github.com/AxelTahmid/tinker/pkg/slogx"
)

func main() {
	ctx := context.Background()

	// Load configuration.
	conf, err := config.InitConfig()
	if err != nil {
		log.Fatalf("Parsing conf failed: %v", err)
	}

	// Setup logger: ERROR and above go to stderr, everything else to stdout,
	// so a process supervisor can capture the two streams into separate files.
	h := &slogx.ContextHandler{Handler: slogx.NewSplitLevelHandler(
		slog.NewJSONHandler(os.Stdout, nil),
		slog.NewJSONHandler(os.Stderr, nil),
		slog.LevelError,
	)}
	logger := slog.New(h)
	if !conf.Server.IsProduction() {
		tinted := tint.NewTextHandler(os.Stdout, &tint.Options{Level: slog.LevelInfo, TimeFormat: time.Kitchen})
		logger = slog.New(&slogx.ContextHandler{Handler: tinted})
	}
	// Set default logger.
	slog.SetDefault(logger)
	logger.InfoContext(ctx, "server: starting", "env", conf.Server.AppEnv, "version", buildinfo.Version)

	app, err := server.Bootstrap(ctx, conf, logger)
	if err != nil {
		log.Fatalf("bootstrap failed: %v", err)
	}

	// Binary-specific step: this is the process that actually works the queue.
	if err = app.DB.StartQueue(ctx, &conf.Server); err != nil {
		app.DB.Close()
		log.Fatalf("Failed to start river queue: %v", err)
	}

	app.Server.Start(ctx)
}
