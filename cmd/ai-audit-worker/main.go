package main

import (
	"context"
	"log"

	"github.com/ps-wizard/revserp/internal/aiaudit"
	"github.com/ps-wizard/revserp/internal/config"
	internaldb "github.com/ps-wizard/revserp/internal/db"
)

// main starts the AI audit worker process.
func main() {
	ctx := context.Background()
	cfg := config.Load()

	dbPool, err := internaldb.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("connect database: %v", err)
	}
	defer dbPool.Close()

	aiAuditWorker := aiaudit.New(dbPool, cfg.AIAuditWorkerConcurrency, cfg.AIAuditWorkerPollInterval)
	log.Printf("ai audit worker listening for queued audits: concurrency=%d poll_interval=%s", cfg.AIAuditWorkerConcurrency, cfg.AIAuditWorkerPollInterval)
	if err := aiAuditWorker.Run(ctx); err != nil {
		log.Fatalf("run ai audit worker: %v", err)
	}
}
