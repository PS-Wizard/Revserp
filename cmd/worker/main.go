package main

import (
	"context"
	"log"

	"github.com/ps-wizard/revserp/internal/config"
	internaldb "github.com/ps-wizard/revserp/internal/db"
	"github.com/ps-wizard/revserp/internal/worker"
)

// main starts the crawl worker process.
func main() {
	ctx := context.Background()
	cfg := config.Load()

	dbPool, err := internaldb.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("connect database: %v", err)
	}
	defer dbPool.Close()

	crawlWorker := worker.New(dbPool, cfg.WorkerConcurrency, cfg.WorkerPollInterval)
	log.Printf("worker listening for queued crawls: concurrency=%d poll_interval=%s", cfg.WorkerConcurrency, cfg.WorkerPollInterval)
	if err := crawlWorker.Run(ctx); err != nil {
		log.Fatalf("run worker: %v", err)
	}
}
