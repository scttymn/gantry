-- A sortable list: by position, then id, as the Rails app's
-- order(:position, :id); an unset position sorts first.

-- name: ListPillars :many
SELECT * FROM pillars ORDER BY position, id;

-- name: GetPillar :one
SELECT * FROM pillars WHERE id = sqlc.arg(id);

-- name: CreatePillar :one
INSERT INTO pillars (title, body, position, created_at, updated_at)
VALUES (sqlc.arg(title), sqlc.arg(body), sqlc.arg(position), sqlc.arg(now), sqlc.arg(now))
RETURNING id;

-- name: UpdatePillar :exec
UPDATE pillars SET title = sqlc.arg(title), body = sqlc.arg(body), position = sqlc.arg(position), updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(id);

-- name: DeletePillar :exec
DELETE FROM pillars WHERE id = sqlc.arg(id);

-- A new pillar with no position goes last, as Rails' Positioned.
-- name: NextPillarPosition :one
SELECT CAST(COALESCE(MAX(position), 0) + 1 AS INTEGER) AS position FROM pillars;
