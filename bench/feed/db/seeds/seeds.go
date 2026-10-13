// Package seeds is the data a fresh database starts with (Rails' db/seeds):
// run at start and by gantry db seed, so it must be safe to run again.
package seeds

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/scttymn/gantry/db"
)

// Run fills an empty database for FEED_SEED_USERS users (bin/bench sets it:
// 50,000, the challenge's), and does nothing without it.
func Run(ctx context.Context, d *db.DB) error {
	users, _ := strconv.Atoi(os.Getenv("FEED_SEED_USERS"))
	if users <= 0 {
		return nil
	}
	return Fill(ctx, d, users)
}

// Fill makes the challenge's shape of data for that many users: ten posts
// each over a year, the newest last, and about four likes a post. It's the
// same rows every time, all of them or none, and a database that has users
// is left as it is.
func Fill(ctx context.Context, d *db.DB, users int) error {
	rng := rand.New(rand.NewPCG(12, 2026))
	word := func(from []string) string { return from[rng.IntN(len(from))] }
	const stamp = "2006-01-02T15:04:05.000Z"
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	posts := 10 * users
	every := 365 * 24 * time.Hour / time.Duration(posts)

	return d.Tx(ctx, func(tx *db.Tx) error {
		// Asked inside the transaction, which is the one writer: of two
		// processes starting at once, the second finds the first's users.
		var have int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT 1 FROM users LIMIT 1)`).Scan(&have); err != nil || have > 0 {
			return err
		}
		u := newRows(tx, `INSERT INTO users (id, username, created_at) VALUES `, 3)
		for id := 1; id <= users; id++ {
			if err := u.add(ctx, id, fmt.Sprint(word(adjectives), "_", word(nouns), "_", id), start.Add(-time.Duration(users-id)*time.Minute).Format(stamp)); err != nil {
				return err
			}
		}
		if err := u.flush(ctx); err != nil {
			return err
		}
		p := newRows(tx, `INSERT INTO posts (id, user_id, body, created_at) VALUES `, 4)
		l := newRows(tx, `INSERT INTO likes (user_id, post_id, created_at) VALUES `, 3)
		l.after = p // a like's post has to be there
		for id := 1; id <= posts; id++ {
			var body strings.Builder
			for n := 6 + rng.IntN(18); n > 0; n-- {
				body.WriteString(word(words))
				if n > 1 {
					body.WriteByte(' ')
				}
			}
			at := start.Add(time.Duration(id) * every).Format(stamp)
			if err := p.add(ctx, id, 1+rng.IntN(users), body.String(), at); err != nil {
				return err
			}
			// Its likes are from users one after another, so no two are one user's.
			for first, n := rng.IntN(users), min(rng.IntN(9), users); n > 0; n-- {
				if err := l.add(ctx, (first+n)%users+1, id, at); err != nil {
					return err
				}
			}
		}
		return l.flush(ctx)
	})
}

// rows inserts many rows a statement at a time: the same statement for
// every full batch, so it's compiled once.
type rows struct {
	tx     *db.Tx
	insert string // "INSERT INTO t (a, b) VALUES "
	width  int
	args   []any
	after  *rows // rows these point at, which go in first
}

const batch = 200

func newRows(tx *db.Tx, insert string, width int) *rows {
	return &rows{tx: tx, insert: insert, width: width, args: make([]any, 0, batch*width)}
}

func (r *rows) add(ctx context.Context, values ...any) error {
	r.args = append(r.args, values...)
	if len(r.args) < batch*r.width {
		return nil
	}
	return r.flush(ctx)
}

func (r *rows) flush(ctx context.Context) error {
	if r.after != nil {
		if err := r.after.flush(ctx); err != nil {
			return err
		}
	}
	n := len(r.args) / r.width
	if n == 0 {
		return nil
	}
	one := "(" + strings.TrimSuffix(strings.Repeat("?, ", r.width), ", ") + ")"
	_, err := r.tx.ExecContext(ctx, r.insert+strings.TrimSuffix(strings.Repeat(one+", ", n), ", "), r.args...)
	r.args = r.args[:0]
	return err
}

var (
	adjectives = strings.Fields("amber brave calm dusty eager fuzzy golden happy icy jolly kind lucky mellow noble odd plucky quiet rusty sunny tiny upbeat vivid witty young zesty")
	nouns      = strings.Fields("anchor badger comet delta ember falcon glacier harbor island jungle kettle lantern meadow nebula orchard pebble quartz river summit thicket umbra valley willow yonder zephyr")
	words      = strings.Fields("a after again all always and another any around back before best big bug build cache can change code coffee day deploy does down droplet each early even every fast feed find first fix for from good great had hard has have here how idea if in into is it its just keep last late later less like little long made make many more most much my never new next no not now of off old on once one only or other our out over own page part people plan post project quick rarely read really request right run same server ship should side simple slow small so some soon still such take test than that the their them then there these they thing this time to today tomorrow too try two under up use very want was way we weird well what when where which while who why will with work would write year yet you your")
)
