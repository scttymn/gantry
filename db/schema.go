package db

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Schema is the database's schema as SQL: every table, index, view and
// trigger, as SQLite keeps them, tables first; on Postgres, pg_dump's
// (Rails' structure.sql), which needs pg_dump installed. An app writes it to
// db/schema.sql after migrating (as Rails keeps schema.rb), and sqlc reads
// that, rather than replaying every migration. Tables named in skip (the
// migrations' version tables) are left out, and so are gantry's own
// (gantry_settings, where sign keeps its key), which aren't the app's.
func (d *DB) Schema(ctx context.Context, skip ...string) (string, error) {
	if d.Engine == Postgres {
		return d.pgSchema(ctx, skip)
	}
	rows, err := d.Read.QueryContext(ctx, `SELECT type, name, tbl_name, sql FROM sqlite_master
		WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%' AND tbl_name NOT LIKE 'gantry\_%' ESCAPE '\'
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
	b.WriteString("-- The schema as the migrations leave it, for sqlc and for reading. It's\n-- written from the migrations; don't edit it by hand.\n\n")
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

// pgSchema is pg_dump's schema, without what changes from run to run or
// machine to machine (its comments, SET lines, and pg_dump 17's \restrict
// keys), so db/schema.sql only changes when the schema does.
func (d *DB) pgSchema(ctx context.Context, skip []string) (string, error) {
	args := []string{"--schema-only", "--no-owner", "--no-privileges", "--no-comments", "--exclude-table=gantry_*"}
	for _, s := range skip {
		args = append(args, "--exclude-table="+s)
	}
	cmd := exec.CommandContext(ctx, "pg_dump", append(args, "--dbname="+d.url)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("pg_dump: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var b strings.Builder
	b.WriteString("-- The schema as the migrations leave it, for sqlc and for reading. It's\n-- written from the migrations by pg_dump; don't edit it by hand.\n")
	blank := true
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "--"), strings.HasPrefix(line, "SET "), strings.HasPrefix(line, "SELECT pg_catalog.set_config"),
			strings.HasPrefix(line, `\restrict`), strings.HasPrefix(line, `\unrestrict`):
			continue
		case strings.TrimSpace(line) == "":
			if blank {
				continue
			}
			blank = true
		default:
			blank = false
		}
		b.WriteString(line + "\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n", nil
}
