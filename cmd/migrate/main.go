package main

import (
	"context"
	"database/sql"
	"log"
	"os/signal"
	"syscall"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/ps-wizard/revserp/internal/config"
)

// main runs database migrations.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		log.Fatalf("config: %v", err)
	}

	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()

	if err := goose.SetDialect("postgres"); err != nil {
		log.Fatalf("set goose dialect: %v", err)
	}

	if err := goose.UpContext(ctx, db, "migrations"); err != nil {
		log.Fatalf("run migrations: %v", err)
	}

	log.Println("migrations applied successfully")
}
