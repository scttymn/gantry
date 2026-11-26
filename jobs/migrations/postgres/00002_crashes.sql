-- How many times a job's process died while it ran it: maintenance fails the
-- job at the third instead of putting it back forever, and never counts it
-- against Retry.Attempts.
-- +goose Up
ALTER TABLE gantry_jobs ADD COLUMN crashes integer NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE gantry_jobs DROP COLUMN crashes;
