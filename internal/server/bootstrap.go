package server

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/AxelTahmid/tinker/config"
	"github.com/AxelTahmid/tinker/internal/db"
	"github.com/AxelTahmid/tinker/internal/httpx"
	"github.com/AxelTahmid/tinker/internal/jwt"
)

// BootstrapResult exposes the shared application dependencies so each entry
// point can run its binary-specific steps — starting the job queue, writing
// the OpenAPI document — after construction.
type BootstrapResult struct {
	Server *Server
	DB     db.DB
}

// Bootstrap wires the full application exactly once for every binary:
// database, JWT service, request validators, and the HTTP server. Keeping the
// wiring in one place is what stops the entry points drifting apart — before
// this existed, cmd/api and the docs router each initialized validators on
// their own, and nothing forced the two paths to stay in step.
//
// The job queue is deliberately NOT started here. Construction is shared;
// deciding to process jobs is the API binary's business, and the docs
// generator must never start a worker.
func Bootstrap(ctx context.Context, conf *config.Config, logger *slog.Logger) (*BootstrapResult, error) {
	database, err := db.New(ctx, &conf.DB, logger)
	if err != nil {
		return nil, fmt.Errorf("initializing database: %w", err)
	}

	if err = database.Ping(ctx); err != nil {
		database.Close()
		return nil, fmt.Errorf("pinging database: %w", err)
	}

	if err = jwt.InitJWT(&conf.Jwt); err != nil {
		database.Close()
		return nil, fmt.Errorf("initializing jwt service: %w", err)
	}

	if err = httpx.InitValidator(); err != nil {
		database.Close()
		return nil, fmt.Errorf("registering validators: %w", err)
	}

	return &BootstrapResult{
		Server: NewServer(conf, database, logger),
		DB:     database,
	}, nil
}
