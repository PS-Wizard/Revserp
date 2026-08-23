package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/ps-wizard/revserp/internal/config"
	"github.com/ps-wizard/revserp/internal/crawler"
	internaldb "github.com/ps-wizard/revserp/internal/db"
	"github.com/ps-wizard/revserp/internal/worker"
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

	renderer := crawler.NewRenderer(cfg.ObscuraPath, cfg.RendererConcurrency, cfg.ObscuraTimeout, cfg.ObscuraKillTimeout)
	crawlWorker := worker.New(dbPool, worker.Config{
		ManualConcurrency:     cfg.WorkerConcurrency,
		ManualPoll:            cfg.WorkerPollInterval,
		AutoConcurrency:       cfg.AutoCrawlConcurrency,
		AutoPoll:              cfg.AutoCrawlPollInterval,
		AutoSchedulerInterval: cfg.AutoCrawlSchedulerInterval,
		CrawlPageWorkerCount:  cfg.CrawlPageWorkerCount,
		PageSpeedAPIKey:       cfg.PageSpeedAPIKey,
		CrawlMaxRetries:       cfg.CrawlMaxRetries,
		CrawlRetryBase:        cfg.CrawlRetryBase,
		CrawlRetryMax:         cfg.CrawlRetryMax,
		CrawlTimeout:          cfg.CrawlTimeout,
		MaxAPIResponseBytes:   cfg.MaxAPIResponseBytes,
	}, renderer)
	log.Printf("worker listening for queued crawls: manual_concurrency=%d manual_poll=%s auto_concurrency=%d auto_poll=%s auto_scheduler=%s crawl_page_worker_count=%d renderer_concurrency=%d obscura_path=%q obscura_timeout=%s obscura_kill_timeout=%s crawl_max_retries=%d crawl_retry_base=%s crawl_retry_max=%s crawl_timeout=%s max_api_response_bytes=%d", cfg.WorkerConcurrency, cfg.WorkerPollInterval, cfg.AutoCrawlConcurrency, cfg.AutoCrawlPollInterval, cfg.AutoCrawlSchedulerInterval, cfg.CrawlPageWorkerCount, cfg.RendererConcurrency, cfg.ObscuraPath, cfg.ObscuraTimeout, cfg.ObscuraKillTimeout, cfg.CrawlMaxRetries, cfg.CrawlRetryBase, cfg.CrawlRetryMax, cfg.CrawlTimeout, cfg.MaxAPIResponseBytes)
	if err := crawlWorker.Run(ctx); err != nil {
		log.Printf("worker error: %v", err)
	}
	log.Printf("worker shutting down")
	return nil
}
