package testkit

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/scttymn/gantry/db"
)

// Migrations checks an app's migrations on a fresh database: every one up,
// then each down, newest first, then every one up again. A down section
// that fails, or that doesn't leave the schema as it was before its up
// (a table it forgot to drop), fails the test. Rolling back stops at a
// migration that can't be undone (an empty down section), as Rails' does.
func Migrations(t testing.TB, fsys fs.FS, table string) {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, "sqlite://"+filepath.Join(t.TempDir(), "migrations.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	st, err := d.Status(ctx, fsys, table)
	if err != nil {
		t.Fatal(err)
	}
	// The schema before each migration, and once they've all run: each is
	// applied alone, by migrating a copy of fsys that ends at it.
	before := make([]string, 0, len(st)+1)
	upTo := fstest.MapFS{}
	for _, m := range st {
		before = append(before, schemaOf(t, d, table))
		b, err := fs.ReadFile(fsys, m.Name)
		if err != nil {
			t.Fatal(err)
		}
		upTo[m.Name] = &fstest.MapFile{Data: b}
		if err := d.Migrate(ctx, upTo, table); err != nil {
			t.Fatalf("%s: %v", m.Name, err)
		}
	}
	all := schemaOf(t, d, table)
	for i := len(st) - 1; i >= 0; i-- {
		done, err := d.Rollback(ctx, fsys, table, 1)
		var irreversible *db.IrreversibleError
		if errors.As(err, &irreversible) {
			break
		}
		if err != nil {
			t.Errorf("%s: its down section fails: %v", st[i].Name, err)
			return
		}
		if got := schemaOf(t, d, table); len(done) == 1 && got != before[i] {
			t.Errorf("%s: its down section doesn't undo its up. The schema before it:\n%s\nafter its down:\n%s", st[i].Name, before[i], got)
			return
		}
	}
	if err := d.Migrate(ctx, fsys, table); err != nil {
		t.Errorf("migrating again after rolling back: %v", err)
		return
	}
	if got := schemaOf(t, d, table); got != all {
		t.Errorf("migrating again leaves a different schema:\n%s\nwant\n%s", got, all)
	}
}

func schemaOf(t testing.TB, d *db.DB, table string) string {
	t.Helper()
	s, err := d.Schema(context.Background(), table)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
