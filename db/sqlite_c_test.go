//go:build cgo && sqlite_fts5 && sqlite_math_functions && sqlite_dbstat && sqlite_stat4

package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"modernc.org/sqlite"
)

// The C build answers as the translated build's driver does: two databases
// made the same, one opened by each, asked the same things, give the same
// columns, types, values, results and errors. Each is asked through database/sql, on
// one connection, with no statement kept.
func TestSQLiteCMatchesTranslated(t *testing.T) {
	tc, err := sqlite.NewConnector(sqliteDSN(filepath.Join(t.TempDir(), "translated.sqlite3"), false))
	if err != nil {
		t.Fatal(err)
	}
	translated := sql.OpenDB(tc)
	cc, _ := sqliteConnector(filepath.Join(t.TempDir(), "compiled.sqlite3"), false)
	compiled := sql.OpenDB(cc)
	for _, p := range []*sql.DB{translated, compiled} {
		p.SetMaxOpenConns(1)
		t.Cleanup(func() { p.Close() })
	}
	// Two databases, the same: each statement is run once on each.
	both := func(run func(Querier, string, ...any) answer, query string, args ...any) {
		t.Helper()
		same(t, translated, compiled, run, query, args...)
	}
	setup := func(query string) {
		t.Helper()
		mustExec(t, translated, query)
		mustExec(t, compiled, query)
	}

	setup(`CREATE TABLE kinds (id integer PRIMARY KEY, n integer, f real, s text, b blob, ok boolean,
		at datetime, d date, ts timestamp, dec decimal(10,2), any)`)
	paris := time.FixedZone("Paris", 2*60*60)
	at := time.Date(2026, 10, 13, 12, 30, 0, 123456789, paris)
	for _, row := range [][]any{
		{1, int64(7), 1.5, "text", []byte{0, 1, 2}, true, at, at, at, "12.50", "x"},
		{2, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil},
		{3, int64(-1), 0.0, "", []byte{}, false, at.UTC(), "2026-10-13", "2026-10-13T12:30:00Z", 0, []byte(nil)},
		{4, int64(1 << 62), 1e300, "ünïcødé", []byte("blob"), int64(2), "2026-10-13 12:30", "2026-10-13T12:30", at.String(), 1.25, 3},
		{5, int64(0), -0.5, "not a time", nil, int64(0), "not a time", "", "2026-10-13 12:30:00.5", "x", nil},
	} {
		both(execAnswer, `INSERT OR REPLACE INTO kinds VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, row...)
	}
	both(queryAnswer, `SELECT * FROM kinds ORDER BY id`)
	both(queryAnswer, `SELECT id, at, typeof(at), d, ts, max(at) AS latest FROM kinds GROUP BY id ORDER BY id`)
	both(queryAnswer, `SELECT count(*), sum(f), group_concat(s) FROM kinds`)

	// Arguments: in their place, by number ($ and ?, in any order), by name,
	// one missing, one too many.
	both(queryAnswer, `SELECT ?, ?`, 1, "two")
	both(queryAnswer, `SELECT $2 || $1 || $2`, "b", "a")
	both(queryAnswer, `SELECT ?2, ?1`, "b", "a")
	both(queryAnswer, `SELECT :x, @y, $z`, sql.Named("x", 1), sql.Named("y", 2), sql.Named("z", 3))
	both(queryAnswer, `SELECT ?, ?`, 1)
	both(queryAnswer, `SELECT ?`, 1, 2)
	both(queryAnswer, `SELECT $1`, sql.Named("x", 1))

	// Scripts: each statement runs, with the arguments it numbers; a query's
	// rows are its last statement's that had any.
	setup(`CREATE TABLE things (id integer PRIMARY KEY, name text NOT NULL UNIQUE)`)
	both(execAnswer, `INSERT INTO things (name) VALUES (?1 || 'a'); INSERT INTO things (name) VALUES (?1 || 'b')`, "t")
	both(queryAnswer, `INSERT INTO things (name) VALUES (?1 || 'c'); SELECT count(*) FROM things`, "t")
	both(queryAnswer, `SELECT 1; SELECT 2`)
	both(queryAnswer, `SELECT 1 AS one; DELETE FROM things WHERE name = 'nobody'`)
	both(queryAnswer, `SELECT 1 AS one; DELETE FROM things WHERE name = 'nobody'; `)
	both(queryAnswer, `DELETE FROM things WHERE name = 'nobody'; SELECT 2 AS two; -- the end`)
	both(queryAnswer, `DELETE FROM things WHERE name = 'nobody'; DELETE FROM things WHERE name = 'no one'`)
	both(execAnswer, `UPDATE things SET name = name || '!' WHERE name LIKE ?; -- the end`, "t%")
	both(queryAnswer, `SELECT name FROM things ORDER BY id`)
	both(execAnswer, ``)
	both(queryAnswer, `-- only a comment`)
	both(queryAnswer, `   `)

	// A statement asked as a query happens, read or not.
	both(func(q Querier, query string, args ...any) answer {
		rows, err := q.QueryContext(t.Context(), query, args...)
		if err != nil {
			return answer{Err: err.Error()}
		}
		rows.Close()
		return queryAnswer(q, `SELECT count(*) FROM things WHERE name = 'asked'`)
	}, `INSERT INTO things (name) VALUES ('asked') ON CONFLICT DO NOTHING`)

	// Results, and errors.
	both(execAnswer, `INSERT INTO things (name) VALUES (?) ON CONFLICT DO NOTHING`, "z")
	both(execAnswer, `UPDATE things SET name = name || '.'`)
	both(execAnswer, `INSERT INTO things (name) VALUES ('ta!')`)
	both(execAnswer, `INSERT INTO things (id, name) VALUES ('one', 'x')`)
	both(queryAnswer, `SELECT nope FROM things`)
	both(queryAnswer, `SELECT * FROM nowhere`)
	both(queryAnswer, `SELEC 1`)
	both(execAnswer, `INSERT INTO things (name) VALUES (?)`, struct{}{})
}

// SQLite was told not to count its allocations before its first: counting
// takes a lock every connection shares, on every one.
func TestSQLiteCUncounted(t *testing.T) {
	if !sqliteUncounted {
		t.Fatal("SQLite was started before gantry's db could tell it not to count its allocations")
	}
}

// A prepared statement run again while the rows of its last run are open
// ends those rows, which say so: they never read a row that's gone.
func TestSQLiteCRunAgain(t *testing.T) {
	d := sqliteOne(t)
	ctx := context.Background()
	conn, err := d.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	st, err := conn.PrepareContext(ctx, `SELECT ? FROM (SELECT 1 UNION ALL SELECT 2)`)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	first, err := st.QueryContext(ctx, strings.Repeat("x", 1<<16))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	var v string
	if !first.Next() || first.Scan(&v) != nil || len(v) != 1<<16 {
		t.Fatalf("the first run's first row: %d bytes, %v", len(v), first.Err())
	}
	if err := st.QueryRowContext(ctx, "again").Scan(&v); err != nil || v != "again" {
		t.Fatalf("run again: %q, %v", v, err)
	}
	if first.Next() || !errors.Is(first.Err(), errRunAgain) {
		t.Errorf("the first run's rows, after a second run: %v; want %v", first.Err(), errRunAgain)
	}
}

// The C build has what the translated one has, but for the options listed
// here, none of which changes what SQL means: a new difference fails until
// it's added, with why. (A build that started has sqliteCOptions: init
// checks them.)
func TestSQLiteCCompileOptions(t *testing.T) {
	// None of these changes what SQL means: each is a C API gantry doesn't
	// call, how the C was compiled, or a default gantry sets itself.
	allowed := map[string]string{
		// translated only
		"DEFAULT_MEMSTATUS=0":       "the C build turns counting off when it starts",
		"DEFAULT_WAL_SYNCHRONOUS=2": "gantry sets synchronous NORMAL on every connection",
		"DISABLE_INTRINSIC":         "how the C was compiled for translation",
		"ENABLE_COLUMN_METADATA":    "sqlite3_column_table_name and the like: a C API",
		"ENABLE_MEMORY_MANAGEMENT":  "sqlite3_release_memory: a C API",
		"ENABLE_PREUPDATE_HOOK":     "a C API",
		"ENABLE_RBU":                "a C API",
		"ENABLE_SESSION":            "a C API",
		"ENABLE_SNAPSHOT":           "a C API",
		"ENABLE_UNLOCK_NOTIFY":      "a C API",
		// C only
		"DEFAULT_WAL_SYNCHRONOUS=1":  "gantry sets synchronous NORMAL on every connection",
		"ENABLE_FTS3":                "FTS3 and FTS4 too: SQL the translated build refuses, none it runs differently",
		"ENABLE_FTS3_PARENTHESIS":    "FTS3 and FTS4 too: SQL the translated build refuses, none it runs differently",
		"ENABLE_UPDATE_DELETE_LIMIT": "UPDATE and DELETE take ORDER BY and LIMIT: SQL the translated build refuses",
		"OMIT_DEPRECATED":            "deprecated C APIs",
		"OMIT_LOAD_EXTENSION":        "no extensions loaded at run time (and no dlopen in a static binary)",
	}
	tc, err := sqlite.NewConnector(sqliteDSN(filepath.Join(t.TempDir(), "translated.sqlite3"), false))
	if err != nil {
		t.Fatal(err)
	}
	options := func(d *sql.DB) map[string]bool {
		defer d.Close()
		rows, err := d.Query(`PRAGMA compile_options`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		m := map[string]bool{}
		for rows.Next() {
			var o string
			rows.Scan(&o)
			m[o] = true
		}
		return m
	}
	translated, compiled := options(sql.OpenDB(tc)), options(sqliteOne(t))
	for o := range translated {
		if _, ok := allowed[o]; !compiled[o] && !ok {
			t.Errorf("the translated build has %s and the C build doesn't (a build tag missing?)", o)
		}
	}
	for o := range compiled {
		if _, ok := allowed[o]; !translated[o] && !ok {
			t.Errorf("the C build has %s and the translated build doesn't", o)
		}
	}
}

// conn is one of the C build's connections, without database/sql between:
// what database/sql keeps an app from doing, the driver must still survive.
func cConnFor(t *testing.T) *cConn {
	t.Helper()
	c, _ := sqliteConnector(filepath.Join(t.TempDir(), "test.sqlite3"), false)
	dc, err := c.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dc.Close() })
	return dc.(*cConn)
}

// A statement closed while its rows are open cuts them off: they don't read
// a statement that's gone.
func TestSQLiteCStmtClosedUnderRows(t *testing.T) {
	c := cConnFor(t)
	ctx := context.Background()
	st, err := c.PrepareContext(ctx, `SELECT 1 UNION ALL SELECT 2`)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := st.(*cStmt).QueryContext(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	if err := rows.Next(make([]driver.Value, 1)); !errors.Is(err, errStmtClosed) {
		t.Errorf("Next after its statement closed: %v; want %v", err, errStmtClosed)
	}
	if err := rows.Close(); err != nil {
		t.Errorf("Close after its statement closed: %v", err)
	}
}

// A run cut off by the next still says, when closed, what ending it came
// to: here a write refused when it ended.
func TestSQLiteCRunAgainKeepsItsError(t *testing.T) {
	c := cConnFor(t)
	ctx := context.Background()
	for _, q := range []string{
		`CREATE TABLE parents (id integer PRIMARY KEY)`,
		`CREATE TABLE children (id integer PRIMARY KEY, parent integer REFERENCES parents (id) DEFERRABLE INITIALLY DEFERRED)`,
	} {
		if _, err := c.ExecContext(ctx, q, nil); err != nil {
			t.Fatal(err)
		}
	}
	st, err := c.PrepareContext(ctx, `INSERT INTO children (parent) VALUES (?) RETURNING id`)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	first, err := st.(*cStmt).QueryContext(ctx, []driver.NamedValue{{Ordinal: 1, Value: int64(7)}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.(*cStmt).QueryContext(ctx, []driver.NamedValue{{Ordinal: 1, Value: nil}})
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Errorf("the second run, whose parent is NULL: %v", err)
	}
	if err := first.Close(); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Errorf("the first run, its parent missing, cut off: %v; want its FOREIGN KEY error", err)
	}
}

// A script fails when one of its statements does as it ends (a RETURNING's
// write), not only the last. (The translated build's driver gives the last
// statement's answer: the C build is the stricter here.)
func TestSQLiteCScriptFailsOnAnEarlierStatement(t *testing.T) {
	d := sqliteOne(t)
	mustExec(t, d, `CREATE TABLE parents (id integer PRIMARY KEY)`)
	mustExec(t, d, `CREATE TABLE children (id integer PRIMARY KEY, parent integer REFERENCES parents (id) DEFERRABLE INITIALLY DEFERRED)`)
	var one int
	err := d.QueryRow(`INSERT INTO children (parent) VALUES (7) RETURNING id; SELECT 1`).Scan(&one)
	if err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Errorf("a script whose first statement's write is refused: %d, %v", one, err)
	}
}
