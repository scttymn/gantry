// Package sqlite3way asks one question: of SQLite translated from C to Go
// (modernc.org/sqlite: what gantry runs on), SQLite compiled from C
// (mattn/go-sqlite3, through cgo) and SQLite compiled to WebAssembly
// (ncruces/go-sqlite3), which runs the bench's app's queries in the least
// CPU? Each through database/sql, on one connection, with its statements
// kept, on the challenge's database: as gantry would run it.
//
//	SQLITE3WAY_DB=/path/to/feed.db go test -bench . -benchmem
//	SQLITE3WAY_DB=/path/to/feed.db go test -bench . -benchmem -tags viawasm
//
// Twice, as the C one and the WebAssembly one both want the name "sqlite3":
// the first run has the C one, the second (-tags viawasm) the WebAssembly one.
//
// It's an experiment, in a module of its own: nothing in gantry imports it.
package sqlite3way

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

const (
	feed = `SELECT p.id, p.body, p.created_at, u.username AS author,
       (SELECT count(*) FROM likes l WHERE l.post_id = p.id) AS like_count
  FROM posts p JOIN users u ON u.id = p.user_id
 ORDER BY p.created_at DESC, p.id DESC LIMIT 20`
	post = `SELECT p.id, p.body, p.created_at, u.username AS author,
       (SELECT count(*) FROM likes l WHERE l.post_id = p.id) AS like_count
  FROM posts p JOIN users u ON u.id = p.user_id
 WHERE p.id = ?`
	like   = `INSERT INTO likes (user_id, post_id) VALUES (?, ?) ON CONFLICT (user_id, post_id) DO NOTHING`
	create = `INSERT INTO posts (user_id, body) VALUES (?, ?) RETURNING id, created_at`
)

// The pragmas are gantry's (db/sqlite.go), in each driver's spelling.
type engine struct{ name, driver, dsn string }

const pragmas = "file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_pragma=temp_store(MEMORY)"

var engines = []engine{{"translated-to-Go", "sqlite", pragmas}, other}

func open(b *testing.B, driver, dsn string) *sql.Conn {
	b.Helper()
	from := os.Getenv("SQLITE3WAY_DB")
	if from == "" {
		b.Skip("SQLITE3WAY_DB: the challenge's seeded feed.db")
	}
	path := filepath.Join(b.TempDir(), "feed.db")
	in, err := os.Open(from)
	if err != nil {
		b.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(path)
	if err != nil {
		b.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		b.Fatal(err)
	}
	out.Close()
	d, err := sql.Open(driver, fmt.Sprintf(dsn, path))
	if err != nil {
		b.Fatal(err)
	}
	d.SetMaxOpenConns(1)
	b.Cleanup(func() { d.Close() })
	c, err := d.Conn(context.Background())
	if err != nil {
		b.Fatal(err)
	}
	var mode, version string
	if err := c.QueryRowContext(context.Background(), `PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		b.Fatalf("journal_mode = %q, %v", mode, err)
	}
	c.QueryRowContext(context.Background(), `SELECT sqlite_version()`).Scan(&version)
	b.Logf("SQLite %s", version)
	return c
}

type row struct {
	id                    int64
	body, created, author string
	likes                 int64
}

func BenchmarkEngines(b *testing.B) {
	ctx := context.Background()
	for _, e := range engines {
		b.Run(e.name+"/feed", func(b *testing.B) {
			c := open(b, e.driver, e.dsn)
			st, err := c.PrepareContext(ctx, feed)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				rows, err := st.QueryContext(ctx)
				if err != nil {
					b.Fatal(err)
				}
				n := 0
				for rows.Next() {
					var r row
					if err := rows.Scan(&r.id, &r.body, &r.created, &r.author, &r.likes); err != nil {
						b.Fatal(err)
					}
					n++
				}
				if rows.Close(); n != 20 {
					b.Fatalf("%d rows, %v", n, rows.Err())
				}
			}
		})
		b.Run(e.name+"/post", func(b *testing.B) {
			c := open(b, e.driver, e.dsn)
			st, err := c.PrepareContext(ctx, post)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			id := int64(0)
			for b.Loop() {
				id = id%400000 + 1
				var r row
				if err := st.QueryRowContext(ctx, id*7919%400000+1).Scan(&r.id, &r.body, &r.created, &r.author, &r.likes); err != nil {
					b.Fatal(err)
				}
			}
		})
		// A like and a new post, each in a transaction of its own, as the
		// app's are: so it's mostly the commit, and the write to the log.
		b.Run(e.name+"/like", func(b *testing.B) {
			c := open(b, e.driver, e.dsn)
			b.ReportAllocs()
			b.ResetTimer()
			n := int64(0)
			for b.Loop() {
				n++
				tx, err := c.BeginTx(ctx, nil)
				if err != nil {
					b.Fatal(err)
				}
				if _, err := tx.ExecContext(ctx, like, n*31%50000+1, n*7919%400000+1); err != nil {
					b.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(e.name+"/create", func(b *testing.B) {
			c := open(b, e.driver, e.dsn)
			b.ReportAllocs()
			b.ResetTimer()
			n := int64(0)
			for b.Loop() {
				n++
				tx, err := c.BeginTx(ctx, nil)
				if err != nil {
					b.Fatal(err)
				}
				var id int64
				var created string
				if err := tx.QueryRowContext(ctx, create, n%50000+1, "a post of the bench's, about as long as the seeds' are, give or take a word").Scan(&id, &created); err != nil {
					b.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
