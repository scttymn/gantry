-- A post as the API shows it: its author's name, and its likes counted now.

-- name: Feed :many
SELECT p.id, p.body, p.created_at, u.username AS author,
       (SELECT count(*) FROM likes l WHERE l.post_id = p.id) AS like_count
  FROM posts p JOIN users u ON u.id = p.user_id
 ORDER BY p.created_at DESC, p.id DESC LIMIT 20;

-- name: GetPost :one
SELECT p.id, p.body, p.created_at, u.username AS author,
       (SELECT count(*) FROM likes l WHERE l.post_id = p.id) AS like_count
  FROM posts p JOIN users u ON u.id = p.user_id
 WHERE p.id = ?;

-- name: CreatePost :one
INSERT INTO posts (user_id, body) VALUES (?, ?) RETURNING id, created_at;

-- name: LikePost :execrows
INSERT INTO likes (user_id, post_id) VALUES (?, ?) ON CONFLICT (user_id, post_id) DO NOTHING;
