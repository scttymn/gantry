// Package testkit gives an app's tests a fresh database each, and loads
// fixtures into it: YAML files named for their table, as Rails' are.
//
//	# test/fixtures/programs.yml
//	crossfit:
//	  name: CrossFit
//	  position: 1
package testkit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/scttymn/gantry/db"
)

// DB is an empty SQLite database, brought up to date by migrate (which may
// be nil) and removed after the test.
func DB(t testing.TB, migrate func(context.Context, *db.DB) error) *db.DB {
	t.Helper()
	d, err := db.Open(context.Background(), "sqlite://"+filepath.Join(t.TempDir(), "test.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if migrate != nil {
		if err := migrate(context.Background(), d); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

// IDs maps table, then fixture label, to the row's id.
type IDs map[string]map[string]int64

// Fixtures loads every <table>.yml at fsys's root, the files in name order
// and each file's rows in its order. A table's created_at and updated_at,
// when it has them, default to now. A value equal to a key of replace is
// replaced ("PASSWORD_DIGEST" with a real hash, say). true and false are
// written as 1 and 0, and ~ as NULL.
func Fixtures(t testing.TB, d *db.DB, fsys fs.FS, replace map[string]any) IDs {
	t.Helper()
	ctx := context.Background()
	names, err := fs.Glob(fsys, "*.yml")
	if err != nil {
		t.Fatal(err)
	}
	ids := IDs{}
	now := time.Now().UTC()
	for _, name := range names {
		table := strings.TrimSuffix(name, ".yml")
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			t.Fatal(err)
		}
		var doc yaml.Node
		if err := yaml.Unmarshal(body, &doc); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		ids[table] = map[string]int64{}
		if len(doc.Content) == 0 {
			continue
		}
		columns := columnsOf(t, d, table)
		rows := doc.Content[0]
		for i := 0; i+1 < len(rows.Content); i += 2 {
			label, fields := rows.Content[i].Value, rows.Content[i+1]
			row := map[string]any{}
			for _, c := range []string{"created_at", "updated_at"} {
				if slices.Contains(columns, c) {
					row[c] = now
				}
			}
			for k := 0; k+1 < len(fields.Content); k += 2 {
				col, node := fields.Content[k].Value, fields.Content[k+1]
				var v any = node.Value
				switch {
				case node.Tag == "!!bool":
					v = map[string]int{"true": 1, "false": 0}[node.Value]
				case node.Tag == "!!null":
					v = nil
				default:
					if r, ok := replace[node.Value]; ok {
						v = r
					}
				}
				row[col] = v
			}
			cols := make([]string, 0, len(row))
			for c := range row {
				cols = append(cols, c)
			}
			slices.Sort(cols)
			quoted, marks, vals := make([]string, len(cols)), make([]string, len(cols)), make([]any, len(cols))
			for k, c := range cols {
				quoted[k], marks[k], vals[k] = `"`+c+`"`, "$"+strconv.Itoa(k+1), row[c]
			}
			q := fmt.Sprintf(`INSERT INTO %q (%s) VALUES (%s)`, table, strings.Join(quoted, ", "), strings.Join(marks, ", "))
			if !slices.Contains(columns, "id") {
				if _, err := d.Write.ExecContext(ctx, q, vals...); err != nil {
					t.Fatalf("fixture %s.%s: %v", table, label, err)
				}
				continue
			}
			var id int64
			if err := d.Write.QueryRowContext(ctx, q+" RETURNING id", vals...).Scan(&id); err != nil {
				t.Fatalf("fixture %s.%s: %v", table, label, err)
			}
			ids[table][label] = id
		}
	}
	return ids
}

// columnsOf is a table's column names, on either engine.
func columnsOf(t testing.TB, d *db.DB, table string) []string {
	t.Helper()
	rows, err := d.Read.Query(fmt.Sprintf(`SELECT * FROM %q LIMIT 0`, table))
	if err != nil {
		t.Fatalf("fixtures for %s: %v", table, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	return cols
}

// File is a fixture file's bytes, from fsys (test/fixtures/files, say).
func File(t testing.TB, fsys fs.FS, name string) []byte {
	t.Helper()
	b, err := fs.ReadFile(fsys, path.Clean(name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Postgres is DB on Postgres: a new database on the server at
// TEST_DATABASE_URL (a user that may create databases), brought up to date
// by migrate (which may be nil), and dropped after the test. So each test,
// in parallel or not, has a database of its own, as DB's SQLite file is.
// Without TEST_DATABASE_URL the test fails: a Postgres app's queries don't
// run on SQLite.
func Postgres(t testing.TB, migrate func(context.Context, *db.DB) error) *db.DB {
	t.Helper()
	server := os.Getenv("TEST_DATABASE_URL")
	if server == "" {
		t.Fatal("TEST_DATABASE_URL isn't set: a Postgres server for the tests' databases (gantry test sets it)")
	}
	ctx := context.Background()
	admin, err := db.Open(ctx, server)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	raw := make([]byte, 6)
	rand.Read(raw)
	name := "test_" + hex.EncodeToString(raw)
	if _, err := admin.Write.ExecContext(ctx, `CREATE DATABASE `+name); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(server)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	d, err := db.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		d.Close()
		if admin, err := db.Open(context.Background(), server); err == nil {
			admin.Write.Exec(`DROP DATABASE IF EXISTS ` + name + ` WITH (FORCE)`)
			admin.Close()
		}
	})
	if migrate != nil {
		if err := migrate(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	return d
}
