package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/ps-wizard/revserp/internal/aiaudit"
	"github.com/ps-wizard/revserp/internal/config"
	internaldb "github.com/ps-wizard/revserp/internal/db"
	"github.com/ps-wizard/revserp/internal/tinyfish"
)

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("config: %w", err)
	}

	dbPool, err := internaldb.Connect(ctx, cfg.DatabaseURL, cfg.DBStatementTimeout, cfg.DBLockTimeout)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer dbPool.Close()

	log.Printf("ai audit worker starting: concurrency=%d poll=%s", cfg.AIAuditWorkerConcurrency, cfg.AIAuditWorkerPollInterval)

	worker := aiaudit.New(dbPool, cfg, cfg.AIAuditWorkerConcurrency, cfg.AIAuditWorkerPollInterval)
	// The web tools stay unavailable when no key is set: the bootstrap agent
	// reports that as an ordinary state rather than failing.
	if strings.TrimSpace(cfg.TinyfishAPIKey) != "" {
		worker.Web = tinyfish.NewClient(cfg.TinyfishAPIKey, cfg.TinyfishSearchEndpoint, cfg.TinyfishFetchEndpoint, 0)
	}
	if err := worker.Run(ctx); err != nil {
		log.Printf("ai audit worker error: %v", err)
	}

	log.Printf("ai audit worker shut down")
	return nil
}
