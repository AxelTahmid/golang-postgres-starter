package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/lmittmann/tint"

	"github.com/AxelTahmid/tinker/config"
	"github.com/AxelTahmid/tinker/internal/db"
	"github.com/AxelTahmid/tinker/internal/httpx"
	"github.com/AxelTahmid/tinker/internal/jwt"
	"github.com/AxelTahmid/tinker/internal/server"
	"github.com/AxelTahmid/tinker/internal/utils"
)

func main() {
	ctx := context.Background()

	// Load configuration
	conf, err := config.InitConfig()
	if err != nil {
		log.Fatalf("Parsing conf failed: %v", err)
	}

	// Setup logger
	h := &utils.ContextHandler{Handler: slog.NewJSONHandler(os.Stdout, nil)}
	logger := slog.New(h)
	if conf.Server.AppEnv != "production" {
		logger = slog.New(
			&utils.ContextHandler{Handler: tint.NewHandler(os.Stdout, &tint.Options{
				Level:      slog.LevelInfo,
				TimeFormat: time.Kitchen,
			})},
		)
	}
	// Set default logger
	slog.SetDefault(logger)

	database, err := db.New(ctx, &conf.DB, logger)
	if err != nil {
		log.Fatalf("Db Connection Failed: %v", err)
	}

	// Start the job queue
	if err = database.StartQueue(ctx, &conf.Server); err != nil {
		database.Close()
		log.Fatalf("Failed to start river queue: %v", err)
	}

	// Check if database connection is successful
	err = database.Ping(ctx)
	if err != nil {
		log.Fatalf("Error connecting to database: %v", err)
	}

	err = jwt.InitJWT(&conf.Jwt)
	if err != nil {
		log.Fatalf("failed to create jwt manager: %v", err)
	}

	// instantiate req parsing, serialization and validation
	err = httpx.InitValidator()
	if err != nil {
		log.Fatalf("failed to register custom validators: %v", err)
	}

	// Start server
	s := server.NewServer(conf, database, logger)
	s.Start(ctx)
}
