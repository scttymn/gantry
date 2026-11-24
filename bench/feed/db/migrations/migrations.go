package migrations

import (
	"context"
	"embed"

	"github.com/scttymn/gantry/db"
)

//go:embed *.sql
var files embed.FS

var Files = files

const Table = "app_migrations"

func Up(ctx context.Context, d *db.DB) error { return d.Migrate(ctx, files, Table) }

func Rollback(ctx context.Context, d *db.DB, steps int) ([]string, error) {
	return d.Rollback(ctx, files, Table, steps)
}

func Status(ctx context.Context, d *db.DB) ([]db.Migration, error) {
	return d.Status(ctx, files, Table)
}
