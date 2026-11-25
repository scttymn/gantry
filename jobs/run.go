package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/scttymn/gantry/db"
)

type claimed struct {
	ID       int64
	Name     string
	Args     []byte
	Attempts int
	Key      string
	// the process that claimed it
	By string
	// when: with By, which claim this is
	At time.Time
}

func (q *Queue) claim(ctx context.Context, queue, process string) (claimed, bool, error) {
	q.mu.Lock()
	var names, discards []any
	for name, r := range q.defs {
		if r.queue() == queue {
			names = append(names, name)
			if _, _, discard := r.limit(); discard {
				discards = append(discards, name)
			}
		}
	}
	q.mu.Unlock()
	if len(names) == 0 {
		return claimed{}, false, nil
	}
	for {
		var got claimed
		found, dropped := false, false
		err := q.d.Tx(ctx, func(tx *db.Tx) error {
			full, err := q.full(ctx, tx)
			if err != nil {
				return err
			}
			candidates, err := q.candidates(ctx, tx, queue, names, discards, full)
			if err != nil {
				return err
			}
			if q.d.Engine == db.Postgres {
				if err := lockKeys(ctx, tx, candidates); err != nil {
					return err
				}
				if full, err = q.full(ctx, tx); err != nil {
					return err
				}
			}
			for _, c := range candidates {
				if full[c.Key] {
					q.mu.Lock()
					_, _, discard := q.defs[c.Name].limit()
					q.mu.Unlock()
					if discard {
						if _, err := tx.ExecContext(ctx, `UPDATE gantry_jobs SET state = 'failed', error = 'discarded: over its limit', finished_at = $1 WHERE id = $2`, q.now(), c.ID); err != nil {
							return err
						}
						dropped = true
					}
					continue
				}
				c.By, c.At = process, q.now().Truncate(time.Microsecond)
				if _, err := tx.ExecContext(ctx, `UPDATE gantry_jobs SET state = 'claimed', claimed_by = $1, claimed_at = $2, attempts = attempts + 1 WHERE id = $3`,
					c.By, c.At, c.ID); err != nil {
					return err
				}
				c.Attempts++
				got, found = c, true
				return nil
			}
			return nil
		})
		if err != nil || found || !dropped {
			return got, found, err
		}
	}
}

func (q *Queue) candidates(ctx context.Context, tx *db.Tx, queue string, names, discards []any, full map[string]bool) ([]claimed, error) {
	args := []any{queue, q.now()}
	in := func(vs []any) string {
		s := ""
		for i, v := range vs {
			if i > 0 {
				s += ", "
			}
			args = append(args, v)
			s += fmt.Sprintf("$%d", len(args))
		}
		return s
	}
	where := `queue = $1 AND state = 'ready' AND run_at <= $2 AND name IN (` + in(names) + `)`
	if len(full) > 0 {
		var keys []any
		for k := range full {
			keys = append(keys, k)
		}
		skip := `limit_key NOT IN (` + in(keys) + `)`
		if len(discards) > 0 {
			skip = `(` + skip + ` OR name IN (` + in(discards) + `))`
		}
		where += ` AND ` + skip
	}
	lock := ""
	if q.d.Engine == db.Postgres {
		lock = " FOR UPDATE SKIP LOCKED"
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, name, args, attempts, limit_key FROM gantry_jobs WHERE `+where+`
		ORDER BY priority, run_at, id LIMIT 20`+lock, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []claimed
	for rows.Next() {
		var c claimed
		var raw string
		if err := rows.Scan(&c.ID, &c.Name, &raw, &c.Attempts, &c.Key); err != nil {
			return nil, err
		}
		c.Args = []byte(raw)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (q *Queue) full(ctx context.Context, tx *db.Tx) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name, limit_key, claimed_at FROM gantry_jobs WHERE state = 'claimed' AND limit_key <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := q.now()
	held, full := map[string]int{}, map[string]bool{}
	for rows.Next() {
		var name, key string
		var at time.Time
		if err := rows.Scan(&name, &key, &at); err != nil {
			return nil, err
		}
		q.mu.Lock()
		r, ok := q.defs[name]
		q.mu.Unlock()
		if !ok {
			continue
		}
		if to, d, _ := r.limit(); at.After(now.Add(-d)) {
			held[key]++
			if held[key] >= to {
				full[key] = true
			}
		}
	}
	return full, rows.Err()
}

func lockKeys(ctx context.Context, tx *db.Tx, candidates []claimed) error {
	var keys []string
	for _, c := range candidates {
		if c.Key != "" {
			keys = append(keys, c.Key)
		}
	}
	slices.Sort(keys)
	for _, k := range slices.Compact(keys) {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('gantry_jobs'), hashtext($1))`, k); err != nil {
			return err
		}
	}
	return nil
}

func (q *Queue) execute(runCtx context.Context, c claimed) {
	q.mu.Lock()
	r := q.defs[c.Name]
	q.mu.Unlock()
	ctx := runCtx
	if t := r.timeout(); t > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(runCtx, t)
		defer cancel()
	}
	began := time.Now()
	q.o.Log.Info("job started", "job", c.Name, "id", c.ID, "attempt", c.Attempts)
	err := func() (err error) {
		defer func() {
			if v := recover(); v != nil {
				err = fmt.Errorf("panic: %v", v)
			}
		}()
		return r.perform(ctx, c.Args)
	}()
	bg := context.WithoutCancel(runCtx)
	now := q.now()
	switch {
	case err == nil:
		q.o.Log.Info("job finished", "job", c.Name, "id", c.ID, "took", time.Since(began))
		q.settle(bg, c, `state = 'finished', finished_at = $1`, now)
	case runCtx.Err() != nil:
		q.o.Log.Info("job put back", "job", c.Name, "id", c.ID)
		q.settle(bg, c, `state = 'ready', attempts = attempts - 1, claimed_by = '', claimed_at = NULL`)
	default:
		retry := r.retry()
		var d discarded
		if !errors.As(err, &d) && c.Attempts < retry.Attempts {
			wait := retry.wait(c.Attempts)
			q.o.Log.Warn("job failed; it'll be tried again", "job", c.Name, "id", c.ID, "attempt", c.Attempts, "in", wait, "err", err)
			q.settle(bg, c, `state = 'ready', run_at = $1, error = $2, claimed_by = '', claimed_at = NULL`, now.Add(wait), err.Error())
			return
		}
		q.o.Log.Error("job failed", "job", c.Name, "id", c.ID, "attempts", c.Attempts, "err", err)
		if q.settle(bg, c, `state = 'failed', error = $1, finished_at = $2`, err.Error(), now) && !errors.As(err, &d) {
			r.exhausted(bg, c.Args, err)
		}
	}
}

func (q *Queue) settle(ctx context.Context, c claimed, set string, args ...any) bool {
	n := len(args)
	query := fmt.Sprintf(`UPDATE gantry_jobs SET %s WHERE id = $%d AND state = 'claimed' AND claimed_by = $%d AND claimed_at = $%d`, set, n+1, n+2, n+3)
	res, err := q.d.Write.ExecContext(ctx, query, append(args, c.ID, c.By, c.At)...)
	if err != nil {
		q.o.Log.Error("jobs: settling a job", "job", c.Name, "id", c.ID, "err", err)
		return false
	}
	saved, err := res.RowsAffected()
	if err != nil {
		q.o.Log.Error("jobs: settling a job", "job", c.Name, "id", c.ID, "err", err)
		return false
	}
	if saved == 0 {
		q.o.Log.Warn("job lost its claim while it ran; its result is dropped", "job", c.Name, "id", c.ID)
	}
	return saved == 1
}

// Drain runs every job that's due, recurring ones included, in the caller,
// until none is: Rails' perform_enqueued_jobs, for tests.
func (q *Queue) Drain(ctx context.Context) error {
	if err := q.tick(ctx); err != nil {
		return err
	}
	for {
		ran := false
		for queue := range q.o.Queues {
			c, ok, err := q.claim(ctx, queue, "drain")
			if err != nil {
				return err
			}
			if ok {
				q.execute(ctx, c)
				ran = true
			}
		}
		if !ran {
			return nil
		}
	}
}

// Run runs the queues' workers until ctx ends, then stops taking jobs,
// gives the running ones their grace, ends their contexts, and returns.
// Its process heartbeats, and keeps the tables: jobs a crashed process
// claimed go back, finished jobs go after Keep.
func (q *Queue) Run(ctx context.Context) error {
	host, _ := os.Hostname()
	raw := make([]byte, 4)
	rand.Read(raw)
	process := fmt.Sprintf("%s-%d-%s", host, os.Getpid(), hex.EncodeToString(raw))
	bg := context.WithoutCancel(ctx)
	if err := q.beat(bg, process); err != nil {
		return err
	}
	defer func() { _, _ = q.d.Write.ExecContext(bg, `DELETE FROM gantry_job_processes WHERE id = $1`, process) }()

	jobCtx, endJobs := context.WithCancel(bg)
	defer endJobs()
	var running sync.WaitGroup
	var workers sync.WaitGroup
	for queue, n := range q.o.Queues {
		slots := make(chan struct{}, max(n, 1))
		workers.Go(func() {
			for {
				select {
				case slots <- struct{}{}:
				case <-ctx.Done():
					return
				}
				c, ok, err := q.claim(bg, queue, process)
				if err != nil {
					q.o.Log.Error("jobs: claiming", "queue", queue, "err", err)
				}
				if !ok {
					<-slots
					select {
					case <-q.wake[queue]:
					case <-time.After(q.o.Poll):
					case <-ctx.Done():
						return
					}
					continue
				}
				running.Go(func() {
					defer func() { <-slots }()
					q.execute(jobCtx, c)
					q.signal(queue)
				})
			}
		})
	}
	workers.Go(func() {
		for {
			select {
			case <-time.After(min(q.o.Heartbeat, q.o.Poll)):
			case <-ctx.Done():
				return
			}
			if err := q.tick(bg); err != nil {
				q.o.Log.Error("jobs: recurring", "err", err)
			}
		}
	})
	workers.Go(func() {
		for {
			select {
			case <-time.After(q.o.Heartbeat):
			case <-ctx.Done():
				return
			}
			if err := q.beat(bg, process); err != nil {
				q.o.Log.Error("jobs: heartbeat", "err", err)
			}
			if err := q.maintain(bg); err != nil {
				q.o.Log.Error("jobs: maintenance", "err", err)
			}
		}
	})
	workers.Wait()
	done := make(chan struct{})
	go func() { running.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(q.o.Grace):
		endJobs()
		<-done
	}
	q.hooks.Wait()
	return nil
}

func (q *Queue) beat(ctx context.Context, process string) error {
	_, err := q.d.Write.ExecContext(ctx, `INSERT INTO gantry_job_processes (id, heartbeat_at) VALUES ($1, $2)
		ON CONFLICT (id) DO UPDATE SET heartbeat_at = $2`, process, q.now())
	return err
}

func (q *Queue) maintain(ctx context.Context) error {
	now := q.now()
	var failed []claimed
	err := q.d.Tx(ctx, func(tx *db.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM gantry_job_processes WHERE heartbeat_at < $1`, now.Add(-q.o.Dead)); err != nil {
			return err
		}
		var err error
		if failed, err = q.failCrashed(ctx, tx, now); err != nil {
			return err
		}
		for _, stmt := range []struct {
			query string
			args  []any
		}{
			{`UPDATE gantry_jobs SET state = 'ready', attempts = attempts - 1, crashes = crashes + 1, claimed_by = '', claimed_at = NULL
				WHERE state = 'claimed' AND claimed_by <> 'drain' AND claimed_by NOT IN (SELECT id FROM gantry_job_processes)
				AND claimed_at < $1`, []any{now.Add(-q.o.Dead)}},
			{`DELETE FROM gantry_jobs WHERE state = 'finished' AND finished_at < $1`, []any{now.Add(-q.o.Keep)}},
			{`DELETE FROM gantry_job_ticks WHERE run_at < $1`, []any{now.Add(-q.o.Keep - tickMargin)}},
		} {
			if _, err := tx.ExecContext(ctx, stmt.query, stmt.args...); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	bg := context.WithoutCancel(ctx)
	for _, c := range failed {
		q.o.Log.Error("job failed", "job", c.Name, "id", c.ID, "err", errCrashed)
		q.mu.Lock()
		r, ok := q.defs[c.Name]
		q.mu.Unlock()
		if ok {
			q.hooks.Go(func() { r.exhausted(bg, c.Args, errCrashed) })
		}
	}
	return nil
}

func (q *Queue) failCrashed(ctx context.Context, tx *db.Tx, now time.Time) ([]claimed, error) {
	rows, err := tx.QueryContext(ctx, `UPDATE gantry_jobs SET state = 'failed', attempts = attempts - 1, crashes = crashes + 1, error = $1, finished_at = $2
		WHERE state = 'claimed' AND claimed_by <> 'drain' AND claimed_by NOT IN (SELECT id FROM gantry_job_processes)
		AND claimed_at < $3 AND crashes + 1 >= $4
		RETURNING id, name, args`, errCrashed.Error(), now, now.Add(-q.o.Dead), maxCrashes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []claimed
	for rows.Next() {
		var c claimed
		var raw string
		if err := rows.Scan(&c.ID, &c.Name, &raw); err != nil {
			return nil, err
		}
		c.Args = []byte(raw)
		out = append(out, c)
	}
	return out, rows.Err()
}
