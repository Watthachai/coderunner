-- 0011: concurrent builds — move the "one build in flight" rule from the org to
-- the project.
--
-- Builds of DIFFERENT projects share nothing: each has its own working dir
-- (projectsDir/<project_id>), its own git repo, and its own image tag. Only two
-- builds of the SAME project genuinely conflict. The original per-org index
-- serialized an entire customer's queue behind one build, which made a run of
-- projects take the sum of their build times.
--
-- The new partial unique index is the DB-layer backstop for the per-project
-- advisory lock in internal/store (AcquireProjectLock); it also serves the
-- NOT EXISTS "is this project already building?" probe in NextQueuedJob.

DROP INDEX IF EXISTS uq_jobs_one_building_per_org;

CREATE UNIQUE INDEX IF NOT EXISTS uq_jobs_one_building_per_project
  ON project_jobs (project_id) WHERE status = 'building';
