// Package seeds is the data a fresh database starts with (Rails' db/seeds):
// run at start and by gantry db seed, so it must be safe to run again.
package seeds

import (
	"context"
	"os"

	"github.com/scttymn/gantry/db"
)

// Run copies the challenge's seed database (SEED_DB, or db/seeds/feed.db:
// 50k users, 500k posts, 2M likes) into an empty one, and leaves one with
// users alone. With no seed file it does nothing.
func Run(ctx context.Context, d *db.DB) error {
	path := os.Getenv("SEED_DB")
	if path == "" {
		path = "db/seeds/feed.db"
	}
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	var users int
	if err := d.Read.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT 1 FROM users LIMIT 1)`).Scan(&users); err != nil || users > 0 {
		return err
	}
	// One connection throughout: the seed is attached to it, not to the pool.
	conn, err := d.Write.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `ATTACH DATABASE ? AS seed`, path); err != nil {
		return err
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), `DETACH DATABASE seed`)
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{"users", "posts", "likes"} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO `+table+` SELECT * FROM seed.`+table); err != nil {
			return err
		}
	}
	return tx.Commit()
}
