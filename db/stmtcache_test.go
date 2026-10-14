package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// counts is what reached the driver: statements compiled and closed through
// the cache, and SQL handed to the driver to run as it would with no cache.
type counts struct{ prepared, closed, direct atomic.Int64 }

func (n *counts) open() int64 { return n.prepared.Load() - n.closed.Load() }

type countingConnector struct {
	driver.Connector
	n *counts
}

func (c countingConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &countingConn{conn.(sqliteConn), c.n}, nil
}

type countingConn struct {
	sqliteConn
	n *counts
}

func (c *countingConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	s, err := c.sqliteConn.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	c.n.prepared.Add(1)
	return &countingStmt{s.(sqliteStmt), c.n}, nil
}

func (c *countingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.n.direct.Add(1)
	return c.sqliteConn.QueryContext(ctx, query, args)
}

func (c *countingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.n.direct.Add(1)
	return c.sqliteConn.ExecContext(ctx, query, args)
}

type countingStmt struct {
	sqliteStmt
	n *counts
}

// Unwrap is the driver's statement it counts for: keepable reads it.
func (s *countingStmt) Unwrap() driver.Stmt { return s.sqliteStmt }

func (s *countingStmt) Close() error {
	s.n.closed.Add(1)
	return s.sqliteStmt.Close()
}

// countedPool is one connection to a new database, through the statement
// cache, over a driver that counts.
func countedPool(t *testing.T) (*sql.DB, *counts) {
	t.Helper()
	return countedPoolAt(t, filepath.Join(t.TempDir(), "test.sqlite3"))
}

func countedPoolAt(t *testing.T, path string) (*sql.DB, *counts) {
	t.Helper()
	base, err := sqliteConnector(path, false)
	if err != nil {
		t.Fatal(err)
	}
	n := &counts{}
	pool := sql.OpenDB(cachingConnector{countingConnector{base, n}})
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { pool.Close() })
	return pool, n
}

// plainPool is one connection through the driver alone: what the cache is
// compared with.
func plainPool(t *testing.T) *sql.DB {
	t.Helper()
	c, err := sqliteConnector(filepath.Join(t.TempDir(), "test.sqlite3"), false)
	if err != nil {
		t.Fatal(err)
	}
	pool := sql.OpenDB(c)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { pool.Close() })
	return pool
}

func mustExec(t *testing.T, q Querier, query string, args ...any) {
	t.Helper()
	if _, err := q.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

const things = `CREATE TABLE things (id integer PRIMARY KEY, name text NOT NULL UNIQUE)`

func ints(t *testing.T, q Querier, query string, args ...any) []int {
	t.Helper()
	rows, err := q.QueryContext(context.Background(), query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []int
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		got = append(got, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return got
}

// SQL an app runs again is compiled once on a connection, not on every call.
func TestStatementCachePreparesOnce(t *testing.T) {
	ctx := context.Background()
	pool, n := countedPool(t)
	mustExec(t, pool, things)
	mustExec(t, pool, `INSERT INTO things (id, name) VALUES (1, 'one')`)
	before := n.prepared.Load()
	for i := range 100 {
		var name string
		if err := pool.QueryRowContext(ctx, `SELECT name FROM things WHERE id = ?`, 1).Scan(&name); err != nil || name != "one" {
			t.Fatalf("run %d: %q, %v", i, name, err)
		}
	}
	if got := n.prepared.Load() - before; got != 1 {
		t.Errorf("100 runs compiled the statement %d times, not once", got)
	}
	if got := n.direct.Load(); got != 0 {
		t.Errorf("%d statements went to the driver uncached", got)
	}
}

// The other door: a transaction's writes, a row read back from an INSERT and
// an Exec, are compiled once however many transactions run them.
func TestStatementCacheInTransactions(t *testing.T) {
	ctx := context.Background()
	pool, n := countedPool(t)
	d := &DB{Engine: SQLite, Read: pool, Write: pool, versioner: &versioner{}}
	mustExec(t, pool, things)
	mustExec(t, pool, `CREATE TABLE tally (n integer NOT NULL)`)
	mustExec(t, pool, `INSERT INTO tally (n) VALUES (0)`)
	before := n.prepared.Load()
	for i := range 50 {
		err := d.Tx(ctx, func(tx *Tx) error {
			var id int
			if err := tx.QueryRowContext(ctx, `INSERT INTO things (name) VALUES (?) RETURNING id`, fmt.Sprint("thing ", i)).Scan(&id); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `UPDATE tally SET n = n + ?`, 1)
			return err
		})
		if err != nil {
			t.Fatalf("transaction %d: %v", i, err)
		}
	}
	if got := n.prepared.Load() - before; got != 2 {
		t.Errorf("50 transactions compiled their two statements %d times, not 2", got)
	}
	var rows, tally int
	if err := pool.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM things), (SELECT n FROM tally)`).Scan(&rows, &tally); err != nil || rows != 50 || tally != 50 {
		t.Errorf("%d rows, tally %d, %v; want 50 and 50", rows, tally, err)
	}
}

// answer is everything a statement's run tells its caller.
type answer struct {
	Columns  []string
	Types    []string
	Rows     [][]any
	Affected int64
	LastID   int64
	Err      string
}

func queryAnswer(q Querier, query string, args ...any) (a answer) {
	rows, err := q.QueryContext(context.Background(), query, args...)
	if err != nil {
		return answer{Err: err.Error()}
	}
	defer rows.Close()
	a.Columns, _ = rows.Columns()
	types, _ := rows.ColumnTypes()
	for _, ct := range types {
		length, hasLength := ct.Length()
		nullable, hasNullable := ct.Nullable()
		a.Types = append(a.Types, fmt.Sprint(ct.DatabaseTypeName(), ct.ScanType(), length, hasLength, nullable, hasNullable))
	}
	for rows.Next() {
		row := make([]any, len(a.Columns))
		dest := make([]any, len(row))
		for i := range row {
			dest[i] = &row[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return answer{Err: err.Error()}
		}
		a.Rows = append(a.Rows, row)
	}
	if err := rows.Err(); err != nil {
		a.Err = err.Error()
	}
	return a
}

func execAnswer(q Querier, query string, args ...any) (a answer) {
	// The driver's result for SQL with no statement in it is nil, and asking
	// it anything panics: that's an answer too, and the cache gives the same.
	defer func() {
		if v := recover(); v != nil {
			a = answer{Err: fmt.Sprint("panic: ", v)}
		}
	}()
	res, err := q.ExecContext(context.Background(), query, args...)
	if err != nil {
		return answer{Err: err.Error()}
	}
	a.Affected, _ = res.RowsAffected()
	a.LastID, _ = res.LastInsertId()
	return a
}

// same runs a statement on the plain driver and through the cache, and
// reports any difference in what they answer.
func same(t *testing.T, plain, cached Querier, run func(Querier, string, ...any) answer, query string, args ...any) answer {
	t.Helper()
	want, got := run(plain, query, args...), run(cached, query, args...)
	if !reflect.DeepEqual(want, got) {
		t.Errorf("%s %v\n the driver: %+v\n the cache:  %+v", query, args, want, got)
	}
	return got
}

// Through the cache a statement answers as the driver alone does, the first
// time (compiled) and again (kept), whatever its arguments are.
func TestStatementCacheMatchesDriver(t *testing.T) {
	plain := plainPool(t)
	cached, _ := countedPool(t)
	const kinds = `CREATE TABLE kinds (id integer PRIMARY KEY, n integer, f real, s text, b blob, ok boolean, at datetime, d decimal(10,2))`
	same(t, plain, cached, execAnswer, kinds)
	at := time.Date(2026, 10, 13, 12, 30, 0, 123456789, time.UTC)
	for _, args := range [][]any{
		{int64(7), 1.5, "text", []byte{0, 1, 2}, true, at, "12.50"},
		{nil, nil, nil, nil, nil, nil, nil},
		{int64(-1), 0.0, "", []byte{}, false, at.Add(time.Hour), "0"},
	} {
		same(t, plain, cached, execAnswer, `INSERT INTO kinds (n, f, s, b, ok, at, d) VALUES (?, ?, ?, ?, ?, ?, ?)`, args...)
	}
	for range 2 {
		same(t, plain, cached, queryAnswer, `SELECT * FROM kinds ORDER BY id`)
		same(t, plain, cached, queryAnswer, `SELECT id, s FROM kinds WHERE n = ? OR s = ?`, int64(7), "")
		same(t, plain, cached, queryAnswer, `SELECT count(*) AS total, max(at) FROM kinds WHERE id > ?`, 0)
		same(t, plain, cached, queryAnswer, `INSERT INTO kinds (n) VALUES (?) RETURNING id, n`, int64(3))
		same(t, plain, cached, execAnswer, `UPDATE kinds SET n = n + 1 WHERE n IS NOT NULL`)
		same(t, plain, cached, execAnswer, `DELETE FROM kinds WHERE id = ?`, 99)
		// Too few arguments, and too many.
		same(t, plain, cached, queryAnswer, `SELECT id FROM kinds WHERE n = ? AND s = ?`, int64(7))
		same(t, plain, cached, queryAnswer, `SELECT id FROM kinds WHERE n = ?`, int64(7), "extra")
		same(t, plain, cached, execAnswer, `DELETE FROM kinds WHERE id = ? AND n = ?`, 99)
	}
}

// database/sql asks a connection and its rows what more they can do, and
// through the cache they can do exactly what the driver's can: a driver
// that gains one shows here.
func TestStatementCacheKeepsDriverInterfaces(t *testing.T) {
	ctx := context.Background()
	base, err := sqliteConnector(filepath.Join(t.TempDir(), "test.sqlite3"), false)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	wrapped, err := cachingConnector{base}.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer wrapped.Close()
	if reflect.TypeOf(raw) == reflect.TypeOf(wrapped) {
		t.Fatal("the cache's connection is the driver's own: nothing is cached")
	}
	compare := func(what string, raw, wrapped any, ifaces ...any) {
		for _, iface := range ifaces {
			it := reflect.TypeOf(iface).Elem()
			if r, w := reflect.TypeOf(raw).Implements(it), reflect.TypeOf(wrapped).Implements(it); r != w {
				t.Errorf("%s: driver.%s: the driver's %v, the cache's %v", what, it.Name(), r, w)
			}
		}
	}
	compare("a connection", raw, wrapped,
		(*driver.ConnBeginTx)(nil), (*driver.ConnPrepareContext)(nil), (*driver.ExecerContext)(nil),
		(*driver.QueryerContext)(nil), (*driver.Pinger)(nil), (*driver.SessionResetter)(nil),
		(*driver.Validator)(nil), (*driver.NamedValueChecker)(nil))
	// What the cache runs a kept statement through.
	stmt, err := raw.(driver.ConnPrepareContext).PrepareContext(ctx, `SELECT 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	if _, ok := stmt.(sqliteStmt); !ok {
		t.Errorf("the driver's statement (%T) has no ExecContext or QueryContext", stmt)
	}
	rawRows, err := raw.(driver.QueryerContext).QueryContext(ctx, `SELECT 1`, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rawRows.Close()
	wrappedRows, err := wrapped.(driver.QueryerContext).QueryContext(ctx, `SELECT 1`, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer wrappedRows.Close()
	compare("rows", rawRows, wrappedRows,
		(*driver.RowsColumnTypeDatabaseTypeName)(nil), (*driver.RowsColumnTypeLength)(nil),
		(*driver.RowsColumnTypeNullable)(nil), (*driver.RowsColumnTypePrecisionScale)(nil),
		(*driver.RowsColumnTypeScanType)(nil), (*driver.RowsNextResultSet)(nil))
}

// The same SQL run again while its rows are still being read, in one
// transaction: the kept statement is in use, so the second run has one of
// its own, and neither loses a row.
func TestStatementCacheSameQueryNested(t *testing.T) {
	ctx := context.Background()
	pool, n := countedPool(t)
	mustExec(t, pool, things)
	mustExec(t, pool, `INSERT INTO things (id, name) VALUES (1, 'one'), (2, 'two'), (3, 'three')`)
	const query = `SELECT id FROM things WHERE id > ? ORDER BY id`
	// Kept, by a first run.
	if got := ints(t, pool, query, 0); !reflect.DeepEqual(got, []int{1, 2, 3}) {
		t.Fatalf("got %v", got)
	}
	before := n.prepared.Load()
	tx, err := pool.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	outer, err := tx.QueryContext(ctx, query, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer outer.Close()
	var got []int
	for outer.Next() {
		var id int
		if err := outer.Scan(&id); err != nil {
			t.Fatal(err)
		}
		got = append(got, id)
		if inner := ints(t, tx, query, id); len(inner) != 3-id {
			t.Errorf("inside row %d: got %v, want the %d rows after it", id, inner, 3-id)
		}
	}
	if err := outer.Err(); err != nil || !reflect.DeepEqual(got, []int{1, 2, 3}) {
		t.Errorf("the outer rows: %v, %v; want 1 2 3", got, err)
	}
	outer.Close()
	// Free again, and still the kept one.
	if got := ints(t, tx, query, 1); !reflect.DeepEqual(got, []int{2, 3}) {
		t.Errorf("afterwards: %v", got)
	}
	if got := n.prepared.Load() - before; got != 0 {
		t.Errorf("the kept statement was compiled %d more times", got)
	}
	if n.open() < 1 {
		t.Error("the kept statement was closed")
	}
}

// What isn't one good statement behaves as the driver alone has it: a
// script runs whole, nothing does nothing, and SQL that doesn't compile is
// the driver's error each time, with nothing kept.
func TestStatementCacheScriptsAndErrors(t *testing.T) {
	plain := plainPool(t)
	cached, n := countedPool(t)
	same(t, plain, cached, execAnswer, things)
	for i := range 2 {
		same(t, plain, cached, execAnswer, `INSERT INTO things (name) VALUES (?1 || 'a'); INSERT INTO things (name) VALUES (?1 || 'b')`, fmt.Sprint(i))
		same(t, plain, cached, queryAnswer, `INSERT INTO things (name) VALUES (?1 || 'c'); SELECT count(*) FROM things`, fmt.Sprint(i))
		same(t, plain, cached, execAnswer, ``)
		same(t, plain, cached, queryAnswer, ``)
		same(t, plain, cached, execAnswer, `-- nothing`)
		same(t, plain, cached, queryAnswer, `-- nothing`)
	}
	if got := same(t, plain, cached, queryAnswer, `SELECT count(*) FROM things`); len(got.Rows) != 1 || got.Rows[0][0] != int64(6) {
		t.Errorf("after two runs of each script: %+v; want 6 things", got)
	}
	open := n.open()
	for range 3 {
		if got := same(t, plain, cached, queryAnswer, `SELEC 1`); got.Err == "" {
			t.Error("SELEC 1 ran")
		}
		if got := same(t, plain, cached, execAnswer, `INSERT INTO nowhere (n) VALUES (1)`); got.Err == "" {
			t.Error("an INSERT into no table ran")
		}
	}
	if got := n.open(); got != open {
		t.Errorf("SQL that doesn't compile left %d statements open", got-open)
	}
	same(t, plain, cached, queryAnswer, `SELECT 1`)
}

// A PRAGMA does its work, or reads its value, as it's compiled, not as it's
// run: kept and run again it would do nothing, or answer with what was true
// the first time. SQLite sees to it (it compiles a PRAGMA again each time
// one is run), so set, read and set again answer as the driver alone has
// them, as do the other statements that aren't a read or a write of rows.
func TestStatementCachePragmas(t *testing.T) {
	plain := plainPool(t)
	cached, _ := countedPool(t)
	same(t, plain, cached, execAnswer, things)
	for _, size := range []int{100, 200, 100} {
		same(t, plain, cached, execAnswer, fmt.Sprint(`PRAGMA cache_size = `, size))
		if got := same(t, plain, cached, queryAnswer, `PRAGMA cache_size`); len(got.Rows) != 1 || got.Rows[0][0] != int64(size) {
			t.Errorf("cache_size set to %d reads %+v", size, got)
		}
	}
	for _, on := range []int{0, 1, 0} {
		same(t, plain, cached, execAnswer, fmt.Sprint(`pragma foreign_keys = `, on))
		if got := same(t, plain, cached, queryAnswer, "  /* which */ PRAGMA foreign_keys"); len(got.Rows) != 1 || got.Rows[0][0] != int64(on) {
			t.Errorf("foreign_keys set to %d reads %+v", on, got)
		}
	}
	for _, v := range []int{7, 8, 7} {
		same(t, plain, cached, execAnswer, fmt.Sprint(`PRAGMA user_version = `, v))
		same(t, plain, cached, queryAnswer, "-- the app's own number\nPRAGMA user_version")
		same(t, plain, cached, execAnswer, fmt.Sprint(`PRAGMA busy_timeout = `, 1000*v))
		same(t, plain, cached, queryAnswer, `PRAGMA busy_timeout`)
	}
	for range 2 {
		same(t, plain, cached, execAnswer, `CREATE TABLE IF NOT EXISTS extra (n integer)`)
		same(t, plain, cached, execAnswer, `SAVEPOINT here`)
		same(t, plain, cached, execAnswer, `RELEASE here`)
		same(t, plain, cached, queryAnswer, `EXPLAIN SELECT 1`)
		same(t, plain, cached, queryAnswer, `PRAGMA table_info(things)`)
	}
}

// SQL is kept however it's written: sqlc's queries start with a comment
// naming them.
func TestStatementCacheCommentedSQL(t *testing.T) {
	plain := plainPool(t)
	cached, n := countedPool(t)
	same(t, plain, cached, execAnswer, things)
	for _, query := range []string{
		"-- name: ListThings :many\nSELECT id FROM things",
		"  \n\t/* all */ -- of them\n select id from things",
		"-- name: AddThing :one\nINSERT INTO things (name) VALUES ('a') ON CONFLICT DO NOTHING RETURNING id;",
	} {
		before := n.prepared.Load()
		for range 3 {
			same(t, plain, cached, queryAnswer, query)
		}
		if got := n.prepared.Load() - before; got != 1 {
			t.Errorf("%q was compiled %d times in 3 runs, not once", query, got)
		}
	}
}

// A statement refused by a constraint is still good: the error is the
// driver's own, and the same SQL with other values goes in.
func TestStatementCacheAfterConstraintError(t *testing.T) {
	ctx := context.Background()
	pool, n := countedPool(t)
	mustExec(t, pool, things)
	mustExec(t, pool, `CREATE TABLE parts (id integer PRIMARY KEY, thing_id integer NOT NULL REFERENCES things(id))`)
	const thing, part = `INSERT INTO things (name) VALUES (?)`, `INSERT INTO parts (thing_id) VALUES (?) RETURNING id`
	before := n.prepared.Load()
	mustExec(t, pool, thing, "one")
	for range 3 {
		if _, err := pool.ExecContext(ctx, thing, "one"); !IsUnique(err) {
			t.Fatalf("a second %q: %v; want a unique violation", "one", err)
		}
		var id int
		if err := pool.QueryRowContext(ctx, part, 404).Scan(&id); !IsForeignKey(err) {
			t.Fatalf("a part of no thing: %v; want a foreign key violation", err)
		}
	}
	mustExec(t, pool, thing, "two")
	var id int
	if err := pool.QueryRowContext(ctx, part, 2).Scan(&id); err != nil || id != 1 {
		t.Errorf("a part of thing 2: id %d, %v", id, err)
	}
	if got := n.prepared.Load() - before; got != 2 {
		t.Errorf("the two statements were compiled %d times, not 2", got)
	}
}

// A query cancelled part-way, in its first step or between rows, and the
// same SQL runs right afterwards. The driver may have database/sql drop the
// connection it interrupted, and a new one compiles its own: what was kept
// on the dropped one is closed with it.
func TestStatementCacheAfterCancel(t *testing.T) {
	pool, n := countedPool(t)
	const count = `WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c WHERE x < ?) SELECT count(*) FROM c`
	const each = `WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c WHERE x < ?) SELECT x FROM c`

	// Kept, then cancelled in the one long step that counts.
	var total int
	if err := pool.QueryRowContext(context.Background(), count, 10).Scan(&total); err != nil || total != 10 {
		t.Fatalf("got %d, %v", total, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := pool.QueryRowContext(ctx, count, int64(1)<<40).Scan(&total); err == nil {
		t.Fatal("counting to 2^40 finished")
	}
	if err := pool.QueryRowContext(context.Background(), count, 25).Scan(&total); err != nil || total != 25 {
		t.Errorf("after the cancelled count: %d, %v", total, err)
	}

	// Cancelled between rows, its rows left open.
	ctx, cancel = context.WithCancel(context.Background())
	rows, err := pool.QueryContext(ctx, each, int64(1)<<40)
	if err != nil {
		t.Fatal(err)
	}
	if !rows.Next() {
		t.Fatalf("no first row: %v", rows.Err())
	}
	cancel()
	for rows.Next() {
	}
	if rows.Err() == nil {
		t.Error("reading to 2^40 finished")
	}
	rows.Close()
	if got := ints(t, pool, each, 3); !reflect.DeepEqual(got, []int{1, 2, 3}) {
		t.Errorf("after the cancelled read: %v", got)
	}
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	if p, c := n.prepared.Load(), n.closed.Load(); p < 2 || p != c {
		t.Errorf("%d statements compiled, %d closed; want the two, on each connection there was, and all of them closed", p, c)
	}
}

// A kept statement follows the schema, as a migration on another connection
// changes it: a new column is in SELECT *, a dropped table is an error, and
// the table made again is read again.
func TestStatementCacheAfterSchemaChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.sqlite3")
	reader, n := countedPoolAt(t, path)
	writer, _ := countedPoolAt(t, path)
	mustExec(t, writer, things)
	mustExec(t, writer, `INSERT INTO things (id, name) VALUES (1, 'one')`)
	const all = `SELECT * FROM things`
	if got := queryAnswer(reader, all); !reflect.DeepEqual(got.Columns, []string{"id", "name"}) || len(got.Rows) != 1 {
		t.Fatalf("got %+v", got)
	}
	mustExec(t, writer, `ALTER TABLE things ADD COLUMN note text NOT NULL DEFAULT 'new'`)
	if got := queryAnswer(reader, all); !reflect.DeepEqual(got.Columns, []string{"id", "name", "note"}) || len(got.Rows) != 1 || got.Rows[0][2] != "new" {
		t.Errorf("after the column was added: %+v", got)
	}
	mustExec(t, writer, `DROP TABLE things`)
	if got := queryAnswer(reader, all); !strings.Contains(got.Err, "no such table") {
		t.Errorf("after the table was dropped: %+v", got)
	}
	mustExec(t, writer, `CREATE TABLE things (name text NOT NULL)`)
	mustExec(t, writer, `INSERT INTO things (name) VALUES ('again')`)
	if got := queryAnswer(reader, all); !reflect.DeepEqual(got.Columns, []string{"name"}) || len(got.Rows) != 1 || got.Rows[0][0] != "again" {
		t.Errorf("after the table was made again: %+v", got)
	}
	if got := n.prepared.Load(); got != 1 {
		t.Errorf("the statement was compiled through the cache %d times, not once", got)
	}
}

// A connection keeps no more than stmtCacheSize statements, the ones run
// longest ago closed first and one whose rows are open never; SQL past
// stmtCacheMaxSQL isn't kept at all.
func TestStatementCacheBounded(t *testing.T) {
	ctx := context.Background()
	pool, n := countedPool(t)
	conn, err := pool.Conn(ctx) // one connection throughout, held across the open rows
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	one := func(query string) {
		t.Helper()
		var v int
		if err := conn.QueryRowContext(ctx, query).Scan(&v); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	held, err := conn.QueryContext(ctx, `SELECT 1 UNION ALL SELECT 2`)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if !held.Next() {
		t.Fatal(held.Err())
	}
	for i := range 1000 {
		one(fmt.Sprint(`SELECT `, i))
	}
	if got := n.open(); got != stmtCacheSize {
		t.Errorf("after 1,000 statements %d are open; want %d", got, stmtCacheSize)
	}
	before := n.prepared.Load()
	one(`SELECT 999`)
	if got := n.prepared.Load() - before; got != 0 {
		t.Errorf("the newest statement was compiled again (%d)", got)
	}
	one(`SELECT 0`)
	if got := n.prepared.Load() - before; got != 1 {
		t.Errorf("the oldest statement was still kept (compiled %d times)", got)
	}
	// The one whose rows were open through all of it is still the kept one.
	var v int
	if err := held.Scan(&v); err != nil || v != 1 || !held.Next() {
		t.Errorf("the held rows: %d, %v, %v", v, err, held.Err())
	}
	held.Close()
	before = n.prepared.Load()
	if got := ints(t, conn, `SELECT 1 UNION ALL SELECT 2`); !reflect.DeepEqual(got, []int{1, 2}) || n.prepared.Load() != before {
		t.Errorf("the statement held open was closed under it: %v, compiled %d more times", got, n.prepared.Load()-before)
	}

	big := `SELECT 1 /* ` + strings.Repeat("x", stmtCacheMaxSQL) + ` */`
	before, direct := n.prepared.Load(), n.direct.Load()
	for range 3 {
		one(big)
	}
	if p, d := n.prepared.Load()-before, n.direct.Load()-direct; p != 0 || d != 3 {
		t.Errorf("SQL of %d bytes: kept %d times, run uncached %d times; want 0 and 3", len(big), p, d)
	}
}

// Every kept statement busy at once (the rows of each still open, on one
// connection), and one more statement still runs and is right: there's
// nothing to close to make room for it, so it goes to the driver as it is,
// and nothing that's kept is closed under its rows. Once rows close, new
// SQL is kept again.
func TestStatementCacheEveryStatementBusy(t *testing.T) {
	ctx := context.Background()
	pool, n := countedPool(t)
	conn, err := pool.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var held []*sql.Rows
	for i := range stmtCacheSize {
		rows, err := conn.QueryContext(ctx, fmt.Sprint(`SELECT `, i, ` UNION ALL SELECT -1`))
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		if !rows.Next() {
			t.Fatal(rows.Err())
		}
		held = append(held, rows)
	}
	const more = `SELECT 7 UNION ALL SELECT 8`
	prepared, direct := n.prepared.Load(), n.direct.Load()
	for range 2 {
		if got := ints(t, conn, more); !reflect.DeepEqual(got, []int{7, 8}) {
			t.Fatalf("with every kept statement busy: %v", got)
		}
		if _, err := conn.ExecContext(ctx, `SELECT 9`); err != nil {
			t.Fatalf("with every kept statement busy, an Exec: %v", err)
		}
	}
	if p, d, open := n.prepared.Load()-prepared, n.direct.Load()-direct, n.open(); p != 0 || d != 4 || open != stmtCacheSize {
		t.Errorf("with every kept statement busy: %d more kept, %d run uncached, %d open; want 0, 4 and %d", p, d, open, stmtCacheSize)
	}
	for i, rows := range held {
		var v int
		if err := rows.Scan(&v); err != nil || v != i {
			t.Fatalf("the held rows of statement %d: %d, %v", i, v, err)
		}
		rows.Close()
	}
	prepared = n.prepared.Load()
	for range 2 {
		if got := ints(t, conn, more); !reflect.DeepEqual(got, []int{7, 8}) {
			t.Fatalf("with the rows closed: %v", got)
		}
	}
	if p, open := n.prepared.Load()-prepared, n.open(); p != 1 || open != stmtCacheSize {
		t.Errorf("with the rows closed: compiled %d times, %d open; want once and %d", p, open, stmtCacheSize)
	}
}

// bareConnector's connections are the driver's with nothing but the least
// database/sql asks; bareStmtConnector's are whole, and their statements
// are that bare.
type bareConnector struct{ driver.Connector }

func (c bareConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	return struct{ driver.Conn }{conn}, err
}

type bareStmtConnector struct{ driver.Connector }

func (c bareStmtConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return bareStmtConn{conn.(sqliteConn)}, nil
}

type bareStmtConn struct{ sqliteConn }

func (c bareStmtConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	s, err := c.sqliteConn.PrepareContext(ctx, query)
	return struct{ driver.Stmt }{s}, err
}

// Over a driver that isn't the one the cache was written for, it says so
// at the first use, and doesn't quietly run with nothing kept.
func TestStatementCacheUnknownDriver(t *testing.T) {
	ctx := context.Background()
	base, err := sqliteConnector(filepath.Join(t.TempDir(), "test.sqlite3"), false)
	if err != nil {
		t.Fatal(err)
	}
	pool := sql.OpenDB(cachingConnector{bareConnector{base}})
	defer pool.Close()
	if err := pool.PingContext(ctx); err == nil || !strings.Contains(err.Error(), "the SQLite driver's connection") {
		t.Errorf("a connection that's only a driver.Conn: %v", err)
	}
	pool = sql.OpenDB(cachingConnector{bareStmtConnector{base}})
	defer pool.Close()
	var one int
	if err := pool.QueryRowContext(ctx, `SELECT 1`).Scan(&one); err == nil || !strings.Contains(err.Error(), "the SQLite driver's statement") {
		t.Errorf("a statement that's only a driver.Stmt: %d, %v", one, err)
	}
	if _, err := pool.ExecContext(ctx, `SELECT 1`); err == nil || !strings.Contains(err.Error(), "the SQLite driver's statement") {
		t.Errorf("a statement that's only a driver.Stmt, run with Exec: %v", err)
	}
}

// Closing the database closes every statement its connections compiled.
func TestStatementCacheClosesStatements(t *testing.T) {
	pool, n := countedPool(t)
	mustExec(t, pool, things)
	for i := range 10 {
		mustExec(t, pool, `INSERT INTO things (name) VALUES (?)`, fmt.Sprint(i))
		if got := ints(t, pool, fmt.Sprint(`SELECT `, i)); len(got) != 1 {
			t.Fatalf("got %v", got)
		}
	}
	if n.prepared.Load() == 0 {
		t.Fatal("nothing was compiled through the cache")
	}
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	if p, c := n.prepared.Load(), n.closed.Load(); p != c {
		t.Errorf("%d statements compiled, %d closed", p, c)
	}
}

// What the cache saves on a query an app runs again and again.
func BenchmarkRepeatedQuery(b *testing.B) {
	const query = `SELECT t.id, t.name, (SELECT count(*) FROM things u WHERE u.id <= t.id) FROM things t WHERE t.id = ?`
	run := func(b *testing.B, pool *sql.DB) {
		pool.SetMaxOpenConns(1)
		defer pool.Close()
		ctx := context.Background()
		if _, err := pool.ExecContext(ctx, things+`; INSERT INTO things (name) VALUES ('a'), ('b'), ('c')`); err != nil {
			b.Fatal(err)
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			var id, n int
			var name string
			if err := pool.QueryRowContext(ctx, query, 2).Scan(&id, &name, &n); err != nil {
				b.Fatal(err)
			}
		}
	}
	b.Run("driver", func(b *testing.B) {
		c, err := sqliteConnector(filepath.Join(b.TempDir(), "test.sqlite3"), false)
		if err != nil {
			b.Fatal(err)
		}
		pool := sql.OpenDB(c)
		run(b, pool)
	})
	b.Run("cached", func(b *testing.B) {
		pool, err := sqlitePool(filepath.Join(b.TempDir(), "test.sqlite3"), false)
		if err != nil {
			b.Fatal(err)
		}
		run(b, pool)
	})
}
