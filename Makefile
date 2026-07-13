# CRN Makefile. Targets are thin wrappers around the canonical commands so the
# README stays short. Override DATABASE_URL etc. via env or a .env you source.

# Default local DSN matching docker-compose.yml (host port 5433 -> container 5432).
DATABASE_URL ?= postgres://crn:crn_dev_password@localhost:5433/crn?sslmode=disable
MIGRATIONS_DIR ?= migrations
# Port the backend listens on (matches CRN_LISTEN_ADDR's default :8080).
PORT ?= 8080

.PHONY: help build run stop restart vet tidy test migrate migrate-baseline db-up db-down fmt frontend-dev

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

migrate: ## Apply un-applied migrations/*.sql in order (tracked in schema_migrations).
	# The initdb mounts only run on a fresh volume, and old migrations are not
	# idempotent, so a plain "apply all" re-run errors. This tracks applied
	# versions in schema_migrations and applies only the ones not yet recorded.
	# NOTE: on a pre-existing DB, run `make migrate-baseline` ONCE first so the
	# already-applied migrations are recorded (else this re-applies and fails).
	@docker compose exec -T postgres psql -v ON_ERROR_STOP=1 -U crn -d crn -qc \
		"CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now());"
	@for f in $$(ls $(MIGRATIONS_DIR)/*.sql | sort); do \
		v=$$(basename $$f .sql); \
		if [ "$$(docker compose exec -T postgres psql -tAqc "SELECT 1 FROM schema_migrations WHERE version='$$v'" -U crn -d crn)" = "1" ]; then \
			echo "skip   $$v"; continue; fi; \
		echo "apply  $$v"; \
		docker compose exec -T postgres psql -v ON_ERROR_STOP=1 -U crn -d crn < $$f || exit 1; \
		docker compose exec -T postgres psql -v ON_ERROR_STOP=1 -U crn -d crn -qc "INSERT INTO schema_migrations(version) VALUES ('$$v');"; \
	done

migrate-baseline: ## Record ALL current migrations as applied WITHOUT running them (adopt an existing DB).
	@docker compose exec -T postgres psql -v ON_ERROR_STOP=1 -U crn -d crn -qc \
		"CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now());"
	@for f in $$(ls $(MIGRATIONS_DIR)/*.sql | sort); do \
		v=$$(basename $$f .sql); \
		docker compose exec -T postgres psql -v ON_ERROR_STOP=1 -U crn -d crn -qc "INSERT INTO schema_migrations(version) VALUES ('$$v') ON CONFLICT DO NOTHING;"; \
		echo "baseline $$v"; \
	done

frontend-dev: ## Run the Next.js dashboard dev server.
	cd frontend && npm install && npm run dev
