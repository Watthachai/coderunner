-- 0012: pick the build's Claude model and effort from the dashboard.
--
-- build_settings holds the operator's saved choice — one row, enforced by the
-- CHECK. It is absent until the first save; until then CRN uses
-- CRN_CLAUDE_MODEL / CRN_CLAUDE_EFFORT. The allowed values live in Go
-- (internal/buildsettings), not in a CHECK here, so a new model does not need
-- a migration.
--
-- project_jobs / job_traces record what each build actually ran with: the job
-- row for the dashboard while it runs, the trace for history. Nullable —
-- builds from before this migration never recorded it, and builds that reuse
-- a cached image never invoke a model at all.
--
-- Additive only: the running binary ignores the new table and columns, so this
-- is safe to apply before restarting onto the code that reads them.

CREATE TABLE IF NOT EXISTS build_settings (
  id            SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  claude_model  TEXT NOT NULL,
  claude_effort TEXT NOT NULL,
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_from  TEXT NOT NULL DEFAULT ''
);

ALTER TABLE project_jobs ADD COLUMN IF NOT EXISTS claude_model  TEXT;
ALTER TABLE project_jobs ADD COLUMN IF NOT EXISTS claude_effort TEXT;
ALTER TABLE job_traces   ADD COLUMN IF NOT EXISTS claude_model  TEXT;
ALTER TABLE job_traces   ADD COLUMN IF NOT EXISTS claude_effort TEXT;
