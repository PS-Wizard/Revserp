FROM golang:1.26-bookworm AS build

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 go build -o /out/api ./cmd/api \
	&& CGO_ENABLED=0 go build -o /out/worker ./cmd/worker \
	&& CGO_ENABLED=0 go build -o /out/migrate ./cmd/migrate

FROM debian:bookworm-slim

WORKDIR /app

RUN apt-get update \
	&& apt-get install -y --no-install-recommends ca-certificates \
	&& rm -rf /var/lib/apt/lists/*

COPY --from=build /out/api /app/api
COPY --from=build /out/worker /app/worker
COPY --from=build /out/migrate /app/migrate
COPY scripts/start-api.sh /app/start-api.sh
COPY scripts/start-worker.sh /app/start-worker.sh

RUN chmod +x /app/api /app/worker /app/migrate /app/start-api.sh /app/start-worker.sh

EXPOSE 8080

CMD ["/app/start-api.sh"]
