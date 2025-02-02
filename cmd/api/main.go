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
	"github.com/AxelTahmid/tinker/internal/jwt"
	"github.com/AxelTahmid/tinker/internal/server"
)

func main() {
	ctx := context.Background()

	// Load configuration
	conf := config.NewConfig()

	// Setup logger
	var logger *slog.Logger

	if conf.AppEnv != "production" {
		logger = slog.New(
			tint.NewHandler(os.Stdout, &tint.Options{
				Level:      slog.LevelDebug,
				TimeFormat: time.Kitchen,
			}),
		)
	} else {
		logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))
	}
	// Set default logger
	slog.SetDefault(logger)

	// Connect to database
	pg, err := db.NewPostgres(ctx, conf.Database, logger)
	if err != nil {
		log.Fatalf("Db Connection Failed: %v", err)
	}

	// Check if database connection is successful
	// ctx = context.WithValue(ctx, ctxkeys.TenantID, nil)
	err = pg.Ping(ctx)
	if err != nil {
		log.Fatalf("Error connecting to database: %v", err)
	}

	err = jwt.NewJWT(&conf.Jwt)
	if err != nil {
		log.Fatalf("failed to create jwt manager: %v", err)
	}

	// Start server
	s := server.NewServer(conf, pg, logger)
	s.Start(ctx)
}
