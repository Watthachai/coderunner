# CRN Makefile. Targets are thin wrappers around the canonical commands so the
# README stays short. Override DATABASE_URL etc. via env or a .env you source.

# Default local DSN matching docker-compose.yml.
DATABASE_URL ?= postgres://crn:crn_dev_password@localhost:5432/crn?sslmode=disable
MIGRATIONS_DIR ?= migrations

.PHONY: help build run vet tidy test migrate db-up db-down fmt frontend-dev

help: ## Show this help.
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

build: ## Compile the Go backend to ./bin/crn-server.
	go build -o bin/crn-server ./cmd/server

run: ## Run the Go backend (loads config from env).
	go run ./cmd/server

vet: ## go vet the whole module.
	go vet ./...

tidy: ## Sync go.mod/go.sum.
	go mod tidy

test: ## Run Go tests (none yet — TODO).
	go test ./...

fmt: ## gofmt the tree.
	gofmt -l -w .

db-up: ## Start Postgres + Mongo via docker compose.
	docker compose up -d

db-down: ## Stop and remove the dev datastores (keeps volumes).
	docker compose down

migrate: ## Apply migrations/0001_init.sql to Postgres ($(DATABASE_URL)).
	# Uses the postgres client inside the compose container so no host psql needed.
	docker compose exec -T postgres psql "$(DATABASE_URL)" -f /docker-entrypoint-initdb.d/0001_init.sql || \
		psql "$(DATABASE_URL)" -f $(MIGRATIONS_DIR)/0001_init.sql

frontend-dev: ## Run the Next.js dashboard dev server.
	cd frontend && npm install && npm run dev
