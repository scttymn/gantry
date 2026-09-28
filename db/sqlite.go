package db

import (
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"runtime"

	_ "modernc.org/sqlite" // pure Go: the binary stays static
)

// sqlitePragmas are set on every connection:
//   - WAL, so readers never wait for the writer
//   - busy_timeout, so a connection waits for a lock instead of failing
//   - foreign keys enforced (SQLite leaves them off by default)
//   - synchronous NORMAL: safe with WAL, and much faster than FULL
var sqlitePragmas = []string{"busy_timeout(5000)", "journal_mode(WAL)", "foreign_keys(1)", "synchronous(NORMAL)"}

func sqliteDSN(path string, readOnly bool) string {
	q := url.Values{}
	for _, p := range sqlitePragmas {
		q.Add("_pragma", p)
	}
	if readOnly {
		q.Add("_pragma", "query_only(1)")
	} else {
		// BEGIN IMMEDIATE: a transaction takes the write lock when it starts.
		q.Set("_txlock", "immediate")
	}
	// Times are written in UTC, as "2006-01-02 15:04:05.999999999+00:00":
	// text that sorts in time order, and that SQLite's date functions read.
	// Text without a zone (Rails' "2006-01-02 15:04:05.000000") reads as UTC,
	// and sorts correctly among these. The same instant in the two formats
	// isn't the same text, though: compare such columns by range, or with
	// julianday(), not with =.
	q.Set("_time_format", "sqlite")
	q.Set("_timezone", "UTC")
	return "file:" + path + "?" + q.Encode()
}

func openSQLite(path string) (*DB, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	write, err := sql.Open("sqlite", sqliteDSN(path, false))
	if err != nil {
		return nil, err
	}
	// One writer: SQLite allows one at a time, so more connections would only
	// wait on each other's locks (or fail with SQLITE_BUSY).
	write.SetMaxOpenConns(1)
	write.SetMaxIdleConns(1)
	write.SetConnMaxLifetime(0)
	write.SetConnMaxIdleTime(0)
	read, err := sql.Open("sqlite", sqliteDSN(path, true))
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
