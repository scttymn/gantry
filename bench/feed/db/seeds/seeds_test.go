package seeds_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/scttymn/gantry/db"

	"feed/db/seeds"
	"feed/test"
)

// rows is every row of a table, as text, in order.
func rows(t *testing.T, d *db.DB, table, order string) string {
	t.Helper()
	r, err := d.Read.QueryContext(context.Background(), `SELECT * FROM `+table+` ORDER BY `+order)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	cols, _ := r.Columns()
	var b strings.Builder
	for r.Next() {
		row := make([]any, len(cols))
		dest := make([]any, len(cols))
		for i := range row {
			dest[i] = &row[i]
		}
		if err := r.Scan(dest...); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(&b, row...)
	}
	return b.String()
}

func count(t *testing.T, d *db.DB, query string) (n int) {
	t.Helper()
	if err := d.Read.QueryRowContext(context.Background(), query).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// The seeds make the challenge's shape of data: ten posts a user, about
// four likes a post, the same rows every time, in the wire's timestamps,
// with no like of a user or a post that isn't there; and a database that
// has its users is left as it is.
func TestSeed(t *testing.T) {
	ctx := context.Background()
	const users = 40
	d, other := test.DB(t), test.DB(t)
	for _, d := range []*db.DB{d, other} {
		if err := seeds.Fill(ctx, d, users); err != nil {
			t.Fatal(err)
		}
	}
	if got := count(t, d, `SELECT count(*) FROM users`); got != users {
		t.Errorf("%d users; want %d", got, users)
	}
	posts := count(t, d, `SELECT count(*) FROM posts`)
	if posts != 10*users {
		t.Errorf("%d posts; want %d", posts, 10*users)
	}
	if likes := count(t, d, `SELECT count(*) FROM likes`); likes < 3*posts || likes > 5*posts {
		t.Errorf("%d likes of %d posts; want about four each", likes, posts)
	}
	if got := count(t, d, `SELECT count(*) FROM posts WHERE length(body) NOT BETWEEN 1 AND 500 OR created_at NOT GLOB '20[0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'`); got != 0 {
		t.Errorf("%d posts with a body or a time the API couldn't send as it is", got)
	}
	// The newest posts are the last made, so the feed is theirs.
	if got := count(t, d, `SELECT count(*) FROM (SELECT id, lag(id) OVER (ORDER BY created_at, id) AS before FROM posts) WHERE id < before`); got != 0 {
		t.Errorf("%d posts older than the one before them", got)
	}
	if got := count(t, d, `SELECT count(*) FROM pragma_foreign_key_check`); got != 0 {
		t.Errorf("%d rows pointing at a user or a post that isn't there", got)
	}
	for table, order := range map[string]string{"users": "id", "posts": "id", "likes": "post_id, user_id"} {
		if a, b := rows(t, d, table, order), rows(t, other, table, order); a != b || a == "" {
			t.Errorf("%s isn't the same rows in two databases (or is empty)", table)
		}
	}

	before := rows(t, d, "likes", "post_id, user_id")
	if err := seeds.Fill(ctx, d, users); err != nil {
		t.Fatal(err)
	}
	if got := count(t, d, `SELECT count(*) FROM users`); got != users || rows(t, d, "likes", "post_id, user_id") != before {
		t.Errorf("a second run changed a database that had its users (%d of them now)", got)
	}
}

// Two processes starting on one empty database both seed it: one does, and
// the other finds its users there, with no error and nothing doubled.
func TestSeedTwiceAtOnce(t *testing.T) {
	ctx := context.Background()
	d := test.DB(t)
	errs := make(chan error, 2)
	for range 2 {
		go func() { errs <- seeds.Fill(ctx, d, 200) }()
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Errorf("seeding at once: %v", err)
		}
	}
	if got := count(t, d, `SELECT count(*) FROM users`); got != 200 {
		t.Errorf("%d users; want 200", got)
	}
}

// Run seeds only when it's asked to: an app started without
// FEED_SEED_USERS has an empty database.
func TestRunNeedsAsking(t *testing.T) {
	ctx := context.Background()
	d := test.DB(t)
	t.Setenv("FEED_SEED_USERS", "")
	if err := seeds.Run(ctx, d); err != nil || count(t, d, `SELECT count(*) FROM users`) != 0 {
		t.Errorf("with nothing asked: %v, %d users", err, count(t, d, `SELECT count(*) FROM users`))
	}
	t.Setenv("FEED_SEED_USERS", "3")
	if err := seeds.Run(ctx, d); err != nil || count(t, d, `SELECT count(*) FROM users`) != 3 {
		t.Errorf("asked for 3: %v, %d users", err, count(t, d, `SELECT count(*) FROM users`))
	}
}
