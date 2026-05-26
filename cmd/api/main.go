package main

import (
	"context"
	"log"
	"net/http"

	"github.com/ps-wizard/revserp/internal/app"
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

	application := app.New(cfg, dbPool)

	server := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: application.Router(),
	}

	log.Printf("api listening on %s", cfg.HTTPAddr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("start server: %v", err)
	}
}
