package main

import (
	"context"
"log"
"net/http"
"os/signal"
"syscall"
	"time"

	"github.com/ps-wizard/revserp/internal/app"
	internalauth "github.com/ps-wizard/revserp/internal/auth"
	"github.com/ps-wizard/revserp/internal/config"
	internaldb "github.com/ps-wizard/revserp/internal/db"
)

// main starts the API server.
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

	authVerifier, err := internalauth.NewVerifier(ctx, cfg.AuthProvider, cfg.SupabaseJWTIssuer, cfg.SupabaseJWKSURL, cfg.SupabaseJWTAudience)
	if err != nil {
		log.Fatalf("initialize auth verifier: %v", err)
	}

	application := app.New(cfg, dbPool, authVerifier)

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
		log.Fatalf("start server: %v", err)
	}
}
