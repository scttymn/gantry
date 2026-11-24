package db

import (
	"database/sql"
	"runtime"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func openPostgres(url string) (*DB, error) {
	pool, err := sql.Open("pgx", url)
	if err != nil {
		return nil, err
	}
	n := max(4, 2*runtime.GOMAXPROCS(0))
	pool.SetMaxOpenConns(n)
	pool.SetMaxIdleConns(n)
	pool.SetConnMaxIdleTime(5 * time.Minute)
	return &DB{Engine: Postgres, Read: pool, Write: pool}, nil
}
