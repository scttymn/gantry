package db

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
)

// Migrate applies the migrations in fsys that haven't run, recording them in
// table. gantry's own packages and the app each keep their own table
// ("gantry_auth_migrations", "app_migrations"), so their numbering never
// collides. (Not "schema_migrations": that's Rails', and an app moved from
// Rails still has it.) A migration that fails is an error, and the process should stop:
// its health check then never passes, and the old version keeps serving.
//
// Migrations are goose's: files named 00001_create_posts.sql at fsys's root
// (pass fs.Sub of an embedded folder), with "-- +goose Up" and
// "-- +goose Down" sections. No migrations at all is fine.
func (d *DB) Migrate(ctx context.Context, fsys fs.FS, table string) error {
	dialect := goose.DialectSQLite3
	if d.Engine == Postgres {
		dialect = goose.DialectPostgres
	}
	p, err := goose.NewProvider(dialect, d.Write, fsys, goose.WithTableName(table), goose.WithDisableGlobalRegistry(true))
	if errors.Is(err, goose.ErrNoMigrations) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("migrations (%s): %w", table, err)
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("migrations (%s): %w", table, err)
	}
	return nil
}
