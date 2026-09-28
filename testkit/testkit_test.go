package testkit

import (
	"context"
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
