// Package migrations is the app's schema changes, embedded in the binary and
// applied at start: gantry g migration writes them. db/schema.sql is the
// schema they leave, for sqlc and for reading; gantry db migrate rewrites it.
package migrations

import (
	"context"
	"embed"

	"github.com/scttymn/gantry/db"
)

//go:embed *.sql
var files embed.FS

// Files are the migrations.
var Files = files

// Table records which migrations have run.
const Table = "app_migrations"

// Up applies the migrations that haven't run.
func Up(ctx context.Context, d *db.DB) error { return d.Migrate(ctx, files, Table) }

// Rollback undoes the last steps migrations, and names them.
func Rollback(ctx context.Context, d *db.DB, steps int) ([]string, error) {
	return d.Rollback(ctx, files, Table, steps)
}

// Status is every migration, and whether it ran.
func Status(ctx context.Context, d *db.DB) ([]db.Migration, error) {
	return d.Status(ctx, files, Table)
}
