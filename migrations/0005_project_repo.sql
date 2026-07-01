-- 0005_project_repo.sql — per-project GitHub repo URL.
--
-- Supports the "one private GitHub repo per project" model (opt-in via
-- CRN_GITHUB_OWNER). When enabled, each project gets its own repo
-- (crn-<slug>-<id8>) and its https clone URL is recorded here once at the first
-- build/edit. Empty means the project has no dedicated repo yet (or the legacy
-- shared-remote/branch model is in use).

BEGIN;

ALTER TABLE projects ADD COLUMN IF NOT EXISTS repo_url TEXT NOT NULL DEFAULT '';

COMMIT;
