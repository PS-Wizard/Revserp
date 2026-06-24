package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ps-wizard/revserp/internal/app"
	internalauth "github.com/ps-wizard/revserp/internal/auth"
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

	authVerifier, err := internalauth.NewVerifier(ctx, cfg.AuthProvider, cfg.SupabaseJWTIssuer, cfg.SupabaseJWKSURL, cfg.SupabaseJWTAudience)
	if err != nil {
		return fmt.Errorf("initialize auth verifier: %w", err)
	}

	application, err := app.New(cfg, dbPool, authVerifier)
	if err != nil {
		return fmt.Errorf("initialize application: %w", err)
	}

	server := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: application.Router(),
	}

	go func() {
		<-ctx.Done()
		log.Printf("api server shutting down...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("api server shutdown error: %v", err)
		}
	}()

	log.Printf("api listening on %s", cfg.HTTPAddr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("start server: %w", err)
	}
	return nil
}
