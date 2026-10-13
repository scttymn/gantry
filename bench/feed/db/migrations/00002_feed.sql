-- The challenge's schema (its schema.sql), as it is: fixed, so there is no
-- migration after this one. IF NOT EXISTS, as a copy of its seed database
-- already has them.
-- +goose Up
CREATE TABLE IF NOT EXISTS users (
  id          INTEGER PRIMARY KEY,
  username    TEXT NOT NULL UNIQUE,
  created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE IF NOT EXISTS posts (
  id          INTEGER PRIMARY KEY,
  user_id     INTEGER NOT NULL REFERENCES users(id),
  body        TEXT NOT NULL CHECK (length(body) BETWEEN 1 AND 500),
  created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE IF NOT EXISTS likes (
  user_id     INTEGER NOT NULL REFERENCES users(id),
  post_id     INTEGER NOT NULL REFERENCES posts(id),
  created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  PRIMARY KEY (user_id, post_id)
);

CREATE INDEX IF NOT EXISTS posts_created_at_id_idx ON posts (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS posts_user_id_idx       ON posts (user_id);
CREATE INDEX IF NOT EXISTS likes_post_id_idx       ON likes (post_id);

-- +goose Down
DROP TABLE likes;
DROP TABLE posts;
DROP TABLE users;
