package db

import (
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
)

// SQLite is compiled from C (sqlite_c.go) when the build has cgo and what
// that C must be compiled with, as a gantry app's Dockerfile has them: it
// runs an app's queries in about half the CPU. Any other build (the gantry
// CLI, cross-compiled; bin/go; an app whose Dockerfile predates this; a cgo
// build without the options) has SQLite translated from C to Go
// (sqlite_go.go), which has them all. The build tags select the C build,
// and the C flags it needs too are checked when it starts (sqliteCOptions).
// Either way the connections are set up the same, the SQL an app can use
// is the same, and the tests are the same tests.

// sqlitePragmas are set on every connection:
//   - WAL, so readers never wait for the writer
//   - busy_timeout, so a connection waits for a lock instead of failing
//   - foreign keys enforced (SQLite leaves them off by default)
//   - synchronous NORMAL: safe with WAL, and much faster than FULL
//   - temp_store MEMORY: what SQLite would keep in a temporary file (a
//     statement's journal past 64 KB in a transaction, a large sort or
//     CREATE INDEX, VACUUM's copy) is kept in memory. A production image
//     is an empty one: with no /tmp in it SQLite has nowhere to name the
//     file, and each of those failed there, and only there, with
//     "disk I/O error (6410)".
//
// and on the read pool's, query_only: a write there is an error, not a
// write that happens to work.
var sqlitePragmas = []string{"busy_timeout(5000)", "journal_mode(WAL)", "foreign_keys(1)", "synchronous(NORMAL)", "temp_store(MEMORY)"}

// Each connection's transactions are BEGIN IMMEDIATE: one takes the write
// lock when it starts, not part-way through when it first writes.
//
// Times are written in UTC, as "2006-01-02 15:04:05.999999999+00:00": text
// that sorts in time order, and that SQLite's date functions read. Text
// without a zone (Rails' "2006-01-02 15:04:05.000000") reads as UTC, and
// sorts correctly among these. The same instant in the two formats isn't
// the same text, though: compare such columns by range, or with
// julianday(), not with =.

// sqliteDSN is the translated build's driver's name for the database at
// path, set up as sqlitePragmas says (sqlite_go.go; the C build's parity
// test opens one too).
func sqliteDSN(path string, readOnly bool) string {
	q := url.Values{}
	for _, p := range sqlitePragmas {
		q.Add("_pragma", p)
	}
	if readOnly {
		q.Add("_pragma", "query_only(1)")
	} else {
		q.Set("_txlock", "immediate")
	}
	q.Set("_time_format", "sqlite")
	q.Set("_timezone", "UTC")
	return "file:" + path + "?" + q.Encode()
}

// sqlitePool is a pool of connections to the database at path, each keeping
// its compiled statements (stmtcache.go).
func sqlitePool(path string, readOnly bool) (*sql.DB, error) {
	c, err := sqliteConnector(path, readOnly)
	if err != nil {
		return nil, err
	}
	return sql.OpenDB(cachingConnector{c}), nil
}

func openSQLite(path string) (*DB, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	write, err := sqlitePool(path, false)
	if err != nil {
		return nil, err
	}
	// One writer: SQLite allows one at a time, so more connections would only
	// wait on each other's locks (or fail with SQLITE_BUSY).
	write.SetMaxOpenConns(1)
	write.SetMaxIdleConns(1)
	write.SetConnMaxLifetime(0)
	write.SetConnMaxIdleTime(0)
	read, err := sqlitePool(path, true)
	if err != nil {
		write.Close()
		return nil, err
	}
	n := max(4, runtime.GOMAXPROCS(0))
	read.SetMaxOpenConns(n)
	read.SetMaxIdleConns(n)
	read.SetConnMaxLifetime(0)
	read.SetConnMaxIdleTime(0)
	return &DB{Engine: SQLite, Read: read, Write: write, versioner: &versioner{}}, nil
}
