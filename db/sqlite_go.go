//go:build !(cgo && sqlite_fts5 && sqlite_math_functions && sqlite_dbstat && sqlite_stat4)

package db

import (
	"database/sql/driver"
	"errors"

	"modernc.org/sqlite"
)

func sqliteConnector(path string, readOnly bool) (driver.Connector, error) {
	return sqlite.NewConnector(sqliteDSN(path, readOnly))
}

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

func keepable(driver.Stmt) (keep, ok bool) { return true, true }

func sqliteCode(err error) (int, bool) {
	var e *sqlite.Error
	if !errors.As(err, &e) {
		return 0, false
	}
	return e.Code(), true
}
