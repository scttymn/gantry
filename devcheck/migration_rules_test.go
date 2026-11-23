package devcheck

import "testing"

func TestMigrationDown(t *testing.T) {
	root := t.TempDir()
	put(t, root, "db/migrations/00001_start.sql", "-- +goose Up\nCREATE TABLE posts (id integer);\n\n-- +goose Down\nDROP TABLE posts;\n")
	put(t, root, "db/migrations/00002_backfill.sql", "-- +goose Up\nUPDATE posts SET id = id;\n\n-- +goose Down\n-- What undoes it. Left empty, the migration can't be rolled back.\n")
	put(t, root, "db/migrations/00003_titles.sql", "-- +goose Up\nALTER TABLE posts ADD COLUMN title text;\n")
	put(t, root, "db/migrations/00004_mentions.sql", "-- +goose Up\n-- the -- +goose Down section is missing\nCREATE TABLE mentions (id integer);\n")
	put(t, root, "app/models/posts.sql", "-- name: Posts :many\nSELECT * FROM posts;\n")
	put(t, root, "db/schema.sql", "CREATE TABLE posts (id integer);\n")
	got, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].File != "db/migrations/00003_titles.sql" || got[1].File != "db/migrations/00004_mentions.sql" {
		t.Fatal(got)
	}
	for _, w := range got {
		if w.Rule != "migration-down" || w.Severity != Violation || w.Line != 1 {
			t.Fatal(w)
		}
	}

	put(t, root, ".gantry-check.json", `{"exceptions":[{"rule":"migration-down","file":"db/migrations/00003_titles.sql","reason":"Applied everywhere before Down sections were required."}]}`)
	got, err = Scan(root)
	if err != nil || len(got) != 1 || got[0].File != "db/migrations/00004_mentions.sql" {
		t.Fatal(got, err)
	}
}
