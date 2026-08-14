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
	pool, err := internaldb.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()

	provider := ai.NewDeepSeekClient(cfg.DeepSeekAPIKey, cfg.DeepSeekModel, cfg.DeepSeekBaseURL, nil)
	worker := aichatworker.New(pool, provider, aichatworker.Config{
		ID:           fmt.Sprintf("%s-%d", hostname(), os.Getpid()),
		Concurrency:  cfg.AIChatWorkerConcurrency,
		PollInterval: cfg.AIChatWorkerPollInterval,
		TurnTimeout:  cfg.AITurnTimeout,
	})
	return worker.Run(ctx)
}

func hostname() string {
	h, e := os.Hostname()
	if e != nil {
		return "ai-chat"
	}
	return h
}
