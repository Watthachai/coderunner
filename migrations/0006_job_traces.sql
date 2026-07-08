-- 0006_job_traces.sql — durable per-build trace (state history).
--
-- The live build stream (tool calls, phases, assistant text) is fanned out over
-- the WebSocket and buffered in memory only (jobs.manager.hist); that buffer is
-- DISCARDED the instant a job reaches a terminal state (closeSubscribers deletes
-- it). So after a build finishes there is no way to see what Claude actually did,
-- what commit it produced, or where it pushed — only the coarse build_events
-- lifecycle rows (started/done/failed) survive.
--
-- job_traces fixes that: at the terminal state (done/failed/cancelled) the job's
-- full normalized event stream is snapshotted here as JSONB, alongside a derived
-- summary (commit sha, branch, remote, cost, session, tool/file counts, timings).
-- One immutable row per job, written once. This is the source for the operator
-- console's retroactive "state trace" view and the armed-idle console replay.

BEGIN;

CREATE TABLE IF NOT EXISTS job_traces (
  job_id       UUID PRIMARY KEY REFERENCES project_jobs(id) ON DELETE CASCADE,
  project_id   UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  build_no     INTEGER NOT NULL,
  outcome      TEXT NOT NULL DEFAULT '',            -- 'done' | 'failed' | 'cancelled'
  mode         TEXT NOT NULL DEFAULT '',            -- 'build' | 'edit'
  commit_sha   TEXT NOT NULL DEFAULT '',
  branch       TEXT NOT NULL DEFAULT '',
  remote       TEXT NOT NULL DEFAULT '',
  session_id   TEXT NOT NULL DEFAULT '',
  cost_usd     DOUBLE PRECISION NOT NULL DEFAULT 0,
  tool_count   INTEGER NOT NULL DEFAULT 0,
  file_count   INTEGER NOT NULL DEFAULT 0,
  error_msg    TEXT NOT NULL DEFAULT '',
  events       JSONB NOT NULL DEFAULT '[]',         -- full BuildEventMsg[] for replay
  started_at   TIMESTAMPTZ,
  finished_at  TIMESTAMPTZ,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Newest build first, per project (the history list query).
CREATE INDEX IF NOT EXISTS idx_job_traces_project ON job_traces (project_id, build_no DESC);
-- Global recent traces (the dashboard traces panel / armed-idle console).
CREATE INDEX IF NOT EXISTS idx_job_traces_created ON job_traces (created_at DESC);

COMMIT;
