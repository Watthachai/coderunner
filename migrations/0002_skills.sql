-- 0002_skills.sql — Claude Agent Skills registry (CRN harness + skill management).
--
-- Skills are the SKILL.md bodies CRN injects into each build's working dir
-- (.claude/skills/{name}/SKILL.md) before spawning Claude. The built-in
-- `fitt-build` skill is seeded on startup (EnsureBuiltinSkill); operators can
-- add/edit/enable more via the /internal/skills API.

BEGIN;

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS skills (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name        TEXT UNIQUE NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  body        TEXT NOT NULL DEFAULT '',
  enabled     BOOLEAN NOT NULL DEFAULT true,
  is_builtin  BOOLEAN NOT NULL DEFAULT false,
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMIT;
