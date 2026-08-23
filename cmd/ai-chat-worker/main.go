package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/aichatworker"
	"github.com/ps-wizard/revserp/internal/config"
	internaldb "github.com/ps-wizard/revserp/internal/db"
	"github.com/ps-wizard/revserp/internal/gsc"
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

	if strings.TrimSpace(cfg.DeepSeekAPIKey) == "" {
		return fmt.Errorf("DEEPSEEK_API_KEY is required for ai chat worker")
	}
	pool, err := internaldb.Connect(ctx, cfg.DatabaseURL, cfg.DBStatementTimeout, cfg.DBLockTimeout)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()

	provider := ai.NewDeepSeekClient(cfg.DeepSeekAPIKey, cfg.DeepSeekModel, cfg.DeepSeekBaseURL, nil)
	workerID := fmt.Sprintf("%s-%d", hostname(), os.Getpid())
	worker := aichatworker.New(pool, provider, aichatworker.Config{
		ID:           workerID,
		Concurrency:  cfg.AIChatWorkerConcurrency,
		PollInterval: cfg.AIChatWorkerPollInterval,
		TurnTimeout:  cfg.AITurnTimeout,
	})
	worker.GSC = gsc.NewService(cfg.GoogleClientID, cfg.GoogleClientSecret, cfg.GoogleRedirectURL, cfg.GoogleTokenEncryptionSecret, cfg.MaxAPIResponseBytes)
	log.Printf("ai chat worker starting: worker_id=%s concurrency=%d poll=%s turn_timeout=%s model=%s", workerID, cfg.AIChatWorkerConcurrency, cfg.AIChatWorkerPollInterval, cfg.AITurnTimeout, cfg.DeepSeekModel)
	if err := worker.Run(ctx); err != nil {
		return fmt.Errorf("run ai chat worker: %w", err)
	}
	log.Printf("ai chat worker shut down: worker_id=%s", workerID)
	return nil
}

func hostname() string {
	h, e := os.Hostname()
	if e != nil {
		return "ai-chat"
	}
	return h
}
