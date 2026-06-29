package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/ps-wizard/revserp/internal/aiaudit"
	"github.com/ps-wizard/revserp/internal/config"
	internaldb "github.com/ps-wizard/revserp/internal/db"
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

	dbPool, err := internaldb.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer dbPool.Close()

	log.Printf("ai audit worker starting: concurrency=%d poll=%s", cfg.AIAuditWorkerConcurrency, cfg.AIAuditWorkerPollInterval)

	worker := aiaudit.New(dbPool, cfg, cfg.AIAuditWorkerConcurrency, cfg.AIAuditWorkerPollInterval)
	if err := worker.Run(ctx); err != nil {
		log.Printf("ai audit worker error: %v", err)
	}

	log.Printf("ai audit worker shut down")
	return nil
}
