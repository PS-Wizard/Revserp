.PHONY: up down sqlc migrate api

up:
	podman-compose up -d

down:
	podman-compose down

sqlc:
	sqlc generate

migrate:
	go run ./cmd/migrate

api:
	go run ./cmd/api
