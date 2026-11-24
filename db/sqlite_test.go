package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sqliteOne is one connection to a new database, gantry's writer's kind.
func sqliteOne(t *testing.T) *sql.DB {
	t.Helper()
	pool, err := sqlitePool(filepath.Join(t.TempDir(), "test.sqlite3"), false)
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { pool.Close() })
	return pool
}

// SQLite reads SQL no further than a NUL: what's after one isn't run, and
// isn't waited on.
func TestSQLiteNUL(t *testing.T) {
	d := sqliteOne(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mustExec(t, d, `CREATE TABLE things (name text)`)
	if _, err := d.ExecContext(ctx, "INSERT INTO things VALUES ('a');\x00INSERT INTO things VALUES ('b')"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := d.QueryRowContext(ctx, "SELECT count(*) FROM things;\x00SELECT 2").Scan(&n); err != nil || n != 1 {
		t.Errorf("count = %d, %v; want 1: the statement after the NUL isn't run", n, err)
	}
}

// A read-only transaction doesn't take the write lock, so it starts while
// another connection is writing.
func TestSQLiteReadOnlyTxWhileWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.sqlite3")
	a, _ := sqlitePool(path, false)
	b, _ := sqlitePool(path, false)
	defer a.Close()
	defer b.Close()
	mustExec(t, a, `CREATE TABLE things (name text)`)
	writing, err := a.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer writing.Rollback()
	if _, err := writing.Exec(`INSERT INTO things VALUES ('a')`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	reading, err := b.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("a read-only transaction, while another writes: %v", err)
	}
	defer reading.Rollback()
	var n int
	if err := reading.QueryRow(`SELECT count(*) FROM things`).Scan(&n); err != nil || n != 0 {
		t.Errorf("count = %d, %v; want 0, the snapshot before the write", n, err)
	}
}

// A query whose context is done after it has answered, its rows left open,
// doesn't reach what the connection runs next.
func TestSQLiteCancelAfterAnswer(t *testing.T) {
	d := sqliteOne(t)
	mustExec(t, d, `CREATE TABLE n (i integer)`)
	mustExec(t, d, `WITH RECURSIVE c(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM c WHERE i < 2000) INSERT INTO n SELECT i FROM c`)
	ctx := context.Background()
	conn, err := d.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for range 50 {
		qctx, cancel := context.WithCancel(ctx)
		rows, err := conn.QueryContext(qctx, `SELECT i FROM n`)
		if err != nil {
			t.Fatal(err)
		}
		rows.Next()
		cancel()
		rows.Close()
		var sum int
		if err := conn.QueryRowContext(ctx, `SELECT sum(a.i * b.i) FROM n a, n b WHERE a.i < 300`).Scan(&sum); err != nil {
			t.Fatalf("the next query, after another's context was done: %v", err)
		}
	}
}

// A write asked as a query that fails only when it ends (a deferred foreign
// key, checked at the end of the statement) says so when its rows are
// closed before they're read to their end, and isn't made.
func TestSQLiteReturningFailsAtClose(t *testing.T) {
	d := sqliteOne(t)
	mustExec(t, d, `CREATE TABLE parents (id integer PRIMARY KEY)`)
	mustExec(t, d, `CREATE TABLE children (id integer PRIMARY KEY, parent integer REFERENCES parents (id) DEFERRABLE INITIALLY DEFERRED)`)
	rows, err := d.Query(`INSERT INTO children (parent) VALUES (7) RETURNING id`)
	if err == nil {
		rows.Next()
		err = rows.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Errorf("a child without its parent, its RETURNING row read and closed: %v", err)
	}
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM children`).Scan(&n); err != nil || n != 0 {
		t.Errorf("children = %d, %v; want none", n, err)
	}
}

// What an app's SQL may use, on either build: full-text search, and the
// math functions.
func TestSQLiteFeatures(t *testing.T) {
	d := sqliteOne(t)
	mustExec(t, d, `CREATE VIRTUAL TABLE search USING fts5(body)`)
	mustExec(t, d, `INSERT INTO search (body) VALUES ('the quick brown fox'), ('a lazy dog')`)
	var body string
	if err := d.QueryRow(`SELECT body FROM search WHERE search MATCH 'quick'`).Scan(&body); err != nil || body != "the quick brown fox" {
		t.Errorf("full-text search: %q, %v", body, err)
	}
	var root, size float64
	if err := d.QueryRow(`SELECT sqrt(16), log2(8)`).Scan(&root, &size); err != nil || root != 4 || size != 3 {
		t.Errorf("math functions: %v %v, %v", root, size, err)
	}
	for _, q := range []string{
		`SELECT * FROM dbstat LIMIT 1`,
		`SELECT * FROM sqlite_dbpage LIMIT 1`,
		`SELECT sqlite_offset(body) FROM search LIMIT 1`,
		`SELECT geopoly_area('[[0,0],[1,0],[1,1],[0,0]]')`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Errorf("%s: %v", q, err)
		}
	}
	var sound string
	var likes bool
	if err := d.QueryRow(`SELECT soundex('Robert'), CAST('abc' AS BLOB) LIKE 'a%'`).Scan(&sound, &likes); err != nil || sound != "R163" || likes {
		t.Errorf("soundex %q, a blob LIKE text %v, %v; want R163, false", sound, likes, err)
	}
}

// A refused write's error has its SQLite code, as an app asks for it.
func TestSQLiteErrorCode(t *testing.T) {
	d := sqliteOne(t)
	mustExec(t, d, `CREATE TABLE things (name text UNIQUE)`)
	mustExec(t, d, `INSERT INTO things VALUES ('a')`)
	_, err := d.Exec(`INSERT INTO things VALUES ('a')`)
	var coded interface{ Code() int }
	if !errors.As(err, &coded) || coded.Code() != sqliteConstraintUnique {
		t.Errorf("a second 'a': %v; want an error with Code() %d", err, sqliteConstraintUnique)
	}
}

// logSize is the size of the database's write-ahead log: 0 when there's none.
func logSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path + "-wal")
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

// The write-ahead log grows to hold a transaction, however big (a seed of
// the whole database, a migration that rewrites a table), and SQLite
// reuses it after, but never gives the space back. Gantry has it cut back,
// at the next write, to what SQLite's own checkpoints need of it.
func TestSQLiteLogShrinksAfterABigWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.sqlite3")
	d, err := Open(context.Background(), "sqlite://"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	mustExec(t, d.Write, `CREATE TABLE blobs (b blob)`)
	mustExec(t, d.Write, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 4000) INSERT INTO blobs SELECT randomblob(4000) FROM n`)
	if size := logSize(t, path); size < 10<<20 {
		t.Fatalf("the log after 16 MB written in one transaction: %d bytes; the test needs it big", size)
	}
	mustExec(t, d.Write, `INSERT INTO blobs VALUES (x'00')`)
	if size, limit := logSize(t, path), int64(32+1000*(4096+24)); size > limit {
		t.Errorf("the log after the next write: %d bytes; want no more than %d, what 1,000 pages take", size, limit)
	}
}

// The log's limit is what SQLite's own checkpoints need: as many pages as
// it checkpoints at (wal_autocheckpoint), each with its frame's header, of
// the database's own page size.
func TestSQLiteLogLimitFollowsThePageSize(t *testing.T) {
	for _, pageSize := range []int64{4096, 8192} {
		path := filepath.Join(t.TempDir(), "test.sqlite3")
		// A database made elsewhere, with its own page size.
		raw, err := sqlitePool(path, false)
		if err != nil {
			t.Fatal(err)
		}
		mustExec(t, raw, fmt.Sprintf(`PRAGMA journal_mode = DELETE; PRAGMA page_size = %d; VACUUM`, pageSize))
		raw.Close()
		d, err := Open(context.Background(), "sqlite://"+path)
		if err != nil {
			t.Fatal(err)
		}
		for _, pool := range []*sql.DB{d.Write, d.Read} {
			var limit, size int64
			if err := pool.QueryRow(`PRAGMA journal_size_limit`).Scan(&limit); err != nil {
				t.Fatal(err)
			}
			pool.QueryRow(`PRAGMA page_size`).Scan(&size)
			if want := 32 + 1000*(pageSize+24); size != pageSize || limit != want {
				t.Errorf("page size %d (want %d): journal_size_limit %d; want %d", size, pageSize, limit, want)
			}
		}
		d.Close()
	}
}

// Migrations are gantry's own big writes: when they're done, the log is
// emptied, whether or not the app writes again.
func TestMigrateLeavesNoLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.sqlite3")
	d, err := Open(context.Background(), "sqlite://"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.Migrate(context.Background(), postsMigrations, "app_migrations"); err != nil {
		t.Fatal(err)
	}
	if size := logSize(t, path); size != 0 {
		t.Errorf("the log after migrating: %d bytes; want none", size)
	}
}
