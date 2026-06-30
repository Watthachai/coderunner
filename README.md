# FITT Code Runner (CRN)

CRN owns the **build lifecycle** of the FITT ecosystem. FTC DV only sends a
trigger; CRN does everything else: receive the trigger → spawn Claude Code →
stream its output live → manage the build queue (1 build per org) → docker
build/push → notify the central DB for fan-out to FBD and FTC DV.

Architecture: see `../fitt-builder-v2/docs/brief-plans/CRN-architecture.md`.

```
FTC DV ──trigger──► CRN (Go: API + WS)  ──stream──► CRN Dashboard (Next.js)
                       │
                       ├─ spawn `claude --output-format stream-json`
                       ├─ docker build/push  (Docker Hub)
                       └─ INSERT build_events ─► DB กลาง ─► FBD + FTC DV
```

## Layout

```
cmd/server/          entrypoint: config → logger → store → jobs → api → serve
internal/domain/     shared types + ALL cross-package interfaces (ports)
internal/config/     env loader (Load)
internal/store/      Postgres adapter (domain.Store, domain.Notifier)   [pgx]
internal/claude/     spawns + parses Claude Code stream-json (THE SPIKE)
internal/jobs/       queue + lifecycle + per-org advisory lock
internal/api/        chi router, REST + WebSocket, API-key auth
migrations/          0001_init.sql (PostgreSQL)
frontend/            Next.js dashboard skeleton
```

The interfaces in `internal/domain` are the contract: every other package codes
against them, not against concrete types.

## Run it (local dev)

Prereqs: Go 1.23+, Docker, Node 20+, and the `claude` CLI on PATH.

```bash
# 1) start datastores (Postgres auto-applies migrations/0001_init.sql on a fresh volume)
docker compose up -d
# or: make db-up

# 2) configure
cp .env.example .env        # set CRN_CLAUDE_BIN (Apple Silicon: /opt/homebrew/bin/claude) + CRN_DOCKER_USER

# 3) run the backend  (http + ws on :8080) — `make run` auto-loads .env
make run

# 4) run the dashboard  (separate terminal)
make frontend-dev           # == cd frontend && npm install && npm run dev  (:3000)
```

Apply migrations manually (if not using the auto-init volume):

```bash
make migrate
```

## Verify the build

```bash
go build ./...   # compiles
go vet ./...     # type-checks the whole module
```

> All four implementer packages (`claude`, `store`, `jobs`, `api`) are fully
> implemented (~2,500 lines of real Go) against the `internal/domain` interfaces,
> and `cmd/server/main.go` wires them together. The remaining `// TODO(...)`
> markers are scoped follow-ups (real docker build/push, rollback retag,
> git-commit-per-build, `LISTEN/NOTIFY`), not panic stubs — the binary builds and
> runs. Do not change the constructor signatures or the `domain` interfaces.

## Skills (Claude Agent Skills harness)

Enabled skills are injected into each build's working dir as
`{workdir}/.claude/skills/{name}/` (SKILL.md from `body`, plus any extra files
from the `files` JSONB map — `scripts/`, `references/`, …) before Claude runs,
then removed before the git push.

The built-in `fitt-build` harness is the **code's** source of truth
(`cmd/server/builtin_skill.go`): on every startup `EnsureBuiltinSkill` re-applies
its body/description/files (`ON CONFLICT (name) DO UPDATE`), but **preserves the
operator's `enabled` flag** — restarting CRN refreshes the canonical harness
while enable/disable stays an operator decision via `PUT /internal/skills/{name}`.

## Frontend

`frontend/` is a minimal Next.js app (App Router). The dashboard — overview,
per-project status, and the live Job Monitor that consumes the WebSocket at
`/api/v1/projects/{id}/jobs/{build_no}/logs` — is built by the frontend
implementer. See `internal/domain/events.go` `BuildEventMsg` for the wire shape.

## Status / TODO

Scaffolded but not feature-complete (clearly marked `// TODO(...)` in code):
real docker build/push, rollback retag, git-commit-per-build, retry logic,
Postgres `LISTEN/NOTIFY` wiring, and the MongoDB BRD/PRD store.

## Production checklist (deferred)

- Bake `claude` + docker CLI into the runtime image (see `Dockerfile` TODO).
- Move the central DB to the shared fixed-IP VM (architecture §8 Phase 9).
- Secrets management for `X-API-Key` hashing + Docker Hub credentials.
- Lock down `POST /internal/trigger` (network policy or shared secret).
