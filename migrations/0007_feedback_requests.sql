-- 0007_feedback_requests.sql
-- In-demo feedback / edit-request intake for the feedback loop
-- (docs/superpowers/specs/2026-07-07-feedback-loop-design.md).
--
-- Write path: a CRN-built demo's feedback widget INSERTs a row via PostgREST
-- (public, unauthenticated `web_anon` role — INSERT-only). Read path: CRN reads
-- this table directly with its existing pgx pool (no CRN exposure) to render the
-- Edit Request Panel. Screenshots are stored inline as base64 data URIs inside
-- `payload` (no object storage in this phase).

CREATE TABLE IF NOT EXISTS feedback_requests (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id  UUID NOT NULL,
  -- new | reviewing | approved | building | done | rejected
  status      TEXT NOT NULL DEFAULT 'new',
  category    TEXT NOT NULL DEFAULT 'feature',   -- bug | feature | style
  priority    TEXT NOT NULL DEFAULT 'med',       -- low | med | high
  note        TEXT NOT NULL DEFAULT '',
  page_url    TEXT NOT NULL DEFAULT '',
  reporter    TEXT NOT NULL DEFAULT '',
  -- { pins:[{selector,label,note,box,region_shot}], full_shot, viewport, user_agent }
  payload     JSONB NOT NULL DEFAULT '{}'::jsonb,
  job_id      UUID,                              -- set when merged into an edit build
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- No FK on project_id: unknown ids are tolerated at insert (a widget baked at
-- build time always carries a real id) and simply ignored by the panel.

CREATE INDEX IF NOT EXISTS idx_feedback_status
  ON feedback_requests (status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_feedback_project
  ON feedback_requests (project_id, created_at DESC);

-- --- PostgREST roles: the public write tier for demo widgets ---
-- `web_anon` is the unauthenticated role PostgREST switches to; it can ONLY
-- INSERT feedback (no SELECT/UPDATE/DELETE, no other tables). `authenticator`
-- is the login role PostgREST itself connects as.
DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'web_anon') THEN
    CREATE ROLE web_anon NOLOGIN;
  END IF;
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'authenticator') THEN
    CREATE ROLE authenticator NOINHERIT LOGIN PASSWORD 'crn_dev_password';
  END IF;
END
$$;

GRANT web_anon TO authenticator;
GRANT USAGE ON SCHEMA public TO web_anon;
GRANT INSERT ON feedback_requests TO web_anon;
