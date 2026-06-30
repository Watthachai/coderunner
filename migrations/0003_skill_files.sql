-- 0003_skill_files.sql — multi-file skills.
--
-- A skill is a SKILL.md body (the existing `body` column) plus an OPTIONAL set
-- of extra files (scripts/, references/, ...) shipped alongside it. The extra
-- files are stored as a JSONB map of relative-path -> file-content so a skill
-- stays a single row (no separate table). SKILL.md itself is NOT in here — it
-- remains in `body`. Injection writes both `body` (as SKILL.md) and every entry
-- in `files` under {workdir}/.claude/skills/{name}/.

BEGIN;

ALTER TABLE skills ADD COLUMN files jsonb NOT NULL DEFAULT '{}'::jsonb;

COMMIT;
