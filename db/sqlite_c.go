//go:build cgo && sqlite_fts5 && sqlite_math_functions && sqlite_dbstat && sqlite_stat4

package db

/*
#include <stdlib.h>

// SQLite's C API, the part of it gantry uses (sqlite3.h). Its code is the
// amalgamation github.com/mattn/go-sqlite3 compiles, imported below for
// that alone.
typedef struct sqlite3 sqlite3;
typedef struct sqlite3_stmt sqlite3_stmt;
typedef long long sqlite3_int64;
extern int sqlite3_config(int, ...);
extern int sqlite3_open_v2(const char*, sqlite3**, int, const char*);
extern int sqlite3_close_v2(sqlite3*);
extern int sqlite3_extended_result_codes(sqlite3*, int);
extern int sqlite3_prepare_v2(sqlite3*, const char*, int, sqlite3_stmt**, const char**);
extern int sqlite3_finalize(sqlite3_stmt*);
extern int sqlite3_reset(sqlite3_stmt*);
extern int sqlite3_clear_bindings(sqlite3_stmt*);
extern int sqlite3_step(sqlite3_stmt*);
extern int sqlite3_bind_parameter_count(sqlite3_stmt*);
extern const char *sqlite3_bind_parameter_name(sqlite3_stmt*, int);
extern int sqlite3_bind_null(sqlite3_stmt*, int);
extern int sqlite3_bind_int64(sqlite3_stmt*, int, sqlite3_int64);
extern int sqlite3_bind_double(sqlite3_stmt*, int, double);
extern int sqlite3_bind_text(sqlite3_stmt*, int, const char*, int, void(*)(void*));
extern int sqlite3_bind_blob(sqlite3_stmt*, int, const void*, int, void(*)(void*));
extern int sqlite3_column_count(sqlite3_stmt*);
extern const char *sqlite3_column_name(sqlite3_stmt*, int);
extern const char *sqlite3_column_decltype(sqlite3_stmt*, int);
extern int sqlite3_column_type(sqlite3_stmt*, int);
extern sqlite3_int64 sqlite3_column_int64(sqlite3_stmt*, int);
extern double sqlite3_column_double(sqlite3_stmt*, int);
extern const unsigned char *sqlite3_column_text(sqlite3_stmt*, int);
extern const void *sqlite3_column_blob(sqlite3_stmt*, int);
extern int sqlite3_column_bytes(sqlite3_stmt*, int);
extern sqlite3_int64 sqlite3_changes64(sqlite3*);
extern sqlite3_int64 sqlite3_last_insert_rowid(sqlite3*);
extern const char *sqlite3_errmsg(sqlite3*);
extern const char *sqlite3_errstr(int);
extern void sqlite3_interrupt(sqlite3*);
extern int sqlite3_get_autocommit(sqlite3*);
extern int sqlite3_compileoption_used(const char*);
extern int sqlite3_stmt_status(sqlite3_stmt*, int, int);

#define GANTRY_TRANSIENT ((void(*)(void*))-1)

// SQLite counts every allocation, under a lock all connections share,
// unless told not to before its first.
static int gantry_memstatus_off(void) { return sqlite3_config(9, 0); } // SQLITE_CONFIG_MEMSTATUS

static int gantry_prepare(sqlite3 *db, _GoString_ q, sqlite3_stmt **s, int *tail) {
	const char *p = _GoStringPtr(q), *t = 0;
	int n = (int)_GoStringLen(q);
	if (n == 0) { *s = 0; *tail = 0; return 0; } // no SQL (and no pointer to it)
	int rc = sqlite3_prepare_v2(db, p, n, s, &t);
	*tail = t ? (int)(t - p) : n;
	return rc;
}

// An empty string or blob has no pointer from Go, and SQLite would take a
// null one for NULL.
static int gantry_bind_text(sqlite3_stmt *s, int i, _GoString_ v) {
	size_t n = _GoStringLen(v);
	return sqlite3_bind_text(s, i, n ? _GoStringPtr(v) : "", (int)n, GANTRY_TRANSIENT);
}

static int gantry_bind_blob(sqlite3_stmt *s, int i, const void *p, int n) {
	return sqlite3_bind_blob(s, i, n ? p : "", n, GANTRY_TRANSIENT);
}

// A row's columns, read in one call from Go: each one's type, and its
// integer, its double, or where its text or blob is and how long.
typedef struct { int type; int n; sqlite3_int64 i; double f; const void *p; } gantry_col;

static void gantry_row(sqlite3_stmt *s, int ncol, gantry_col *cols) {
	for (int i = 0; i < ncol; i++) {
		gantry_col *c = &cols[i];
		c->type = sqlite3_column_type(s, i);
		switch (c->type) {
		case 1: c->i = sqlite3_column_int64(s, i); break;
		case 2: c->f = sqlite3_column_double(s, i); break;
		case 3: c->p = sqlite3_column_text(s, i); c->n = sqlite3_column_bytes(s, i); break;
		case 4: c->p = sqlite3_column_blob(s, i); c->n = sqlite3_column_bytes(s, i); break;
		}
	}
}

// A step, and the row it comes to read into cols, as many columns of it as
// cols has room for; ncol is how many it has (the schema may have changed
// since the statement last ran, and SQLite compiled it again).
static int gantry_step(sqlite3_stmt *s, int room, gantry_col *cols, int *ncol) {
	int rc = sqlite3_step(s);
	*ncol = sqlite3_column_count(s);
	if ((rc & 0xff) == 100) gantry_row(s, *ncol < room ? *ncol : room, cols); // SQLITE_ROW
	return rc;
}
*/
import "C"

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unsafe"

	_ "github.com/mattn/go-sqlite3" // SQLite's C, compiled
)

// SQLite compiled from C, for a build with cgo (sqlite.go): gantry's own
// binding, a small one, made to answer as the translated build's driver
// (modernc.org/sqlite) does, so an app sees no difference but the time it
// takes. Its arguments, scripts, times, values and errors are that driver's,
// and the tests that pin them run on both builds.
//
// What it does that the driver it replaces can't: a connection is opened
// without SQLite's own lock (database/sql lets one goroutine at a time use
// a connection), SQLite doesn't count its allocations under a lock all
// connections share, and a row is read in one call from Go to C.

const (
	sqliteOK   = 0
	sqliteBusy = 5
	sqliteRow  = 100
	sqliteDone = 101

	sqliteInteger = 1
	sqliteFloat   = 2
	sqliteText    = 3
	sqliteBlob    = 4

	sqliteOpenReadWrite = 0x2
	sqliteOpenCreate    = 0x4
	sqliteOpenNoMutex   = 0x8000

	sqliteStmtStatusReprepare = 5
)

// sqliteUncounted is whether SQLite was told, before it started, not to
// count its allocations.
var sqliteUncounted = C.gantry_memstatus_off() == sqliteOK

// sqliteCOptions are what SQLite compiled from C must have been compiled
// with, so an app's SQL means what it means in the translated build. The
// build tags that select this build have github.com/mattn/go-sqlite3
// compile in the first four; the rest are CGO_CFLAGS's (sqliteCFlags), as a
// gantry app's Dockerfile sets them. A build without them doesn't start.
var sqliteCOptions = []string{
	"ENABLE_FTS5", "ENABLE_MATH_FUNCTIONS", "ENABLE_DBSTAT_VTAB", "ENABLE_STAT4",
	"SOUNDEX", "LIKE_DOESNT_MATCH_BLOBS", "ENABLE_OFFSET_SQL_FUNC", "ENABLE_DBPAGE_VTAB", "ENABLE_GEOPOLY",
}

// sqliteCFlags is CGO_CFLAGS for the C build: Go's own, and the options
// the build tags don't give.
const sqliteCFlags = "-O2 -g -DSQLITE_SOUNDEX -DSQLITE_LIKE_DOESNT_MATCH_BLOBS -DSQLITE_ENABLE_OFFSET_SQL_FUNC -DSQLITE_ENABLE_DBPAGE_VTAB -DSQLITE_ENABLE_GEOPOLY"

func init() {
	if missing := sqliteMissing(); len(missing) > 0 {
		panic(fmt.Sprintf("gantry/db: SQLite was compiled without %s: build with CGO_CFLAGS=%q, as a gantry app's Dockerfile does, or without cgo", strings.Join(missing, ", "), sqliteCFlags))
	}
}

// sqliteMissing is which of sqliteCOptions SQLite wasn't compiled with.
func sqliteMissing() (missing []string) {
	for _, o := range sqliteCOptions {
		name := C.CString(o)
		if C.sqlite3_compileoption_used(name) == 0 {
			missing = append(missing, o)
		}
		C.free(unsafe.Pointer(name))
	}
	return missing
}

func sqliteConnector(path string, readOnly bool) (driver.Connector, error) {
	pragmas := sqlitePragmas
	begin := "BEGIN IMMEDIATE"
	if readOnly {
		pragmas = append(pragmas[:len(pragmas):len(pragmas)], "query_only(1)")
		begin = "BEGIN"
	}
	return cConnector{path: path, pragmas: pragmas, begin: begin}, nil
}

type cConnector struct {
	path    string
	pragmas []string
	begin   string // what a transaction starts with
}

func (c cConnector) Connect(ctx context.Context) (driver.Conn, error) {
	name := C.CString(c.path)
	defer C.free(unsafe.Pointer(name))
	var db *C.sqlite3
	if rc := C.sqlite3_open_v2(name, &db, sqliteOpenReadWrite|sqliteOpenCreate|sqliteOpenNoMutex, nil); rc != sqliteOK {
		err := sqliteErr(db, rc)
		C.sqlite3_close_v2(db)
		return nil, err
	}
	C.sqlite3_extended_result_codes(db, 1)
	conn := &cConn{db: db, begin: c.begin}
	for _, p := range c.pragmas {
		if _, err := conn.ExecContext(ctx, "PRAGMA "+p, nil); err != nil {
			conn.Close()
			return nil, err
		}
	}
	return conn, nil
}

func (c cConnector) Driver() driver.Driver { return cDriver{} }

// cDriver is what database/sql's DB.Driver says: it opens a database by its
// path, as gantry's writer would.
type cDriver struct{}

func (cDriver) Open(path string) (driver.Conn, error) {
	c, _ := sqliteConnector(path, false)
	return c.Connect(context.Background())
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
// and that it could tell: not a script of several (each run compiles them
// all, as the other build's driver does), nor SQL that's only comments.
// A statement that only wraps one of these says what it wraps (Unwrap).
func keepable(compiled driver.Stmt) (keep, ok bool) {
	for {
		switch s := compiled.(type) {
		case *cStmt:
			return s.s != nil && !s.script, true
		case interface{ Unwrap() driver.Stmt }:
			compiled = s.Unwrap()
		default:
			return false, false
		}
	}
}

// sqliteCode is err's SQLite result code (extended), if it's SQLite's.
func sqliteCode(err error) (int, bool) {
	var e *sqliteError
	if !errors.As(err, &e) {
		return 0, false
	}
	return e.code, true
}

// sqliteError is SQLite's error, said as the other build's driver says it.
type sqliteError struct {
	code int
	msg  string
}

func (e *sqliteError) Error() string { return e.msg }

// Code is SQLite's result code (extended), as the other build's driver's
// error has it: an app's interface{ Code() int } finds it on either.
func (e *sqliteError) Code() int { return e.code }

func sqliteErr(db *C.sqlite3, rc C.int) error {
	str := C.GoString(C.sqlite3_errstr(rc))
	busy := ""
	if rc == sqliteBusy {
		busy = " (SQLITE_BUSY)"
	}
	if db != nil {
		if msg := C.GoString(C.sqlite3_errmsg(db)); msg != str {
			return &sqliteError{int(rc), fmt.Sprintf("%s: %s (%v)%s", str, msg, rc, busy)}
		}
	}
	return &sqliteError{int(rc), fmt.Sprintf("%s (%v)%s", str, rc, busy)}
}

// cConn is a connection. database/sql has one goroutine at a time use it,
// which is what SQLite asks of a connection opened without its lock.
type cConn struct {
	db    *C.sqlite3
	begin string
}

func (c *cConn) err(rc C.int) error { return sqliteErr(c.db, rc) }

// watch interrupts SQLite's work on the connection when ctx is done, from
// now until the function it returns is called. That function says whether
// it did (ctx's error), and doesn't return while an interrupt is being
// made: none reaches the connection's next statement.
func (c *cConn) watch(ctx context.Context) (stop func() error, err error) {
	done := ctx.Done()
	if done == nil {
		return func() error { return nil }, nil
	}
	select {
	case <-done:
		return nil, ctx.Err()
	default:
	}
	fired := make(chan struct{})
	cancel := context.AfterFunc(ctx, func() {
		C.sqlite3_interrupt(c.db)
		close(fired)
	})
	return func() error {
		if cancel() {
			return nil
		}
		<-fired
		return ctx.Err()
	}, nil
}

// compile compiles query's first statement, and is where the rest begins:
// s is nil when that part of the SQL is only comments or space.
func (c *cConn) compile(query string) (s *C.sqlite3_stmt, rest string, err error) {
	if len(query) > math.MaxInt32 {
		return nil, "", fmt.Errorf("sqlite: SQL of %d bytes is past SQLite's limit", len(query))
	}
	var tail C.int
	if rc := C.gantry_prepare(c.db, query, &s, &tail); rc != sqliteOK {
		return nil, "", c.err(rc)
	}
	if s == nil && tail == 0 {
		return nil, "", nil // SQLite reads no further than a NUL
	}
	return s, query[tail:], nil
}

func (c *cConn) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}

func (c *cConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	s, rest, err := c.compile(query)
	if err != nil {
		return nil, err
	}
	st := &cStmt{c: c, sql: query, s: s, script: strings.TrimSpace(rest) != ""}
	if st.script && s != nil {
		// Each run compiles the script's statements as it goes.
		C.sqlite3_finalize(s)
		st.s = nil
	}
	return st, nil
}

func (c *cConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	s, err := c.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	return s.(*cStmt).ExecContext(ctx, args)
}

func (c *cConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	s, err := c.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	st := s.(*cStmt)
	rows, err := st.QueryContext(ctx, args)
	if err != nil {
		st.Close()
		return nil, err
	}
	rows.(*cRows).owns = st // closed with its rows
	return rows, nil
}

func (c *cConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *cConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	begin := c.begin
	if opts.ReadOnly {
		begin = "BEGIN" // a reader needn't take the write lock
	}
	if _, err := c.ExecContext(ctx, begin, nil); err != nil {
		return nil, err
	}
	return cTx{c}, nil
}

func (c *cConn) Ping(context.Context) error {
	if c.db == nil {
		return driver.ErrBadConn
	}
	return nil
}

func (c *cConn) ResetSession(context.Context) error { return nil }

func (c *cConn) IsValid() bool { return c.db != nil }

func (c *cConn) Close() error {
	if c.db == nil {
		return nil
	}
	rc := C.sqlite3_close_v2(c.db)
	c.db = nil
	if rc != sqliteOK {
		return sqliteErr(nil, rc)
	}
	return nil
}

type cTx struct{ c *cConn }

// Commit commits, or, when it can't (a lock it can't have, a deferred
// foreign key), rolls back: database/sql takes a connection back as out of
// its transaction either way.
func (t cTx) Commit() error {
	_, err := t.c.ExecContext(context.Background(), "COMMIT", nil)
	if err != nil && C.sqlite3_get_autocommit(t.c.db) == 0 {
		t.c.ExecContext(context.Background(), "ROLLBACK", nil)
	}
	return err
}

func (t cTx) Rollback() error {
	_, err := t.c.ExecContext(context.Background(), "ROLLBACK", nil)
	return err
}

// cStmt is a statement: one compiled, or a script compiled a statement at
// a time each run.
type cStmt struct {
	c      *cConn
	sql    string
	s      *C.sqlite3_stmt // nil for a script, or SQL that's only comments
	script bool

	cols *C.gantry_col // a row's worth, room for ncol
	ncol int
	// The result's columns, as they were when SQLite last compiled it (it
	// compiles a statement again when the schema changes).
	names, decls []string
	compiled     C.int
	timeFormat   []int8 // per column, the time format that last fitted

	open *cRows // the rows of its last run, while they're open
}

// errRunAgain and errStmtClosed are what rows say when their statement was
// run again, or closed, before they were read to their end: their row was
// SQLite's, and is gone.
var (
	errRunAgain   = errors.New("sqlite: the statement was run again before its rows were read")
	errStmtClosed = errors.New("sqlite: the statement was closed before its rows were read")
)

// cut ends the rows of the statement's last run, before it runs again or is
// closed: why is what they say when read, and what ending the run came to
// (a RETURNING's write, failing at its end) what they say when closed.
func (s *cStmt) cut(why error) {
	if r := s.open; r != nil {
		s.open = nil
		r.cut = why
		r.cutErr = s.reset(s.s)
	}
}

func (s *cStmt) Close() error {
	s.cut(errStmtClosed)
	if s.s != nil {
		C.sqlite3_finalize(s.s)
		s.s = nil
	}
	if s.cols != nil {
		C.free(unsafe.Pointer(s.cols))
		s.cols, s.ncol = nil, 0
	}
	return nil
}

// NumInput is -1: database/sql leaves the arguments to bind, which says
// which one is missing.
func (s *cStmt) NumInput() int { return -1 }

func (s *cStmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.ExecContext(context.Background(), named(args))
}

func (s *cStmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.QueryContext(context.Background(), named(args))
}

func named(args []driver.Value) []driver.NamedValue {
	nv := make([]driver.NamedValue, len(args))
	for i, v := range args {
		nv[i] = driver.NamedValue{Ordinal: i + 1, Value: v}
	}
	return nv
}

// ExecContext runs the statement, or each of the script's, to its end.
func (s *cStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	stop, err := s.c.watch(ctx)
	if err != nil {
		return nil, err
	}
	r, err := s.exec(args)
	if cerr := stop(); cerr != nil {
		return nil, cerr
	}
	return r, err
}

func (s *cStmt) exec(args []driver.NamedValue) (driver.Result, error) {
	if !s.script {
		if s.s == nil {
			return nil, nil
		}
		s.cut(errRunAgain)
		r, err := s.run(s.s, args)
		if rerr := s.reset(s.s); err == nil && rerr != nil {
			return nil, rerr
		}
		return r, err
	}
	var r driver.Result
	for rest := s.sql; rest != ""; {
		st, more, err := s.c.compile(rest)
		if err != nil {
			return nil, err
		}
		rest = more
		if st == nil {
			continue
		}
		r, err = s.run(st, args)
		C.sqlite3_finalize(st)
		if err != nil {
			return nil, err
		}
	}
	return r, nil
}

// run binds args to st and steps it to its end, as sqlite3_exec does: a
// RETURNING's rows, or a SELECT's, are read and dropped.
func (s *cStmt) run(st *C.sqlite3_stmt, args []driver.NamedValue) (driver.Result, error) {
	if err := s.bind(st, args); err != nil {
		return nil, err
	}
	rc := C.sqlite3_step(st)
	for rc == sqliteRow {
		rc = C.sqlite3_step(st)
	}
	if rc != sqliteDone {
		return nil, s.c.err(rc)
	}
	return cResult{int64(C.sqlite3_last_insert_rowid(s.c.db)), int64(C.sqlite3_changes64(s.c.db))}, nil
}

// reset readies st to run again. Its error is the last step's, or what
// ending the statement came to (a RETURNING's write, committed then).
func (s *cStmt) reset(st *C.sqlite3_stmt) error {
	rc := C.sqlite3_reset(st)
	C.sqlite3_clear_bindings(st)
	if rc != sqliteOK {
		return s.c.err(rc)
	}
	return nil
}

type cResult struct{ id, changes int64 }

func (r cResult) LastInsertId() (int64, error) { return r.id, nil }
func (r cResult) RowsAffected() (int64, error) { return r.changes, nil }

// QueryContext runs the statement to its first row, so a statement run as a
// query has happened when it returns, read or not. ctx can interrupt it
// until then; after, it's database/sql that closes the rows when ctx is
// done (as with the other build's driver: an interrupt for rows left open
// would land on whatever the connection ran next). A script runs each of
// its statements, and its rows are the last statement's that had any (the
// last statement's, when none had).
func (s *cStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	stop, err := s.c.watch(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.query(args)
	if cerr := stop(); cerr != nil {
		rows.Close()
		return nil, cerr
	}
	return rows, err
}

func (s *cStmt) query(args []driver.NamedValue) (*cRows, error) {
	if !s.script {
		if s.s == nil {
			return &cRows{done: true}, nil
		}
		return s.first(s.s, args, false)
	}
	// As the other build's driver has it: a statement that comes to a row is
	// the answer, until another does; one that doesn't is, if nothing before
	// it was; and the statement that ends the SQL is, whatever it comes to.
	var rows *cRows
	for rest := s.sql; rest != ""; {
		st, more, err := s.c.compile(rest)
		if err != nil {
			rows.Close()
			return nil, err
		}
		rest = more
		if st == nil {
			continue
		}
		r, err := s.first(st, args, true)
		if err != nil {
			rows.Close()
			return nil, err
		}
		// What isn't the answer is ended now, and a statement that fails
		// as it ends (a RETURNING's write) fails the script. (The other
		// build's driver says nothing of it, and gives the next answer.)
		done := r
		if !r.done || rows == nil || rest == "" {
			done, rows = rows, r
		}
		if err := done.Close(); err != nil {
			rows.Close()
			return nil, err
		}
	}
	if rows == nil {
		return &cRows{done: true}, nil
	}
	return rows, nil
}

// first binds and steps st to its first row; finalize says st is the rows'
// own, to finalize when they're closed, not to reset.
func (s *cStmt) first(st *C.sqlite3_stmt, args []driver.NamedValue, finalize bool) (*cRows, error) {
	if !finalize {
		s.cut(errRunAgain)
	}
	end := func() {
		if finalize {
			C.sqlite3_finalize(st)
		} else {
			s.reset(st)
		}
	}
	if err := s.bind(st, args); err != nil {
		end()
		return nil, err
	}
	r := &cRows{s: s, st: st, finalize: finalize}
	if finalize {
		r.s = &cStmt{c: s.c} // a script's statement has its own columns
	}
	switch rc := r.step(); rc {
	case sqliteRow:
		r.pending = true
	case sqliteDone:
		r.done = true
	default:
		err := s.c.err(rc)
		end()
		if finalize {
			r.s.Close()
		}
		return nil, err
	}
	r.columns()
	if !finalize {
		s.open = r
	}
	return r, nil
}

// bind binds args to st as the other build's driver does: ?NNN and $NNN
// are the argument of that number, other names the argument of that name,
// and a ? the argument in its place.
func (s *cStmt) bind(st *C.sqlite3_stmt, args []driver.NamedValue) error {
	n := int(C.sqlite3_bind_parameter_count(st))
	for i := 1; i <= n; i++ {
		var name string
		if p := C.sqlite3_bind_parameter_name(st, C.int(i)); p != nil {
			name = C.GoString(p)
		}
		var v driver.NamedValue
		found := false
		for _, v = range args {
			if name != "" {
				if (name[0] == '?' || name[0] == '$') && name[1:] == strconv.Itoa(v.Ordinal) || name[1:] == v.Name {
					found = true
					break
				}
			} else if v.Ordinal == i {
				found = true
				break
			}
		}
		if !found {
			if name != "" {
				return fmt.Errorf("missing named argument %q", name[1:])
			}
			return fmt.Errorf("missing argument with index %d", i)
		}
		var rc C.int
		switch x := v.Value.(type) {
		case int64:
			rc = C.sqlite3_bind_int64(st, C.int(i), C.sqlite3_int64(x))
		case float64:
			rc = C.sqlite3_bind_double(st, C.int(i), C.double(x))
		case bool:
			b := 0
			if x {
				b = 1
			}
			rc = C.sqlite3_bind_int64(st, C.int(i), C.sqlite3_int64(b))
		case []byte:
			if x == nil { // NULL, where an empty one is an empty blob
				rc = C.sqlite3_bind_null(st, C.int(i))
				break
			}
			if len(x) > math.MaxInt32 {
				return fmt.Errorf("sqlite: a blob of %d bytes is past SQLite's limit", len(x))
			}
			var p unsafe.Pointer
			if len(x) > 0 {
				p = unsafe.Pointer(&x[0])
			}
			rc = C.gantry_bind_blob(st, C.int(i), p, C.int(len(x)))
		case string:
			if len(x) > math.MaxInt32 {
				return fmt.Errorf("sqlite: text of %d bytes is past SQLite's limit", len(x))
			}
			rc = C.gantry_bind_text(st, C.int(i), x)
		case time.Time:
			rc = C.gantry_bind_text(st, C.int(i), x.UTC().Format(parseTimeFormats[0]))
		case nil:
			rc = C.sqlite3_bind_null(st, C.int(i))
		default:
			return fmt.Errorf("sqlite: invalid driver.Value type %T", x)
		}
		if rc != sqliteOK {
			return s.c.err(rc)
		}
	}
	return nil
}

// cRows are a statement's rows, its first already stepped to.
type cRows struct {
	s        *cStmt
	st       *C.sqlite3_stmt
	finalize bool   // st is the rows' own: finalized, not reset, when they close
	owns     *cStmt // a statement that's closed with them
	pending  bool   // the row stepped to hasn't been read
	done     bool
	cut      error // the statement ran again or closed: st isn't the rows' (cut)
	cutErr   error // what ending their run came to, then
	closed   bool
}

// step steps to the next row and reads it into the statement's columns.
func (r *cRows) step() C.int {
	s := r.s
	var n C.int
	rc := C.gantry_step(r.st, C.int(s.ncol), s.cols, &n)
	if rc == sqliteRow && int(n) > s.ncol {
		C.free(unsafe.Pointer(s.cols))
		s.cols = (*C.gantry_col)(C.malloc(C.size_t(n) * C.size_t(unsafe.Sizeof(C.gantry_col{}))))
		s.ncol = int(n)
		C.gantry_row(r.st, n, s.cols)
	}
	return rc
}

// columns reads the result's columns' names and declared types, if SQLite
// has compiled the statement since they were last read.
func (r *cRows) columns() {
	s := r.s
	compiled := C.sqlite3_stmt_status(r.st, sqliteStmtStatusReprepare, 0)
	n := int(C.sqlite3_column_count(r.st))
	if s.names != nil && compiled == s.compiled && n == len(s.names) {
		return
	}
	s.compiled = compiled
	s.names, s.decls, s.timeFormat = make([]string, n), make([]string, n), make([]int8, n)
	for i := range n {
		s.names[i] = C.GoString(C.sqlite3_column_name(r.st, C.int(i)))
		if p := C.sqlite3_column_decltype(r.st, C.int(i)); p != nil {
			s.decls[i] = strings.ToUpper(C.GoString(p))
		}
		s.timeFormat[i] = -1
	}
}

func (r *cRows) Columns() []string {
	if r.st == nil {
		return []string{}
	}
	return r.s.names
}

func (r *cRows) Next(dest []driver.Value) error {
	if r.cut != nil {
		return r.cut
	}
	if r.done || r.closed {
		return io.EOF
	}
	if !r.pending {
		switch rc := r.step(); rc {
		case sqliteRow:
		case sqliteDone:
			r.done = true
			return io.EOF
		default:
			r.done = true
			return r.s.c.err(rc)
		}
	}
	r.pending = false
	if len(dest) != len(r.s.names) {
		return fmt.Errorf("sqlite: Next: have %v destination values, expected %v", len(dest), len(r.s.names))
	}
	cols := unsafe.Slice(r.s.cols, len(dest))
	for i := range dest {
		c := &cols[i]
		switch c._type {
		case sqliteInteger:
			dest[i] = int64(c.i)
		case sqliteFloat:
			dest[i] = float64(c.f)
		case sqliteText:
			v := C.GoStringN((*C.char)(c.p), c.n)
			switch r.s.decls[i] {
			case "DATE", "DATETIME", "TIMESTAMP":
				t, ok, f := parseSQLiteTime(v, int(r.s.timeFormat[i]))
				if ok && r.s.timeFormat[i] < 0 && f >= 0 {
					r.s.timeFormat[i] = int8(f)
				}
				dest[i] = t
			default:
				dest[i] = v
			}
		case sqliteBlob:
			if c.n == 0 {
				dest[i] = []byte(nil) // as the other build's driver has it
			} else {
				dest[i] = C.GoBytes(c.p, c.n)
			}
		default:
			dest[i] = nil
		}
	}
	return nil
}

// Close ends the rows' statement, and is what that came to: a RETURNING's
// write is committed only now, when its rows weren't read to their end.
func (r *cRows) Close() error {
	if r == nil || r.closed {
		return nil
	}
	r.closed = true
	var err error
	switch {
	case r.cut != nil:
		err = r.cutErr
	case r.st == nil:
	case r.finalize:
		if rc := C.sqlite3_finalize(r.st); rc != sqliteOK {
			err = r.s.c.err(rc)
		}
		r.s.Close()
	default:
		r.s.open = nil
		err = r.s.reset(r.st)
	}
	if r.owns != nil {
		r.owns.Close()
	}
	return err
}

func (r *cRows) ColumnTypeDatabaseTypeName(i int) string { return r.s.decls[i] }

func (r *cRows) ColumnTypeLength(i int) (int64, bool) {
	switch C.sqlite3_column_type(r.st, C.int(i)) {
	case sqliteText, sqliteBlob:
		return math.MaxInt64, true
	}
	return 0, false
}

func (r *cRows) ColumnTypeNullable(int) (nullable, ok bool) { return true, true }

func (r *cRows) ColumnTypePrecisionScale(int) (precision, scale int64, ok bool) { return 0, 0, false }

func (r *cRows) ColumnTypeScanType(i int) reflect.Type {
	switch C.sqlite3_column_type(r.st, C.int(i)) {
	case sqliteInteger:
		switch r.s.decls[i] {
		case "BOOLEAN":
			return reflect.TypeOf(false)
		case "DATE", "DATETIME", "TIME", "TIMESTAMP":
			return reflect.TypeOf(time.Time{})
		}
		return reflect.TypeOf(int64(0))
	case sqliteFloat:
		return reflect.TypeOf(float64(0))
	case sqliteText:
		return reflect.TypeOf("")
	case sqliteBlob:
		return reflect.TypeOf([]byte(nil))
	}
	return reflect.TypeOf(nil)
}

// The formats a time is read in, the first of them the one it's written in.
var parseTimeFormats = []string{
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02T15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04",
	"2006-01-02T15:04",
	"2006-01-02",
}

// parseSQLiteTime reads a column declared as a time as the other build's
// driver does: as time.Time's String wrote it, or in the first of the
// formats that fits (the one that last fitted tried first), as UTC when it
// has no zone; and gives it in UTC. Text that fits none is the text.
func parseSQLiteTime(s string, hint int) (any, bool, int) {
	t := s
	if x := strings.Index(s, "m="); x > 0 {
		t = s[:x]
	}
	if v, err := time.Parse("2006-01-02 15:04:05.999999999 -0700 MST", strings.TrimSpace(t)); err == nil {
		return v.UTC(), true, -1
	}
	ts, z := strings.CutSuffix(s, "Z")
	try := func(f string) (time.Time, error) {
		if !z {
			return time.ParseInLocation(f, ts, time.UTC)
		}
		return time.Parse(f, ts)
	}
	if hint >= 0 && hint < len(parseTimeFormats) {
		if t, err := try(parseTimeFormats[hint]); err == nil {
			return t.UTC(), true, hint
		}
	}
	for i, f := range parseTimeFormats {
		if i == hint {
			continue
		}
		if t, err := try(f); err == nil {
			return t.UTC(), true, i
		}
	}
	return s, false, -1
}
