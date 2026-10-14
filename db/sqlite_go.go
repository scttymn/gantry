//go:build !(cgo && sqlite_fts5 && sqlite_math_functions && sqlite_dbstat && sqlite_stat4)

package db

import (
	"database/sql/driver"
	"errors"

	"modernc.org/sqlite"
)

// SQLite translated from C to Go, for a build without cgo, or without the
// options SQLite compiled from C must have (sqlite.go).

func sqliteConnector(path string, readOnly bool) (driver.Connector, error) {
	return sqlite.NewConnector(sqliteDSN(path, readOnly))
}

// sqliteConn and sqliteRows are what the driver's connections and rows
// are, beyond the least database/sql asks: what a cachingConn passes on.
type sqliteConn interface {
	driver.Conn
	driver.ConnBeginTx
	driver.ConnPrepareContext
	driver.ExecerContext
	driver.QueryerContext
	driver.Pinger
	driver.SessionResetter
	driver.Validator
}

type sqliteRows interface {
	driver.Rows
	driver.RowsColumnTypeDatabaseTypeName
	driver.RowsColumnTypeLength
	driver.RowsColumnTypeNullable
	driver.RowsColumnTypePrecisionScale
	driver.RowsColumnTypeScanType
}

// keepable reports whether a compiled statement can be kept and run again,
// and that it could tell: any can, a script of several too (the driver's
// statement runs them all).
func keepable(driver.Stmt) (keep, ok bool) { return true, true }

// sqliteCode is err's SQLite result code (extended), if it's SQLite's.
func sqliteCode(err error) (int, bool) {
	var e *sqlite.Error
	if !errors.As(err, &e) {
		return 0, false
	}
	return e.Code(), true
}
