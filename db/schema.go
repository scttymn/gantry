package db

import (
	"context"
	"errors"
	"strings"
)

// Schema is the database's schema as SQL: every table, index, view and
// trigger, as SQLite keeps them, tables first. An app writes it to
// db/schema.sql after migrating (as Rails keeps schema.rb), and sqlc reads
// that, rather than replaying every migration. Tables named in skip (the
// migrations' version tables) are left out.
func (d *DB) Schema(ctx context.Context, skip ...string) (string, error) {
	if d.Engine != SQLite {
		return "", errors.New("db: Schema is SQLite's so far; Postgres' comes with pg_dump (batch 6)")
	}
	rows, err := d.Read.QueryContext(ctx, `SELECT type, name, tbl_name, sql FROM sqlite_master
		WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%'
		ORDER BY CASE type WHEN 'table' THEN 0 WHEN 'index' THEN 1 WHEN 'view' THEN 2 ELSE 3 END, tbl_name, name`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	skipped := map[string]bool{}
	for _, s := range skip {
		skipped[s] = true
	}
	var b strings.Builder
	b.WriteString("-- The schema as the migrations leave it, for sqlc. Written from the\n-- migrations (go test ./db/migrations -update); don't edit it by hand.\n\n")
	for rows.Next() {
		var typ, name, table, sql string
		if err := rows.Scan(&typ, &name, &table, &sql); err != nil {
			return "", err
		}
		if skipped[table] {
			continue
		}
		b.WriteString(strings.TrimSpace(sql) + ";\n")
	}
	return b.String(), rows.Err()
}
