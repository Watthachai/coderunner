-- 0013: rehearsal jobs and project freezes.
--
-- A rehearsal job is a build that must never report to FTC DV (no building,
-- released or failed callback), so it creates nothing in Fittcore. The switch
-- lives on the one job it was set for; every other job, including the rows FTC
-- DV inserts itself, defaults to normal callbacks.
--
-- A frozen project runs no queued job except the one the freeze allows. Its
-- other queued jobs stay queued — never failed, so nothing reports — until an
-- operator lifts the freeze.

ALTER TABLE project_jobs ADD COLUMN IF NOT EXISTS rehearsal BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE projects ADD COLUMN IF NOT EXISTS frozen_reason TEXT;
ALTER TABLE projects ADD COLUMN IF NOT EXISTS frozen_allow_job UUID;
ALTER TABLE projects ADD COLUMN IF NOT EXISTS frozen_at TIMESTAMPTZ;
