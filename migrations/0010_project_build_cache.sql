-- 0010_project_build_cache.sql — per-project source-hash dedup cache.
--
-- A fresh build converts the Vite prototype into a Next.js + Prisma app with an
-- AI agent — the expensive part of a build (minutes + $). When the SAME source is
-- resubmitted (a retry, or a rebuild with an unchanged prototype), there is nothing
-- new to convert. We record the content hash of the last successfully-built source
-- plus the image it produced, so an identical resubmission can skip the conversion
-- and re-emit the existing image.
--
-- last_source_hash: sha256 of the last successful FRESH build's source (empty for
--   edit builds, which diverge the source — this naturally prevents a later fresh
--   build from reusing a post-edit image).
-- last_image_ref:   the image tag that build produced (pullable). Reuse only when set.

BEGIN;

ALTER TABLE projects ADD COLUMN IF NOT EXISTS last_source_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN IF NOT EXISTS last_image_ref   TEXT NOT NULL DEFAULT '';

COMMIT;
