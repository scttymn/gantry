-- gantry's jobs: the jobs, the processes running them, the limits per key,
-- and the ticks of recurring jobs (docs/plans/mission-control.md, G2).
-- +goose Up
CREATE TABLE gantry_jobs (
  id integer PRIMARY KEY AUTOINCREMENT NOT NULL,
  queue text NOT NULL,
  name text NOT NULL,
  args text NOT NULL,
  priority integer NOT NULL DEFAULT 0,
  run_at datetime NOT NULL,
  attempts integer NOT NULL DEFAULT 0,
  state text NOT NULL DEFAULT 'ready',
  limit_key text NOT NULL DEFAULT '',
  claimed_by text NOT NULL DEFAULT '',
  claimed_at datetime,
  finished_at datetime,
  error text NOT NULL DEFAULT '',
  created_at datetime NOT NULL
);
CREATE INDEX gantry_jobs_due ON gantry_jobs (queue, state, priority, run_at, id);
CREATE INDEX gantry_jobs_claimed ON gantry_jobs (state, claimed_by);
CREATE TABLE gantry_job_processes (
  id text PRIMARY KEY NOT NULL,
  heartbeat_at datetime NOT NULL
);
CREATE TABLE gantry_job_semaphores (
  key text PRIMARY KEY NOT NULL,
  value integer NOT NULL,
  expires_at datetime NOT NULL
);
CREATE TABLE gantry_job_ticks (
  task text NOT NULL,
  run_at datetime NOT NULL,
  PRIMARY KEY (task, run_at)
);

-- +goose Down
DROP TABLE gantry_job_ticks;
DROP TABLE gantry_job_semaphores;
DROP TABLE gantry_job_processes;
DROP TABLE gantry_jobs;
