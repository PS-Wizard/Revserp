package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"

	"github.com/ps-wizard/revserp/internal/aiaudit"
	"github.com/ps-wizard/revserp/internal/config"
	internaldb "github.com/ps-wizard/revserp/internal/db"
)

// main starts the AI audit worker process.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		log.Fatalf("config: %v", err)
	}

	dbPool, err := internaldb.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("connect database: %v", err)
	}
	defer dbPool.Close()

	aiAuditWorker := aiaudit.New(dbPool, cfg.AIAuditWorkerConcurrency, cfg.AIAuditWorkerPollInterval)
	log.Printf("ai audit worker: queue processing not yet implemented, exiting")
	if err := aiAuditWorker.Run(ctx); err != nil {
		log.Printf("ai audit worker error: %v", err)
	}
	log.Printf("ai audit worker shutting down")
}
