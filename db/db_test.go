package db

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

func openTemp(t *testing.T) *DB {
	t.Helper()
	d, err := Open(context.Background(), "sqlite://"+filepath.Join(t.TempDir(), "sub", "test.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

var postsMigrations = fstest.MapFS{
	"00001_create_posts.sql": {Data: []byte(`-- +goose Up
CREATE TABLE posts (id integer PRIMARY KEY, title text NOT NULL, created_at datetime NOT NULL);
-- +goose Down
DROP TABLE posts;
`)},
}

func TestOpen(t *testing.T) {
	t.Run("a sqlite URL opens the file, making its folder", func(t *testing.T) {
		d := openTemp(t)
		if d.Engine != SQLite || d.Read == d.Write {
			t.Fatalf("engine %s, pools shared %v", d.Engine, d.Read == d.Write)
		}
	})
	t.Run("a relative sqlite path", func(t *testing.T) {
		dir := t.TempDir()
		wd, _ := os.Getwd()
		os.Chdir(dir)
		defer os.Chdir(wd)
		d, err := Open(context.Background(), "sqlite:rel/app.sqlite3")
		if err != nil {
			t.Fatal(err)
		}
		d.Close()
		if _, err := os.Stat(filepath.Join(dir, "rel", "app.sqlite3")); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("any other scheme is refused", func(t *testing.T) {
		_, err := Open(context.Background(), "mysql://u:p@h/db")
		if err == nil || !strings.Contains(err.Error(), "sqlite: or postgres://") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a password never reaches an error", func(t *testing.T) {
		_, err := Open(context.Background(), "postgres://app:hunter2@127.0.0.1:1/app?connect_timeout=1")
		if err == nil || strings.Contains(err.Error(), "hunter2") || !strings.Contains(err.Error(), "app:xxxxx@") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("sqlite is set up: WAL, foreign keys, one writer", func(t *testing.T) {
		d := openTemp(t)
		var mode string
		var fk int
		d.Read.QueryRow(`PRAGMA journal_mode`).Scan(&mode)
		d.Read.QueryRow(`PRAGMA foreign_keys`).Scan(&fk)
		if mode != "wal" || fk != 1 {
			t.Fatalf("journal_mode %q, foreign_keys %d", mode, fk)
		}
		if n := d.Write.Stats().MaxOpenConnections; n != 1 {
			t.Fatalf("writer connections: %d", n)
		}
	})
	t.Run("the read pool can't write", func(t *testing.T) {
		d := openTemp(t)
		if err := d.Migrate(context.Background(), postsMigrations, "app_migrations"); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Read.Exec(`INSERT INTO posts (title, created_at) VALUES ('x', ?)`, time.Now()); err == nil {
			t.Fatal("a write through Read succeeded")
		}
	})
}

func TestMigrate(t *testing.T) {
	ctx := context.Background()
	t.Run("applies each migration once, recorded in the named table", func(t *testing.T) {
		d := openTemp(t)
		for range 2 {
			if err := d.Migrate(ctx, postsMigrations, "app_migrations"); err != nil {
				t.Fatal(err)
			}
		}
		var n int
		if err := d.Read.QueryRow(`SELECT COUNT(*) FROM app_migrations WHERE version_id = 1`).Scan(&n); err != nil || n != 1 {
			t.Fatalf("recorded %d times, err %v", n, err)
		}
	})
	t.Run("two tables keep separate numbering", func(t *testing.T) {
		d := openTemp(t)
		other := fstest.MapFS{"00001_create_tags.sql": {Data: []byte("-- +goose Up\nCREATE TABLE tags (id integer PRIMARY KEY);\n")}}
		if err := d.Migrate(ctx, postsMigrations, "app_migrations"); err != nil {
			t.Fatal(err)
		}
		if err := d.Migrate(ctx, other, "gantry_test_migrations"); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Read.Exec(`SELECT 1 FROM tags`); err != nil {
			t.Fatal("the second table's migration 1 didn't run:", err)
		}
	})
	t.Run("no migrations is fine", func(t *testing.T) {
		if err := openTemp(t).Migrate(ctx, fstest.MapFS{}, "app_migrations"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a failing migration is an error and leaves nothing behind", func(t *testing.T) {
		d := openTemp(t)
		bad := fstest.MapFS{"00001_bad.sql": {Data: []byte("-- +goose Up\nCREATE TABLE ok (id integer);\nNOT SQL;\n")}}
		if err := d.Migrate(ctx, bad, "app_migrations"); err == nil {
			t.Fatal("no error")
		}
		if _, err := d.Read.Exec(`SELECT 1 FROM ok`); err == nil {
			t.Fatal("half a migration was kept")
		}
	})
}

func TestTx(t *testing.T) {
	ctx := context.Background()
	d := openTemp(t)
	if err := d.Migrate(ctx, postsMigrations, "app_migrations"); err != nil {
		t.Fatal(err)
	}
	insert := func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO posts (title, created_at) VALUES ('x', ?)`, time.Now())
		return err
	}
	t.Run("an error rolls back", func(t *testing.T) {
		boom := errors.New("boom")
		err := d.Tx(ctx, func(tx *sql.Tx) error {
			if err := insert(tx); err != nil {
				return err
			}
			return boom
		})
		var n int
		d.Read.QueryRow(`SELECT COUNT(*) FROM posts`).Scan(&n)
		if !errors.Is(err, boom) || n != 0 {
			t.Fatalf("err %v, rows %d", err, n)
		}
	})
	t.Run("concurrent writers queue instead of failing", func(t *testing.T) {
		var wg sync.WaitGroup
		errs := make(chan error, 50)
		for range 50 {
			wg.Go(func() { errs <- d.Tx(ctx, insert) })
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		var n int
		d.Read.QueryRow(`SELECT COUNT(*) FROM posts`).Scan(&n)
		if n != 50 {
			t.Fatalf("rows %d", n)
		}
	})
}

func TestTimes(t *testing.T) {
	ctx := context.Background()
	d := openTemp(t)
	if err := d.Migrate(ctx, postsMigrations, "app_migrations"); err != nil {
		t.Fatal(err)
	}
	chicago, _ := time.LoadLocation("America/Chicago")
	at := time.Date(2026, 9, 27, 10, 30, 0, 123456000, chicago)
	t.Run("written in UTC, as text that sorts", func(t *testing.T) {
		if _, err := d.Write.Exec(`INSERT INTO posts (id, title, created_at) VALUES (1, 'go', ?)`, at); err != nil {
			t.Fatal(err)
		}
		var raw string
		d.Read.QueryRow(`SELECT CAST(created_at AS text) FROM posts WHERE id = 1`).Scan(&raw)
		if raw != "2026-09-27 15:30:00.123456+00:00" {
			t.Fatalf("stored %q", raw)
		}
	})
	t.Run("Rails' format reads back as UTC", func(t *testing.T) {
		d.Write.Exec(`INSERT INTO posts (id, title, created_at) VALUES (2, 'rails', '2026-09-27 15:30:00.123456')`)
		var got time.Time
		if err := d.Read.QueryRow(`SELECT created_at FROM posts WHERE id = 2`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if !got.Equal(at) || got.Location() != time.UTC {
			t.Fatalf("read %v", got)
		}
	})
	t.Run("the two formats order correctly together", func(t *testing.T) {
		d.Write.Exec(`INSERT INTO posts (id, title, created_at) VALUES (10, 'go', ?)`, at)
		d.Write.Exec(`INSERT INTO posts (id, title, created_at) VALUES (11, 'rails', '2026-09-27 15:30:00.200000')`)
		d.Write.Exec(`INSERT INTO posts (id, title, created_at) VALUES (12, 'go', ?)`, at.Add(time.Second))
		d.Write.Exec(`INSERT INTO posts (id, title, created_at) VALUES (13, 'rails', '2026-09-27 15:30:01.500000')`)
		d.Write.Exec(`INSERT INTO posts (id, title, created_at) VALUES (14, 'go', ?)`, at.Add(2*time.Second).Truncate(time.Second))
		var ids []int
		rows, _ := d.Read.Query(`SELECT id FROM posts WHERE id >= 10 ORDER BY created_at`)
		for rows.Next() {
			var id int
			rows.Scan(&id)
			ids = append(ids, id)
		}
		rows.Close()
		if want := []int{10, 11, 12, 13, 14}; !equal(ids, want) {
			t.Fatalf("order %v, want %v", ids, want)
		}
	})
}

func TestPostgres(t *testing.T) {
	url := os.Getenv("GANTRY_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("GANTRY_TEST_POSTGRES_URL isn't set")
	}
	ctx := context.Background()
	d, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.Write.Exec(`DROP TABLE IF EXISTS posts, gantry_pg_test_migrations`)
	pg := fstest.MapFS{"00001_create_posts.sql": {Data: []byte("-- +goose Up\nCREATE TABLE posts (id bigint GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY, title text NOT NULL);\n")}}
	if err := d.Migrate(ctx, pg, "gantry_pg_test_migrations"); err != nil {
		t.Fatal(err)
	}
	if err := d.Tx(ctx, func(tx *sql.Tx) error { _, err := tx.Exec(`INSERT INTO posts (title) VALUES ($1)`, "x"); return err }); err != nil {
		t.Fatal(err)
	}
	if d.Engine != Postgres || d.Read != d.Write {
		t.Fatal("postgres should share one pool")
	}
}

func equal(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// gantry's own SQL is written once, with $1-style placeholders: Postgres'
// own, and on SQLite they bind by position too, in any order.
func TestDollarPlaceholders(t *testing.T) {
	d := openTemp(t)
	d.Write.Exec(`CREATE TABLE s (name text PRIMARY KEY, value text)`)
	if _, err := d.Write.Exec(`INSERT INTO s (name, value) VALUES ($1, $2) ON CONFLICT (name) DO NOTHING`, "a", "one"); err != nil {
		t.Fatal(err)
	}
	var v string
	if err := d.Read.QueryRow(`SELECT value FROM s WHERE name = $1 AND $2 = 'x'`, "a", "x").Scan(&v); err != nil || v != "one" {
		t.Fatal(v, err)
	}
	if err := d.Read.QueryRow(`SELECT $2 || $1 || $2`, "b", "a").Scan(&v); err != nil || v != "aba" {
		t.Fatal(v, err)
	}
}

func TestSchema(t *testing.T) {
	d := openTemp(t)
	if err := d.Migrate(context.Background(), postsMigrations, "app_migrations"); err != nil {
		t.Fatal(err)
	}
	d.Write.Exec(`CREATE INDEX posts_by_title ON posts (title)`)
	d.Write.Exec(`CREATE TABLE gantry_settings (name text PRIMARY KEY, value text NOT NULL)`)
	s, err := d.Schema(context.Background(), "app_migrations")
	if err != nil {
		t.Fatal(err)
	}
	table := strings.Index(s, "CREATE TABLE posts")
	index := strings.Index(s, "CREATE INDEX posts_by_title")
	if table < 0 || index < table || strings.Contains(s, "app_migrations") || strings.Contains(s, "sqlite_") || strings.Contains(s, "gantry_settings") {
		t.Fatal(s)
	}
}

func TestVersion(t *testing.T) {
	ctx := context.Background()
	d := openTemp(t)
	if err := d.Migrate(ctx, postsMigrations, "app_migrations"); err != nil {
		t.Fatal(err)
	}
	first, err := d.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("reading changes nothing", func(t *testing.T) {
		d.Read.Exec(`SELECT COUNT(*) FROM posts`)
		if again, _ := d.Version(ctx); again != first {
			t.Fatalf("%d, then %d", first, again)
		}
	})
	t.Run("a write changes it", func(t *testing.T) {
		if _, err := d.Write.Exec(`INSERT INTO posts (title, created_at) VALUES ('x', ?)`, time.Now()); err != nil {
			t.Fatal(err)
		}
		if after, _ := d.Version(ctx); after == first {
			t.Fatal("unchanged after a write")
		}
	})
	t.Run("so does a transaction, once it commits", func(t *testing.T) {
		before, _ := d.Version(ctx)
		d.Tx(ctx, func(tx *sql.Tx) error {
			tx.Exec(`INSERT INTO posts (title, created_at) VALUES ('y', ?)`, time.Now())
			if during, _ := d.Version(ctx); during != before {
				t.Error("changed before the commit")
			}
			return nil
		})
		if after, _ := d.Version(ctx); after == before {
			t.Fatal("unchanged after the commit")
		}
	})
	t.Run("another process's write counts too", func(t *testing.T) {
		before, _ := d.Version(ctx)
		other, err := Open(ctx, "sqlite://"+dbPath(t, d))
		if err != nil {
			t.Fatal(err)
		}
		defer other.Close()
		other.Write.Exec(`INSERT INTO posts (title, created_at) VALUES ('z', ?)`, time.Now())
		if after, _ := d.Version(ctx); after == before {
			t.Fatal("unchanged after another connection's write")
		}
	})
}

// dbPath is the file behind d.
func dbPath(t *testing.T, d *DB) string {
	t.Helper()
	var seq int
	var name, file string
	if err := d.Read.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &file); err != nil {
		t.Fatal(err)
	}
	return file
}
