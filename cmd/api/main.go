package main

import (
	"context"
	"log"
	"net/http"

	"github.com/ps-wizard/revserp/internal/app"
	internalauth "github.com/ps-wizard/revserp/internal/auth"
	"github.com/ps-wizard/revserp/internal/config"
	internaldb "github.com/ps-wizard/revserp/internal/db"
)

// main starts the API server.
func main() {
	ctx := context.Background()
	cfg := config.Load()

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

	log.Printf("api listening on %s", cfg.HTTPAddr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("start server: %v", err)
	}
}
