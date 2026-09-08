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

**Fresh macOS machine?** Run `./scripts/setup-macos.sh` — it installs everything
below (Homebrew, Go, Node, Docker, gh, git, Claude Code), writes `.env`, starts
the datastores, and applies migrations. Otherwise, do it by hand:

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


### Customer delivery checks

Customer builds require `fitt-build` to be enabled. Each agent run records the enabled skill content digests in `CRN_SKILLS.json`. Builds invoking Claude, or producing a customer image, execute the trusted release checker before Git export: locked install, typecheck, nonempty unit tests, production compilation, real customer Docker image startup and Playwright assertions. A failed or skipped required test blocks release. Issue edits cannot rewrite existing migrations; GitHub issues close only after successful delivery. Materialize-only mode with both `CRN_RUN_CLAUDE=false` and `CRN_BUILD_IMAGE=false` is a development path and is not customer verification.

The Runner host needs Node 22 or 24 LTS with npm, Docker and access to the pinned Playwright browser download. `CRN_VERIFY_NODE=/absolute/path/to/node` selects the verifier runtime and places its directory first on the child PATH. Tests get a disposable loopback PostgreSQL database and an isolated Docker network; do not configure customer credentials in test files. The checker cleans up its own temporary containers/images and records the result in `CRN_VERIFICATION.json`. See [the delivery contract](cmd/server/skillassets/references/delivery-checks.md).

New local-account apps bootstrap the initial administrator **once per customer database** using operator-supplied `BOOTSTRAP_ADMIN_EMAIL` / `BOOTSTRAP_ADMIN_PASSWORD`, without shared defaults. Existing users are adopted; later starts and ticket builds preserve accounts, password hashes and business data. A durable marker prevents recreation after deletion. Customer startup uses committed migrations and stops on migration or bootstrap failure. See [bootstrap rules](cmd/server/skillassets/references/auth-and-bootstrap.md).

To activate a source update, build and restart the backend after existing jobs finish. Startup refreshes the built-in skill while preserving its enabled state. Companion skills under `skills/` are uploaded separately; updating files on disk does not change already uploaded skill records. Verify enabled skills in the dashboard before starting the next customer build.

Regression checks: `go test ./...` and `node --test tests/delivery.test.mjs`. The PostgreSQL integration suite `tests/bootstrap-postgres.test.mjs` additionally needs `CRN_BOOTSTRAP_TEST_DATABASE_URL` pointing to a disposable loopback database named `crn_test`, and `CRN_TEST_PRISMA_MODULE` pointing to a matching generated Prisma client. It refuses other database destinations.

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
- **Lock down the interactive terminal WS** `GET /internal/projects/{id}/terminal`.
  It runs a real OS shell in a PTY in the project's workdir on the CRN host with
  **no auth** (mirrors the no-auth log WS) — i.e. a remote shell on the build
  host. Fine for local/trusted dev; in production it MUST be behind
  authentication and a network policy, or removed. Shell is chosen from
  `CRN_TERMINAL_SHELL` → `$SHELL` → `/bin/zsh` → `/bin/bash` → `/bin/sh`.

The full Docker/browser test can be reproduced with `python3 tests/create-runtime-fixture.py`, which prints a new temporary fixture directory. In that directory run `npm install --package-lock-only --ignore-scripts --no-audit --no-fund`. Then run `go test ./internal/buildstep -run TestDeliveryCustomerRuntime -v -count=1` from the repository with `CRN_DELIVERY_TEST_DIR` set to the fixture path and `CRN_VERIFY_NODE` set to the LTS Node executable. The fixture is a small test app, not a customer application or authentication template.
