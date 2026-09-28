-- Newest first.

-- name: ListWorkouts :many
SELECT * FROM workouts ORDER BY id DESC;

-- name: GetWorkout :one
SELECT * FROM workouts WHERE id = sqlc.arg(id);

-- name: CreateWorkout :one
INSERT INTO workouts (date, name, rx, created_at, updated_at)
VALUES (CAST(sqlc.narg(date) AS TEXT), sqlc.arg(name), sqlc.arg(rx), sqlc.arg(now), sqlc.arg(now))
RETURNING id;

-- name: UpdateWorkout :exec
UPDATE workouts SET date = CAST(sqlc.narg(date) AS TEXT), name = sqlc.arg(name), rx = sqlc.arg(rx), updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(id);

-- name: DeleteWorkout :exec
DELETE FROM workouts WHERE id = sqlc.arg(id);
