package db

import (
	"context"
	"database/sql"
	"errors"
	"hash/fnv"
	"sync"
)

// ErrNoVersion: this engine can't tell when data changed. Neither SQLite
// nor Postgres says so now; it stays for callers that check for it.
var ErrNoVersion = errors.New("db: no data version on this engine")

type versioner struct {
	mu   sync.Mutex
	conn *sql.Conn
}

// Version is a number that changes whenever the database's data does. Keep
// it, and when a later call gives a different one, anything derived from
// the data (a cached page, say) is stale.
func (d *DB) Version(ctx context.Context) (int64, error) {
	switch d.Engine {
	case Postgres:
		return d.postgresVersion(ctx)
	case SQLite:
	default:
		return 0, ErrNoVersion
	}
	v := d.versioner
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.conn == nil {
		conn, err := d.Read.Conn(context.Background())
		if err != nil {
			return 0, err
		}
		v.conn = conn
	}
	var n int64
	if err := v.conn.QueryRowContext(ctx, `PRAGMA data_version`).Scan(&n); err != nil {
		v.conn.Close()
		v.conn = nil
		return 0, err
	}
	return n, nil
}

func (d *DB) postgresVersion(ctx context.Context) (int64, error) {
	var snapshot string
	if err := d.Read.QueryRowContext(ctx, `SELECT pg_current_snapshot()::text`).Scan(&snapshot); err != nil {
		return 0, err
	}
	h := fnv.New64a()
	h.Write([]byte(snapshot))
	return int64(h.Sum64()), nil
}
