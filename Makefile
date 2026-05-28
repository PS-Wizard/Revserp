.PHONY: up down sqlc migrate api migen

up:
	podman-compose up -d

down:
	pids=$$(ss -ltnp '( sport = :8080 )' 2>/dev/null | grep -o 'pid=[0-9]*' | cut -d= -f2 | sort -u); \
	if [ -n "$$pids" ]; then kill $$pids || true; fi
	podman-compose down

sqlc:
	sqlc generate

migrate:
	go run ./cmd/migrate

api:
	go run ./cmd/api

migen: migrate sqlc
