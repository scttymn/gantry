package testkit

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/internal/testenv"
)

var schema = fstest.MapFS{"00001_schema.sql": {Data: []byte(`-- +goose Up
CREATE TABLE users (id integer PRIMARY KEY, email text NOT NULL, digest text, admin boolean, created_at datetime NOT NULL, updated_at datetime NOT NULL);
CREATE TABLE settings (name text PRIMARY KEY, value text);
`)}}

func TestFixtures(t *testing.T) {
	d := DB(t, func(ctx context.Context, d *db.DB) error { return d.Migrate(ctx, schema, "app_migrations") })
	ids := Fixtures(t, d, fstest.MapFS{
		"users.yml":    {Data: []byte("one:\n  email: one@example.com\n  digest: DIGEST\n  admin: true\ntwo:\n  email: two@example.com\n  digest: ~\n  admin: false\nthree:\n  email: three@example.com\n  admin: True\nfour:\n  email: four@example.com\n  admin: FALSE\n")},
		"settings.yml": {Data: []byte("theme:\n  name: theme\n  value: dark\n")},
		"empty.yml":    {Data: []byte("")},
		"README.md":    {Data: []byte("not a fixture")},
	}, map[string]any{"DIGEST": "hashed"})
	if ids["users"]["one"] == 0 || ids["users"]["two"] <= ids["users"]["one"] {
		t.Fatalf("ids %v", ids)
	}
	var digest string
	var admin bool
	var created time.Time
	d.Read.QueryRow(`SELECT digest, admin, created_at FROM users WHERE id = $1`, ids["users"]["one"]).Scan(&digest, &admin, &created)
	if digest != "hashed" || !admin || time.Since(created) > time.Minute {
		t.Errorf("row: %q %v %v", digest, admin, created)
	}
	for label, want := range map[string]bool{"two": false, "three": true, "four": false} {
		var admin bool
		if err := d.Read.QueryRow(`SELECT admin FROM users WHERE id = $1`, ids["users"][label]).Scan(&admin); err != nil || admin != want {
			t.Errorf("%s: admin %v, want %v (%v)", label, admin, want, err)
		}
	}
	var null *string
	d.Read.QueryRow(`SELECT digest FROM users WHERE id = $1`, ids["users"]["two"]).Scan(&null)
	if null != nil {
		t.Error("~ isn't NULL")
	}
	var value string
	if d.Read.QueryRow(`SELECT value FROM settings WHERE name = 'theme'`).Scan(&value); value != "dark" {
		t.Error("a table without id or timestamps")
	}
}

func TestPostgres(t *testing.T) {
	t.Setenv("TEST_DATABASE_URL", testenv.Postgres(t))
	var names []string
	for range 2 {
		d := Postgres(t, func(ctx context.Context, d *db.DB) error {
			_, err := d.Write.ExecContext(ctx, `CREATE TABLE posts (id bigint PRIMARY KEY)`)
			return err
		})
		var name string
		if err := d.Read.QueryRow(`SELECT current_database()`).Scan(&name); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Write.Exec(`INSERT INTO posts VALUES (1)`); err != nil {
			t.Fatal("each test's database starts empty:", err)
		}
		names = append(names, name)
	}
	if names[0] == names[1] || !strings.HasPrefix(names[0], "test_") {
		t.Fatalf("databases %q", names)
	}
}

func TestPostgresFixtures(t *testing.T) {
	t.Setenv("TEST_DATABASE_URL", testenv.Postgres(t))
	d := Postgres(t, func(ctx context.Context, d *db.DB) error {
		_, err := d.Write.ExecContext(ctx, `CREATE TABLE posts (id bigserial PRIMARY KEY, title text NOT NULL, published boolean NOT NULL, created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL)`)
		return err
	})
	ids := Fixtures(t, d, fstest.MapFS{
		"posts.yml": {Data: []byte("draft:\n  title: Draft\n  published: false\nlive:\n  title: Live\n  published: true\nshouted:\n  title: Shouted\n  published: TRUE\nhidden:\n  title: Hidden\n  published: False\n")},
	}, nil)
	for label, want := range map[string]bool{"draft": false, "live": true, "shouted": true, "hidden": false} {
		var published bool
		if err := d.Read.QueryRow(`SELECT published FROM posts WHERE id = $1`, ids["posts"][label]).Scan(&published); err != nil || published != want {
			t.Errorf("%s: published %v, want %v (%v)", label, published, want, err)
		}
	}
}
