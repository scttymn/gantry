package db

import (
	"container/list"
	"context"
	"database/sql/driver"
	"fmt"
)

// A SQLite connection keeps the statements it compiles. SQL an app runs
// again (every query, to an app with a handful of them) is compiled the
// first time and run from then on: compiling it on each call, as the driver
// alone does for SQL that wasn't prepared, was a fifth of a request's CPU.
// Nothing asks for it: db.Open's connections do it. Postgres' driver does
// the same.
//
// A connection keeps at most stmtCacheSize statements, closing the ones run
// longest ago, and doesn't keep SQL longer than stmtCacheMaxSQL: an app that
// builds its SQL as it goes, or runs a migration's script, doesn't grow
// without end.
const (
	stmtCacheSize   = 256
	stmtCacheMaxSQL = 16 << 10
)

// sqliteConn, sqliteStmt and sqliteRows are what the SQLite driver's
// connections, statements and rows are, beyond the least database/sql asks:
// what a cachingConn passes on, or runs.
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

type sqliteStmt interface {
	driver.Stmt
	driver.StmtExecContext
	driver.StmtQueryContext
}

type sqliteRows interface {
	driver.Rows
	driver.RowsColumnTypeDatabaseTypeName
	driver.RowsColumnTypeLength
	driver.RowsColumnTypeNullable
	driver.RowsColumnTypePrecisionScale
	driver.RowsColumnTypeScanType
}

// cachingConnector opens the driver's connections, each to keep its
// statements.
type cachingConnector struct{ driver.Connector }

func (c cachingConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	sc, ok := conn.(sqliteConn)
	if !ok {
		conn.Close()
		return nil, unknownDriver("connection", conn)
	}
	return &cachingConn{sqliteConn: sc, stmts: map[string]*list.Element{}, recent: list.New()}, nil
}

// unknownDriver is the error for a driver that isn't the one this was
// written over: said at once, where a test meets it, and not found later
// as an app that got slower.
func unknownDriver(what string, v any) error {
	return fmt.Errorf("db: the SQLite driver's %s (%T) isn't one the statement cache can keep statements on", what, v)
}

// cachingConn is a connection that keeps its statements. It needs no lock:
// database/sql holds the connection's own around every call on it, a
// statement's rows' Next and Close among them.
type cachingConn struct {
	sqliteConn
	stmts  map[string]*list.Element // by their SQL
	recent *list.List               // of *cachedStmt, the last run first
}

type cachedStmt struct {
	sqliteStmt
	query string
	busy  bool // its rows are open: it can't be run again, or closed
}

// stmt is the statement kept for query, compiled if it's new, and nil for a
// run that goes to the driver as it is: SQL too long to keep, SQL whose
// statement is busy (run again while its rows are being read, in one
// transaction), or new SQL when the connection is full of busy ones.
func (c *cachingConn) stmt(ctx context.Context, query string) (*cachedStmt, error) {
	if len(query) > stmtCacheMaxSQL {
		return nil, nil
	}
	if e, ok := c.stmts[query]; ok {
		s := e.Value.(*cachedStmt)
		if s.busy {
			return nil, nil
		}
		c.recent.MoveToFront(e)
		return s, nil
	}
	if len(c.stmts) >= stmtCacheSize && !c.evict() {
		return nil, nil
	}
	compiled, err := c.sqliteConn.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	ss, ok := compiled.(sqliteStmt)
	if !ok {
		compiled.Close()
		return nil, unknownDriver("statement", compiled)
	}
	s := &cachedStmt{sqliteStmt: ss, query: query}
	c.stmts[query] = c.recent.PushFront(s)
	return s, nil
}

// evict closes the statement run longest ago that isn't busy, to make room
// for one more, and reports whether there was one.
func (c *cachingConn) evict() bool {
	for e := c.recent.Back(); e != nil; e = e.Prev() {
		if s := e.Value.(*cachedStmt); !s.busy {
			c.recent.Remove(e)
			delete(c.stmts, s.query)
			s.Close()
			return true
		}
	}
	return false
}

func (c *cachingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	s, err := c.stmt(ctx, query)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return c.sqliteConn.QueryContext(ctx, query, args)
	}
	rows, err := s.QueryContext(ctx, args)
	if err != nil {
		return nil, err
	}
	sr, ok := rows.(sqliteRows)
	if !ok {
		rows.Close()
		return nil, unknownDriver("rows", rows)
	}
	s.busy = true
	return &cachedRows{sqliteRows: sr, stmt: s}, nil
}

func (c *cachingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	s, err := c.stmt(ctx, query)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return c.sqliteConn.ExecContext(ctx, query, args)
	}
	return s.ExecContext(ctx, args)
}

// Close closes the statements the connection kept, then the connection.
func (c *cachingConn) Close() error {
	for e := c.recent.Front(); e != nil; e = e.Next() {
		e.Value.(*cachedStmt).Close()
	}
	clear(c.stmts)
	c.recent.Init()
	return c.sqliteConn.Close()
}

// cachedRows are a kept statement's rows: closing them frees the statement
// for its next run.
type cachedRows struct {
	sqliteRows
	stmt *cachedStmt
}

func (r *cachedRows) Close() error {
	r.stmt.busy = false
	return r.sqliteRows.Close()
}
