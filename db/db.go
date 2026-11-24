// Package db opens an app's database, SQLite or Postgres, chosen by its URL,
// and brings it up to date with migrations embedded in the binary.
//
// A DB has two pools. On SQLite, Write is a single connection, so writes
// queue in the app rather than fail on SQLite's lock, and Read is several
// read-only connections that WAL lets run beside the writer. On Postgres both
// are the same pool. Queries say which they need: models.New(d.Read) for a
// page, d.Tx for a change.
//
// A build with cgo, as an app's is, has SQLite compiled from C through
// gantry's own small binding, made to answer as the translated build's
// driver (modernc.org/sqlite) does: its arguments, scripts, times, values
// and errors are that driver's, and the tests that pin them run on both
// builds. It opens a connection without SQLite's own lock (database/sql
// lets one goroutine at a time use a connection), has SQLite count no
// allocations under a lock all connections share, and reads a row in one
// call from Go to C. It needs SQLite compiled with the options an app's
// Dockerfile sets (the build tags and CGO_CFLAGS in internal/pins), so an
// app's SQL means the same in both builds; without them it doesn't start.
// Any other build (the gantry CLI, a cgo build without the options) has
// SQLite translated from C to Go, which has them all.
//
// On SQLite, every connection has WAL (readers never wait for the writer),
// a busy timeout (a connection waits for a lock instead of failing),
// foreign keys enforced, synchronous NORMAL and temp_store MEMORY (a
// production image has no /tmp for SQLite's temporary files), and the read
// pool's are query_only: a write there is an error. Each connection's
// transactions are BEGIN IMMEDIATE: one takes
// the write lock when it starts, not part-way through when it first writes.
// Times are written in UTC, as "2006-01-02 15:04:05.999999999+00:00": text
// that sorts in time order, and that SQLite's date functions read. Text
// without a zone (Rails' "2006-01-02 15:04:05.000000") reads as UTC, and
// sorts correctly among these. The same instant in the two formats isn't
// the same text, though: compare such columns by range, or with
// julianday(), not with =.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Engine is the database behind a DB.
type Engine string

const (
	SQLite   Engine = "sqlite"
	Postgres Engine = "postgres"
)

// DB is an open database.
type DB struct {
	Engine Engine
	Read   *sql.DB
	Write  *sql.DB

	versioner *versioner
	url       string
}

// Querier is a pool or a transaction: what a query needs to run.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Open opens the database at url:
//
//	sqlite:///data/app.sqlite3   (an absolute path)
//	sqlite:tmp/app.sqlite3       (a relative one)
//	postgres://user:pass@host:5432/name?sslmode=disable
//
// It checks the database answers, so a wrong URL fails here, at start.
func Open(ctx context.Context, url string) (*DB, error) {
	var (
		d   *DB
		err error
	)
	switch {
	case strings.HasPrefix(url, "sqlite:"):
		d, err = openSQLite(strings.TrimPrefix(strings.TrimPrefix(url, "sqlite:"), "//"))
	case strings.HasPrefix(url, "postgres://"), strings.HasPrefix(url, "postgresql://"):
		d, err = openPostgres(url)
	default:
		return nil, fmt.Errorf("database URL %q: it starts with sqlite: or postgres://", redact(url))
	}
	if err != nil {
		return nil, fmt.Errorf("database %s: %w", redact(url), err)
	}
	if err := d.Write.PingContext(ctx); err != nil {
		d.Close()
		return nil, fmt.Errorf("database %s: %w", redact(url), err)
	}
	d.url = url
	return d, nil
}

// Close closes both pools.
func (d *DB) Close() error {
	if d.versioner != nil {
		d.versioner.mu.Lock()
		if d.versioner.conn != nil {
			d.versioner.conn.Close()
			d.versioner.conn = nil
		}
		d.versioner.mu.Unlock()
	}
	if d.Read == d.Write {
		return d.Write.Close()
	}
	return errors.Join(d.Write.Close(), d.Read.Close())
}

// Tx runs fn in a transaction on the write pool, committed if fn returns
// nil and rolled back otherwise (an error or a panic, which goes on up), then
// runs what fn asked to run after the commit (AfterCommit). On SQLite it
// begins IMMEDIATE, so it holds the write lock from the start and can't fail
// partway on a busy database.
func (d *DB) Tx(ctx context.Context, fn func(*Tx) error) error {
	sqlTx, err := d.Write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer sqlTx.Rollback()
	tx := &Tx{Tx: sqlTx}
	if err := fn(tx); err != nil {
		return err
	}
	if err := sqlTx.Commit(); err != nil {
		return err
	}
	for _, f := range tx.after {
		f()
	}
	return nil
}

// Tx is a transaction: Go's (so sqlc's queries take it as they are), and
// what's to run once it commits.
type Tx struct {
	*sql.Tx
	after []func()
}

// AfterCommit runs f once the transaction commits, after any registered
// before it, and never if it rolls back (Rails' after_commit): sending the
// email, enqueuing the job, telling the pages. The change is saved by then,
// so f can't undo it; it logs its own failure.
func (t *Tx) AfterCommit(f func()) { t.after = append(t.after, f) }

func redact(url string) string {
	scheme, rest, ok := strings.Cut(url, "://")
	if !ok {
		return url
	}
	userinfo, host, ok := strings.Cut(rest, "@")
	if !ok {
		return url
	}
	if user, _, hasPass := strings.Cut(userinfo, ":"); hasPass {
		return scheme + "://" + user + ":xxxxx@" + host
	}
	return url
}
