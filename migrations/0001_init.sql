-- 0001_init.sql — CRN schema (PostgreSQL).
-- Adapted from CRN-architecture.md §2.2. Run with `make migrate` (golang-migrate)
-- or psql -f. Idempotent-ish: uses IF NOT EXISTS so re-runs are safe in dev.
--
-- NOTE: gen_random_uuid() requires the pgcrypto extension on older Postgres;
-- on PG 13+ it is built in via pgcrypto. We enable it explicitly to be safe.

BEGIN;

CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- ─────────────────────────────────────────────────────────────────────────────
-- Orgs — tenants. One API key + max one building job each (architecture §2.4).
-- ─────────────────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS orgs (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name        TEXT NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ─────────────────────────────────────────────────────────────────────────────
-- API keys — per-org credential for the external edit API (architecture §6).
-- Only the hash is stored; plaintext sk-org-{org_id}-{random_32} is shown once.
-- ─────────────────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS api_keys (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id      UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  key_hash    TEXT NOT NULL UNIQUE,          -- sha256 hex of the plaintext key
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  revoked_at  TIMESTAMPTZ                    -- NULL = active
);
-- Auth lookup path: resolve active org by key hash.
CREATE INDEX IF NOT EXISTS idx_api_keys_active
  ON api_keys (key_hash) WHERE revoked_at IS NULL;

-- ─────────────────────────────────────────────────────────────────────────────
-- Projects — registry of buildable codebases (architecture §2.2).
-- ─────────────────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS projects (
  id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id         UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  name           TEXT NOT NULL,
  status         TEXT NOT NULL DEFAULT 'pending'
                 CHECK (status IN ('pending','active','archived')),
  current_build  INT  NOT NULL DEFAULT 0,
  stack          TEXT,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_projects_org ON projects (org_id);

-- ─────────────────────────────────────────────────────────────────────────────
-- Project jobs — the build queue / state machine (architecture §2.2, §3).
-- ─────────────────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS project_jobs (
  id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id   UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  org_id       UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  status       TEXT NOT NULL DEFAULT 'queued'
               CHECK (status IN ('queued','building','done','failed','cancelled')),
  build_no     INT  NOT NULL,
  payload      JSONB NOT NULL,           -- requirement + assets handed to Claude
  session_id   TEXT,                     -- Claude Code session id (for --resume)
  docker_tag   TEXT,                     -- {docker_user}/{project_id}:v{build_no}
  error_msg    TEXT,
  queued_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  started_at   TIMESTAMPTZ,
  finished_at  TIMESTAMPTZ,
  UNIQUE (project_id, build_no)
);
-- Queue scan path: oldest queued job per org (FTC DV trigger / chaining).
CREATE INDEX IF NOT EXISTS idx_jobs_org_status_queued
  ON project_jobs (org_id, queued_at) WHERE status = 'queued';
CREATE INDEX IF NOT EXISTS idx_jobs_project ON project_jobs (project_id);
-- Enforce "max 1 building job per org" at the DB layer as a backstop to the
-- advisory lock used by internal/store (partial unique index).
CREATE UNIQUE INDEX IF NOT EXISTS uq_jobs_one_building_per_org
  ON project_jobs (org_id) WHERE status = 'building';

-- ─────────────────────────────────────────────────────────────────────────────
-- Edit requests — external "please change X" requests (architecture §6).
-- ─────────────────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS edit_requests (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id    UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  job_id        UUID REFERENCES project_jobs(id) ON DELETE SET NULL,
  requester     TEXT,                    -- org member / external API caller
  diff_request  JSONB NOT NULL,          -- { "change": "...", "files": [...] }
  priority      TEXT NOT NULL DEFAULT 'normal'
                CHECK (priority IN ('normal','urgent')),
  status        TEXT NOT NULL DEFAULT 'pending'
                CHECK (status IN ('pending','merged_to_job','rejected')),
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_edit_requests_project ON edit_requests (project_id);

-- ─────────────────────────────────────────────────────────────────────────────
-- Build events — notification bus for fan-out to FBD + FTC DV (architecture §2.2).
-- On the central DB, internal/store.Notifier INSERTs here and issues NOTIFY.
-- ─────────────────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS build_events (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  job_id          UUID NOT NULL REFERENCES project_jobs(id) ON DELETE CASCADE,
  event_type      TEXT NOT NULL
                  CHECK (event_type IN ('build_started','build_done','build_failed')),
  payload         JSONB,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  notified_fbd    BOOLEAN NOT NULL DEFAULT false,
  notified_ftcdv  BOOLEAN NOT NULL DEFAULT false
);
-- Subscriber poll path: unsent events oldest-first.
CREATE INDEX IF NOT EXISTS idx_build_events_unsent
  ON build_events (created_at)
  WHERE notified_fbd = false OR notified_ftcdv = false;

COMMIT;
