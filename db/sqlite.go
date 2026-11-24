package db

import (
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
)

var sqlitePragmas = []string{"busy_timeout(5000)", "journal_mode(WAL)", "foreign_keys(1)", "synchronous(NORMAL)", "temp_store(MEMORY)"}

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
