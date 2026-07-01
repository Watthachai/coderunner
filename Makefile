# CRN Makefile. Targets are thin wrappers around the canonical commands so the
# README stays short. Override DATABASE_URL etc. via env or a .env you source.

# Default local DSN matching docker-compose.yml (host port 5433 -> container 5432).
DATABASE_URL ?= postgres://crn:crn_dev_password@localhost:5433/crn?sslmode=disable
MIGRATIONS_DIR ?= migrations
# Port the backend listens on (matches CRN_LISTEN_ADDR's default :8080).
PORT ?= 8080

.PHONY: help build run stop restart vet tidy test migrate db-up db-down fmt frontend-dev

help: ## Show this help.
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

build: ## Compile the Go backend to ./bin/crn-server.
	go build -o bin/crn-server ./cmd/server

run: ## Run the Go backend (auto-loads .env if present).
	@if [ -f .env ]; then set -a; . ./.env; set +a; else \
		echo "warning: no .env found — run: cp .env.example .env"; fi; \
	go run ./cmd/server

stop: ## Stop the running backend (kills whatever listens on :$(PORT)).
	@pid=$$(lsof -nP -tiTCP:$(PORT) -sTCP:LISTEN 2>/dev/null); \
	if [ -n "$$pid" ]; then kill $$pid 2>/dev/null; sleep 1; \
		pid=$$(lsof -nP -tiTCP:$(PORT) -sTCP:LISTEN 2>/dev/null); \
		[ -n "$$pid" ] && kill -9 $$pid 2>/dev/null; echo "stopped :$(PORT)"; \
	else echo "nothing listening on :$(PORT)"; fi

restart: ## Stop any running backend, then run a fresh one (picks up .env + rebuild).
	@$(MAKE) stop
	@$(MAKE) run

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

migrate: ## Apply migrations/0001_init.sql to Postgres (inside the compose container).
	# Runs psql inside the container via its local connection, so it is immune to
	# the host-side port (5433) and needs no host psql.
	docker compose exec -T postgres psql -U crn -d crn -f /docker-entrypoint-initdb.d/0001_init.sql

frontend-dev: ## Run the Next.js dashboard dev server.
	cd frontend && npm install && npm run dev
