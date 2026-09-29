package db

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"time"

	"github.com/pressly/goose/v3"
)

// Migrate applies the migrations in fsys that haven't run, recording them in
// table. gantry's own packages and the app each keep their own table
// ("gantry_auth_migrations", "app_migrations"), so their numbering never
// collides. (Not "schema_migrations": that's Rails', and an app moved from
// Rails still has it.) A migration that fails is an error, and the process should stop:
// its health check then never passes, and the old version keeps serving.
//
// Migrations are goose's: files named 20260929120000_create_posts.sql (or
// 00001_create_posts.sql) at fsys's root (pass fs.Sub of an embedded folder),
// with "-- +goose Up" and "-- +goose Down" sections. No migrations at all is
// fine. As Rails, any pending migration runs, older than the newest applied
// or not, so a branch's migration merged after main's still runs.
func (d *DB) Migrate(ctx context.Context, fsys fs.FS, table string) error {
	p, err := d.migrations(fsys, table)
	if errors.Is(err, goose.ErrNoMigrations) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("migrations (%s): %w", table, err)
	}
	return nil
}

// IrreversibleError is a rollback reaching a migration whose down section
// does nothing though its up does something (Rails' IrreversibleMigration). It, and everything before
// it, stays applied.
type IrreversibleError struct{ Name string }

func (e *IrreversibleError) Error() string {
	return fmt.Sprintf("migration %s can't be undone: its down section is empty", e.Name)
}

// Rollback undoes the last steps migrations applied, newest first, and
// returns their file names. It stops, with an *IrreversibleError, at one
// whose down section is empty, before running it. Nothing applied is
// nothing to do.
func (d *DB) Rollback(ctx context.Context, fsys fs.FS, table string, steps int) ([]string, error) {
	p, err := d.migrations(fsys, table)
	if errors.Is(err, goose.ErrNoMigrations) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var done []string
	for range steps {
		version, err := d.lastApplied(ctx, table)
		if err != nil || version == 0 {
			return done, err
		}
		name, reversible, err := downOf(fsys, p, version)
		if err != nil {
			return done, err
		}
		if !reversible {
			return done, &IrreversibleError{Name: name}
		}
		if _, err := p.Down(ctx); err != nil {
			return done, fmt.Errorf("rolling back %s: %w", name, err)
		}
		done = append(done, name)
	}
	return done, nil
}

// Migration is one migration file, and whether it has run.
type Migration struct {
	Version   int64
	Name      string // the file's name
	Applied   bool
	AppliedAt time.Time // zero while pending
}

// Status is every migration in fsys, oldest version first.
func (d *DB) Status(ctx context.Context, fsys fs.FS, table string) ([]Migration, error) {
	p, err := d.migrations(fsys, table)
	if errors.Is(err, goose.ErrNoMigrations) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	st, err := p.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("migrations (%s): %w", table, err)
	}
	out := make([]Migration, len(st))
	for i, s := range st {
		out[i] = Migration{Version: s.Source.Version, Name: path.Base(s.Source.Path), Applied: s.State == goose.StateApplied, AppliedAt: s.AppliedAt}
	}
	return out, nil
}

func (d *DB) migrations(fsys fs.FS, table string) (*goose.Provider, error) {
	dialect := goose.DialectSQLite3
	if d.Engine == Postgres {
		dialect = goose.DialectPostgres
	}
	p, err := goose.NewProvider(dialect, d.Write, fsys, goose.WithTableName(table),
		goose.WithDisableGlobalRegistry(true), goose.WithAllowOutofOrder(true))
	if err != nil && !errors.Is(err, goose.ErrNoMigrations) {
		return nil, fmt.Errorf("migrations (%s): %w", table, err)
	}
	return p, err
}

// lastApplied is the version applied most recently (0: none), the one goose
// rolls back next. table is gantry's or the app's own name, never a
// visitor's.
func (d *DB) lastApplied(ctx context.Context, table string) (int64, error) {
	var v int64
	err := d.Write.QueryRowContext(ctx, `SELECT version_id FROM `+table+` WHERE version_id <> 0 ORDER BY id DESC LIMIT 1`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return v, err
}

// downOf is version's file name, and whether it can be undone: its down
// section has a statement (a line that isn't blank or a comment), or its up
// has none, so there's nothing to undo.
func downOf(fsys fs.FS, p *goose.Provider, version int64) (string, bool, error) {
	for _, s := range p.ListSources() {
		if s.Version != version {
			continue
		}
		f, err := fsys.Open(s.Path)
		if err != nil {
			return "", false, err
		}
		defer f.Close()
		down, upDoes := false, false
		lines := bufio.NewScanner(f)
		for lines.Scan() {
			line := strings.TrimSpace(lines.Text())
			statement := line != "" && !strings.HasPrefix(line, "--")
			switch {
			case strings.HasPrefix(line, "-- +goose Down"):
				down = true
			case strings.HasPrefix(line, "-- +goose Up"):
				down = false
			case down && statement:
				return path.Base(s.Path), true, nil
			case statement:
				upDoes = true
			}
		}
		return path.Base(s.Path), !upDoes, lines.Err()
	}
	return "", false, fmt.Errorf("migration %d was applied but its file is gone", version)
}
