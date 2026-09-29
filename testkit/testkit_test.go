package testkit

import (
	"context"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/scttymn/gantry/db"
)

var schema = fstest.MapFS{"00001_schema.sql": {Data: []byte(`-- +goose Up
CREATE TABLE users (id integer PRIMARY KEY, email text NOT NULL, digest text, admin boolean, created_at datetime NOT NULL, updated_at datetime NOT NULL);
CREATE TABLE settings (name text PRIMARY KEY, value text);
`)}}

func TestFixtures(t *testing.T) {
	d := DB(t, func(ctx context.Context, d *db.DB) error { return d.Migrate(ctx, schema, "app_migrations") })
	ids := Fixtures(t, d, fstest.MapFS{
		"users.yml":    {Data: []byte("one:\n  email: one@example.com\n  digest: DIGEST\n  admin: true\ntwo:\n  email: two@example.com\n  digest: ~\n  admin: false\n")},
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
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL isn't set")
	}
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
