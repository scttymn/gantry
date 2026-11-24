package db

import (
	"container/list"
	"context"
	"database/sql/driver"
	"fmt"
)

const (
	stmtCacheSize   = 256
	stmtCacheMaxSQL = 16 << 10
)

type sqliteStmt interface {
	driver.Stmt
	driver.StmtExecContext
	driver.StmtQueryContext
}

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
	if err := limitLog(ctx, sc); err != nil {
		conn.Close()
		return nil, err
	}
	return &cachingConn{sqliteConn: sc, stmts: map[string]*list.Element{}, recent: list.New()}, nil
}

func limitLog(ctx context.Context, c sqliteConn) error {
	pageSize, err := pragmaInt(ctx, c, "page_size")
	if err != nil {
		return err
	}
	pages, err := pragmaInt(ctx, c, "wal_autocheckpoint")
	if err != nil {
		return err
	}
	_, err = c.ExecContext(ctx, fmt.Sprintf("PRAGMA journal_size_limit = %d", 32+max(pages, 1)*(pageSize+24)), nil)
	return err
}

func pragmaInt(ctx context.Context, c sqliteConn, name string) (int64, error) {
	rows, err := c.QueryContext(ctx, "PRAGMA "+name, nil)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	v := make([]driver.Value, 1)
	if err := rows.Next(v); err != nil {
		return 0, fmt.Errorf("db: PRAGMA %s: %w", name, err)
	}
	n, ok := v[0].(int64)
	if !ok {
		return 0, fmt.Errorf("db: PRAGMA %s is %T, not an integer", name, v[0])
	}
	return n, nil
}

func unknownDriver(what string, v any) error {
	return fmt.Errorf("db: the SQLite driver's %s (%T) isn't one the statement cache can keep statements on", what, v)
}

type cachingConn struct {
	sqliteConn
	stmts  map[string]*list.Element
	recent *list.List
}

type cachedStmt struct {
	sqliteStmt
	query string
	busy  bool
}

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
	if keep, ok := keepable(compiled); !ok {
		compiled.Close()
		return nil, unknownDriver("statement", compiled)
	} else if !keep {
		compiled.Close()
		return nil, nil
	}
	s := &cachedStmt{sqliteStmt: ss, query: query}
	c.stmts[query] = c.recent.PushFront(s)
	return s, nil
}

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

type cachedRows struct {
	sqliteRows
	stmt *cachedStmt
}

func (r *cachedRows) Close() error {
	r.stmt.busy = false
	return r.sqliteRows.Close()
}
