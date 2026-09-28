-- A sortable list: by position, then id, as the Rails app's
-- order(:position, :id); an unset position sorts first.

-- name: ListPrograms :many
SELECT * FROM programs ORDER BY position, id;

-- name: GetProgram :one
SELECT * FROM programs WHERE id = sqlc.arg(id);

-- name: CreateProgram :one
INSERT INTO programs (name, blurb, cta, position, created_at, updated_at)
VALUES (sqlc.arg(name), sqlc.arg(blurb), sqlc.arg(cta), sqlc.arg(position), sqlc.arg(now), sqlc.arg(now))
RETURNING id;

-- name: UpdateProgram :exec
UPDATE programs SET name = sqlc.arg(name), blurb = sqlc.arg(blurb), cta = sqlc.arg(cta), position = sqlc.arg(position), updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(id);

-- name: DeleteProgram :exec
DELETE FROM programs WHERE id = sqlc.arg(id);

-- A new program with no position goes last, as Rails' Positioned.
-- name: NextProgramPosition :one
SELECT CAST(COALESCE(MAX(position), 0) + 1 AS INTEGER) AS position FROM programs;
