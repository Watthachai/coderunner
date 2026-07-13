#!/usr/bin/env bash
#
# FITT Code Runner (CRN) — one-shot macOS setup.
#
# Installs every prerequisite (Homebrew, Go 1.23+, Node 20+, Docker Desktop,
# gh, git, Claude Code), prepares .env, starts the datastores, and applies the
# database migrations. Safe to re-run.
#
# Usage (from a fresh clone):
#   git clone https://github.com/Watthachai/coderunner.git fitt-coderunner
#   cd fitt-coderunner && git checkout dev
#   ./scripts/setup-macos.sh
#
set -euo pipefail

info() { printf "\033[1;36m==>\033[0m %s\n" "$*"; }
warn() { printf "\033[1;33m!!\033[0m  %s\n" "$*"; }
die()  { printf "\033[1;31mxx\033[0m  %s\n" "$*" >&2; exit 1; }

[ "$(uname -s)" = "Darwin" ] || die "macOS only. On Linux install Go 1.23+, Node 20+, Docker, gh, git, and Claude Code by hand."

# Run from the repo root regardless of where the script is invoked.
cd "$(cd "$(dirname "$0")/.." && pwd)"

# --- 1) Homebrew ------------------------------------------------------------
if ! command -v brew >/dev/null 2>&1; then
  info "Installing Homebrew…"
  /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
fi
# Make brew available in this shell (Apple Silicon vs Intel prefixes).
[ -x /opt/homebrew/bin/brew ] && eval "$(/opt/homebrew/bin/brew shellenv)"
[ -x /usr/local/bin/brew ]    && eval "$(/usr/local/bin/brew shellenv)"

# --- 2) CLI tools -----------------------------------------------------------
info "Installing CLI tools (go, node, gh, git)…"
brew install go node gh git
go version   | grep -qE 'go1\.(2[3-9]|[3-9][0-9])'    || warn "Go 1.23+ recommended (got: $(go version))"
node -v      | grep -qE 'v(2[0-9]|[3-9][0-9])'         || warn "Node 20+ recommended (got: $(node -v))"

# --- 3) Docker Desktop ------------------------------------------------------
if ! command -v docker >/dev/null 2>&1; then
  info "Installing Docker Desktop…"
  brew install --cask docker
fi
info "Starting Docker Desktop and waiting for the daemon…"
open -ga Docker 2>/dev/null || true
tries=0
until docker info >/dev/null 2>&1; do
  tries=$((tries + 1)); [ "$tries" -gt 60 ] && die "Docker daemon not ready — open Docker Desktop, then re-run."
  sleep 2
done
info "Docker is running."

# --- 4) Claude Code CLI -----------------------------------------------------
if ! command -v claude >/dev/null 2>&1; then
  info "Installing Claude Code CLI…"
  npm install -g @anthropic-ai/claude-code
fi
CLAUDE_BIN="$(command -v claude)"
info "Claude Code: ${CLAUDE_BIN}"

# --- 5) .env ----------------------------------------------------------------
if [ ! -f .env ]; then
  info "Creating .env from .env.example…"
  cp .env.example .env
fi
# Point CRN_CLAUDE_BIN at the detected binary.
sed -i '' "s|^CRN_CLAUDE_BIN=.*|CRN_CLAUDE_BIN=${CLAUDE_BIN}|" .env
# Ask for the GitHub owner (enables the feedback→GitHub-issues feature).
if grep -qE '^CRN_GITHUB_OWNER=$' .env; then
  printf "GitHub username for CRN_GITHUB_OWNER (blank to skip): "
  read -r owner || owner=""
  [ -n "$owner" ] && sed -i '' "s|^CRN_GITHUB_OWNER=.*|CRN_GITHUB_OWNER=${owner}|" .env
fi

# --- 6) Datastores + migrations --------------------------------------------
info "Starting datastores (Postgres + PostgREST + Mongo)…"
docker compose up -d
info "Waiting for Postgres to become healthy…"
tries=0
until [ "$(docker inspect -f '{{.State.Health.Status}}' crn-postgres 2>/dev/null)" = "healthy" ]; do
  tries=$((tries + 1)); [ "$tries" -gt 60 ] && die "Postgres never became healthy."
  sleep 2
done
# A fresh volume auto-applies ALL migrations via docker-entrypoint-initdb.d.
# Baseline the ledger to the latest version so future `make migrate` only runs
# NEW migrations. (Existing/older DBs: instead run
#   make migrate-baseline BASELINE_UPTO=<last-applied>  &&  make migrate)
LATEST="$(ls migrations/*.sql | sort | tail -1 | xargs basename | cut -d_ -f1)"
info "Baselining migration ledger up to ${LATEST}…"
make migrate-baseline BASELINE_UPTO="${LATEST}" >/dev/null

# --- 7) Frontend deps + gh auth --------------------------------------------
info "Installing dashboard dependencies…"
(cd frontend && npm install --silent)

if ! gh auth status >/dev/null 2>&1; then
  warn "GitHub CLI is not authenticated. Run:  gh auth login   (needed for the issues feature)."
fi

cat <<'EOF'

✅ Setup complete.

Next steps:
  1) Confirm .env — CRN_GITHUB_OWNER and (if not done) `gh auth login`.
  2) Backend:    make run
  3) Dashboard:  make frontend-dev      # in another terminal → http://localhost:3000

Datastores are up (`docker compose ps`), migrations applied.
EOF
