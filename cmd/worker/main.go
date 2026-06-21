package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"

	"github.com/ps-wizard/revserp/internal/config"
	"github.com/ps-wizard/revserp/internal/crawler"
	internaldb "github.com/ps-wizard/revserp/internal/db"
	"github.com/ps-wizard/revserp/internal/worker"
)

// main starts the crawl worker process.
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

	renderer := crawler.NewRenderer(cfg.ObscuraPath, cfg.RendererConcurrency, cfg.ObscuraTimeout, cfg.ObscuraKillTimeout)
	crawlWorker := worker.New(dbPool, cfg.WorkerConcurrency, cfg.WorkerPollInterval, cfg.CrawlPageWorkerCount, renderer, cfg.PageSpeedAPIKey)
	log.Printf("worker listening for queued crawls: concurrency=%d poll_interval=%s crawl_page_worker_count=%d renderer_concurrency=%d obscura_path=%q obscura_timeout=%s obscura_kill_timeout=%s", cfg.WorkerConcurrency, cfg.WorkerPollInterval, cfg.CrawlPageWorkerCount, cfg.RendererConcurrency, cfg.ObscuraPath, cfg.ObscuraTimeout, cfg.ObscuraKillTimeout)
	if err := crawlWorker.Run(ctx); err != nil {
		log.Printf("worker error: %v", err)
	}
	log.Printf("worker shutting down")
}
