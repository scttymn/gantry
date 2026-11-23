-- A sortable list: by position, then id, as the Rails app's
-- order(:position, :id); an unset position sorts first.

-- name: ListEvents :many
SELECT * FROM events ORDER BY position, id;

-- name: GetEvent :one
SELECT * FROM events WHERE id = sqlc.arg(id);

-- The row, read in the transaction that changes it, so no other change
-- lands between the read and the write: it stays locked until the commit.
-- name: GetEventForUpdate :one
SELECT * FROM events WHERE id = sqlc.arg(id) FOR UPDATE;

-- name: CreateEvent :one
INSERT INTO events (name, starts_on, position, created_at, updated_at)
VALUES (sqlc.arg(name), CAST(CAST(sqlc.narg(starts_on) AS TEXT) AS DATE), sqlc.arg(position), sqlc.arg(now), sqlc.arg(now))
RETURNING id;

-- name: UpdateEvent :exec
UPDATE events SET name = sqlc.arg(name), starts_on = CAST(CAST(sqlc.narg(starts_on) AS TEXT) AS DATE), position = sqlc.arg(position), updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(id);

-- name: DeleteEvent :exec
DELETE FROM events WHERE id = sqlc.arg(id);

-- A new event with no position goes last, as Rails' Positioned.
-- BIGINT is int64 on both engines, as the position column is.
-- name: NextEventPosition :one
SELECT CAST(COALESCE(MAX(position), 0) + 1 AS BIGINT) AS position FROM events;
