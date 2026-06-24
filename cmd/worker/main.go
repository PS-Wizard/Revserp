package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ps-wizard/revserp/internal/config"
	"github.com/ps-wizard/revserp/internal/crawler"
	internaldb "github.com/ps-wizard/revserp/internal/db"
	"github.com/ps-wizard/revserp/internal/worker"
)

// drainTimeout is the maximum time we allow in-flight crawls to complete after
// a SIGINT/SIGTERM is received. Workers stop claiming NEW crawls immediately on
// signal; this window lets already-running runCrawl calls finish naturally.
// Crawls that do not complete within drainTimeout are abandoned with their DB
// rows left in 'running'. A future restart or a separate cleanup job must reset
// those rows (marking them failed). Full automated cleanup is deferred — it
// requires a sqlc query not owned by this file.
const drainTimeout = 10 * time.Minute

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run() error {
	// claimCtx is canceled the moment the first SIGINT/SIGTERM arrives.
	// Workers use it to stop claiming NEW crawls.
	claimCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	dbPool, err := internaldb.Connect(claimCtx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer dbPool.Close()

	renderer := crawler.NewRenderer(cfg.ObscuraPath, cfg.RendererConcurrency, cfg.ObscuraTimeout, cfg.ObscuraKillTimeout)
	crawlWorker := worker.New(dbPool, cfg.WorkerConcurrency, cfg.WorkerPollInterval, cfg.CrawlPageWorkerCount, renderer, cfg.PageSpeedAPIKey)
	log.Printf("worker listening for queued crawls: concurrency=%d poll_interval=%s crawl_page_worker_count=%d renderer_concurrency=%d obscura_path=%q obscura_timeout=%s obscura_kill_timeout=%s", cfg.WorkerConcurrency, cfg.WorkerPollInterval, cfg.CrawlPageWorkerCount, cfg.RendererConcurrency, cfg.ObscuraPath, cfg.ObscuraTimeout, cfg.ObscuraKillTimeout)

	// drainCtx is separate from claimCtx: it is NOT canceled on signal, only
	// after drainTimeout elapses. In-flight runCrawl calls use drainCtx as
	// their parent so they can complete after the claim loop has been stopped.
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), drainTimeout)
	defer cancelDrain()

	// RunWithDrain stops claiming new work when claimCtx is canceled (signal)
	// and lets in-flight crawls run until drainCtx expires or they finish.
	// It blocks until all worker goroutines have fully returned.
	if err := crawlWorker.RunWithDrain(claimCtx, drainCtx); err != nil {
		log.Printf("worker error: %v", err)
	}

	log.Printf("worker shutting down")
	return nil
}
