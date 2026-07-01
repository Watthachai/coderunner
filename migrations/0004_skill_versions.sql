-- 0004_skill_versions.sql — skill version history.
--
-- Every user-initiated skill change (an operator edit via PUT /internal/skills
-- or a zip upload) records a snapshot of the resulting skill state here. The
-- builtin re-seed on startup (EnsureBuiltinSkill) does NOT record a version.
-- version is monotonically increasing per skill_name (COALESCE(max,0)+1).

BEGIN;

CREATE TABLE IF NOT EXISTS skill_versions (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  skill_name  TEXT NOT NULL,
  version     INT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  body        TEXT NOT NULL DEFAULT '',
  files       JSONB NOT NULL DEFAULT '{}'::jsonb,
  note        TEXT NOT NULL DEFAULT '',
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (skill_name, version)
);

COMMIT;
