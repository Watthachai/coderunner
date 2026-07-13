-- 0008_feedback_issue_link.sql
-- Link each feedback request to the GitHub issue CRN mirrors it into
-- (docs/superpowers/specs/2026-07-13-feedback-to-github-issues-design.md).
-- web_anon still only INSERTs the widget-supplied columns; these default on
-- insert (NULL / '') and are filled later by CRN, so no new grant is needed.
ALTER TABLE feedback_requests ADD COLUMN IF NOT EXISTS issue_number INT;
ALTER TABLE feedback_requests ADD COLUMN IF NOT EXISTS issue_url    TEXT NOT NULL DEFAULT '';

-- The watcher scans for un-mirrored rows; index just those.
CREATE INDEX IF NOT EXISTS idx_feedback_unmirrored
  ON feedback_requests (created_at) WHERE issue_number IS NULL;
