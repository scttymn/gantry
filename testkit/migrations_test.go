package testkit

import (
	"strings"
	"testing"
	"testing/fstest"
)

// migrations runs Migrations with a recorder, as a test would.
func migrations(t *testing.T, fsys fstest.MapFS) *recorder {
	r := &recorder{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		Migrations(r, DB(t, nil), fsys, "app_migrations")
	}()
	<-done
	return r
}

func TestMigrations(t *testing.T) {
	create := &fstest.MapFile{Data: []byte("-- +goose Up\nCREATE TABLE posts (id integer PRIMARY KEY);\n-- +goose Down\nDROP TABLE posts;\n")}
	t.Run("up, each down, and up again: nothing to report", func(t *testing.T) {
		index := &fstest.MapFile{Data: []byte("-- +goose Up\nCREATE INDEX posts_by_id ON posts (id);\n-- +goose Down\nDROP INDEX posts_by_id;\n")}
		if r := migrations(t, fstest.MapFS{"00001_posts.sql": create, "00002_index.sql": index}); len(r.errors) != 0 {
			t.Fatal(r.errors)
		}
	})
	t.Run("a down section that fails", func(t *testing.T) {
		broken := &fstest.MapFile{Data: []byte("-- +goose Up\nCREATE TABLE tags (id integer);\n-- +goose Down\nDROP TABLE tagz;\n")}
		r := migrations(t, fstest.MapFS{"00001_posts.sql": create, "00002_tags.sql": broken})
		if len(r.errors) != 1 || !strings.Contains(r.errors[0], "00002_tags.sql") {
			t.Fatal(r.errors)
		}
	})
	t.Run("a down section that leaves something behind", func(t *testing.T) {
		leaky := &fstest.MapFile{Data: []byte("-- +goose Up\nCREATE TABLE tags (id integer);\nCREATE TABLE labels (id integer);\n-- +goose Down\nDROP TABLE tags;\n")}
		r := migrations(t, fstest.MapFS{"00001_posts.sql": create, "00002_tags.sql": leaky})
		if len(r.errors) != 1 || !strings.Contains(r.errors[0], "00002_tags.sql: its down section doesn't undo its up") || !strings.Contains(r.errors[0], "labels") {
			t.Fatal(r.errors)
		}
	})
	t.Run("an irreversible migration is where rolling back stops, not a failure", func(t *testing.T) {
		oneWay := &fstest.MapFile{Data: []byte("-- +goose Up\nINSERT INTO posts (id) VALUES (1);\n-- +goose Down\n")}
		if r := migrations(t, fstest.MapFS{"00001_posts.sql": create, "00002_seed.sql": oneWay}); len(r.errors) != 0 {
			t.Fatal(r.errors)
		}
	})
}
