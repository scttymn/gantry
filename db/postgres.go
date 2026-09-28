package db

import (
	"database/sql"
	"runtime"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers "pgx"
)

func openPostgres(url string) (*DB, error) {
	pool, err := sql.Open("pgx", url)
	if err != nil {
		return nil, err
	}
	// Sized for a small host: a few connections per CPU is what Postgres
	// serves best, and each one costs the server memory.
	n := max(4, 2*runtime.GOMAXPROCS(0))
	pool.SetMaxOpenConns(n)
	pool.SetMaxIdleConns(n)
	pool.SetConnMaxIdleTime(5 * time.Minute)
	return &DB{Engine: Postgres, Read: pool, Write: pool}, nil
}
