-- 0009_build_cancelled_event.sql
-- Add 'build_cancelled' to the build_events event_type CHECK so an operator
-- cancellation is a first-class terminal event, distinct from build_failed.
-- The dashboard activity feed can then show "cancelled" instead of "failed".
-- Additive: the three prior types and all existing rows stay valid. Constraint
-- name confirmed against the live schema (Postgres auto-name for the inline
-- column CHECK in 0001_init.sql).
ALTER TABLE build_events DROP CONSTRAINT IF EXISTS build_events_event_type_check;
ALTER TABLE build_events ADD CONSTRAINT build_events_event_type_check
  CHECK (event_type IN ('build_started', 'build_done', 'build_failed', 'build_cancelled'));
