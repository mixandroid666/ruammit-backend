# Ruammit backend — common dev tasks.
# Note: targets shell out to docker/sqlc/migrate; install those as needed.

.PHONY: help run worker build test vet tidy up down logs migrate-up migrate-down sqlc seed

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  %-14s %s\n", $$1, $$2}'

run: ## Run the API server
	go run ./cmd/api

worker: ## Run the background worker
	go run ./cmd/worker

build: ## Build both binaries into ./bin
	go build -o bin/api ./cmd/api
	go build -o bin/worker ./cmd/worker

test: ## Run tests
	go test ./...

vet: ## Run go vet
	go vet ./...

tidy: ## Tidy modules
	go mod tidy

up: ## Start postgres, redis, minio
	docker compose up -d

db-ui: ## Start Adminer web DB browser at http://localhost:8081
	docker compose --profile tools up -d adminer

down: ## Stop infra
	docker compose down

logs: ## Tail infra logs
	docker compose logs -f

migrate-up: ## Apply migrations (requires golang-migrate)
	migrate -path db/migrations -database "$(DATABASE_URL)" up

migrate-down: ## Roll back one migration
	migrate -path db/migrations -database "$(DATABASE_URL)" down 1

sqlc: ## Generate type-safe DB code (requires sqlc)
	sqlc generate

seed: ## Load demo feed data (idempotent; requires psql + a migrated DB)
	psql "$(DATABASE_URL)" -f db/seed/0001_demo_feed.sql
