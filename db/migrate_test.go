package db

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"testing/fstest"
)

var tagsMigration = &fstest.MapFile{Data: []byte(`-- +goose Up
CREATE TABLE tags (id integer PRIMARY KEY, name text NOT NULL);
-- +goose Down
DROP TABLE tags;
`)}

func twoMigrations() fstest.MapFS {
	return fstest.MapFS{"00001_create_posts.sql": postsMigrations["00001_create_posts.sql"], "20260929120000_create_tags.sql": tagsMigration}
}

func exists(d *DB, table string) bool {
	_, err := d.Read.Exec(`SELECT 1 FROM ` + table)
	return err == nil
}

func TestRollback(t *testing.T) {
	ctx := context.Background()
	t.Run("undoes the last migration, then the one before", func(t *testing.T) {
		d := openTemp(t)
		if err := d.Migrate(ctx, twoMigrations(), "app_migrations"); err != nil {
			t.Fatal(err)
		}
		done, err := d.Rollback(ctx, twoMigrations(), "app_migrations", 1)
		if err != nil || len(done) != 1 || done[0] != "20260929120000_create_tags.sql" {
			t.Fatalf("rolled back %q, err %v", done, err)
		}
		if exists(d, "tags") || !exists(d, "posts") {
			t.Fatal("rolled back the wrong one")
		}
		if done, err := d.Rollback(ctx, twoMigrations(), "app_migrations", 1); err != nil || len(done) != 1 || exists(d, "posts") {
			t.Fatalf("second: %q, %v", done, err)
		}
		if done, err := d.Rollback(ctx, twoMigrations(), "app_migrations", 1); err != nil || len(done) != 0 {
			t.Fatalf("nothing left to roll back: %q, %v", done, err)
		}
	})
	t.Run("steps", func(t *testing.T) {
		d := openTemp(t)
		d.Migrate(ctx, twoMigrations(), "app_migrations")
		if done, err := d.Rollback(ctx, twoMigrations(), "app_migrations", 2); err != nil || len(done) != 2 || exists(d, "posts") || exists(d, "tags") {
			t.Fatalf("%q, %v", done, err)
		}
	})
	t.Run("a migration with no down section stops it, changing nothing", func(t *testing.T) {
		d := openTemp(t)
		oneWay := fstest.MapFS{
			"00001_create_posts.sql": postsMigrations["00001_create_posts.sql"],
			"00002_backfill.sql":     {Data: []byte("-- +goose Up\nINSERT INTO posts (title, created_at) VALUES ('x', '2026-01-01');\n-- +goose Down\n-- nothing to undo\n")},
		}
		d.Migrate(ctx, oneWay, "app_migrations")
		done, err := d.Rollback(ctx, oneWay, "app_migrations", 2)
		var irreversible *IrreversibleError
		if !errors.As(err, &irreversible) || irreversible.Name != "00002_backfill.sql" || len(done) != 0 {
			t.Fatalf("%q, %v", done, err)
		}
		st, _ := d.Status(ctx, oneWay, "app_migrations")
		if !st[1].Applied {
			t.Fatal("the irreversible migration was forgotten")
		}
	})
}

func TestStatus(t *testing.T) {
	ctx := context.Background()
	d := openTemp(t)
	d.Migrate(ctx, postsMigrations, "app_migrations")
	st, err := d.Status(ctx, twoMigrations(), "app_migrations")
	if err != nil || len(st) != 2 {
		t.Fatalf("%+v, %v", st, err)
	}
	if st[0].Version != 1 || st[0].Name != "00001_create_posts.sql" || !st[0].Applied || st[0].AppliedAt.IsZero() {
		t.Errorf("first: %+v", st[0])
	}
	if st[1].Version != 20260929120000 || st[1].Applied || !st[1].AppliedAt.IsZero() {
		t.Errorf("second: %+v", st[1])
	}
}

// Rails runs any pending migration, older than the newest applied or not:
// a branch's migration merged after main's still runs.
func TestMigrateOutOfOrder(t *testing.T) {
	ctx := context.Background()
	d := openTemp(t)
	newer := fstest.MapFS{"20260929120000_create_tags.sql": tagsMigration}
	if err := d.Migrate(ctx, newer, "app_migrations"); err != nil {
		t.Fatal(err)
	}
	if err := d.Migrate(ctx, twoMigrations(), "app_migrations"); err != nil || !exists(d, "posts") {
		t.Fatalf("the older migration didn't run: %v", err)
	}
}

func TestSchemaPostgres(t *testing.T) {
	url := os.Getenv("GANTRY_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("GANTRY_TEST_POSTGRES_URL isn't set")
	}
	if _, err := exec.LookPath("pg_dump"); err != nil {
		t.Skip("pg_dump isn't installed")
	}
	ctx := context.Background()
	d, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.Write.Exec(`DROP TABLE IF EXISTS schema_posts, gantry_schema_test_migrations`)
	pg := fstest.MapFS{"00001_create.sql": {Data: []byte("-- +goose Up\nCREATE TABLE schema_posts (id bigint PRIMARY KEY, title text NOT NULL);\n-- +goose Down\nDROP TABLE schema_posts;\n")}}
	if err := d.Migrate(ctx, pg, "gantry_schema_test_migrations"); err != nil {
		t.Fatal(err)
	}
	s, err := d.Schema(ctx, "gantry_schema_test_migrations")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s, "CREATE TABLE public.schema_posts") || strings.Contains(s, "gantry_schema_test_migrations") || strings.Contains(s, `\restrict`) || strings.Contains(s, "SET ") {
		t.Fatal(s)
	}
	again, _ := d.Schema(ctx, "gantry_schema_test_migrations")
	if again != s {
		t.Fatal("the schema isn't the same twice, so it would churn in git")
	}
}
