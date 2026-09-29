package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/scttymn/gantry/db"
)

// claimed is a job a worker has taken.
type claimed struct {
	ID       int64
	Name     string
	Args     []byte
	Attempts int
	Key      string
}

// claim takes the next job due on queue for process: the highest priority,
// then the earliest, among the jobs this queue set defines, skipping any
// whose limit is full (and dropping it, when its limit says to). false: none.
func (q *Queue) claim(ctx context.Context, queue, process string) (claimed, bool, error) {
	q.mu.Lock()
	var names []any
	for name, r := range q.defs {
		if r.queue() == queue {
			names = append(names, name)
		}
	}
	q.mu.Unlock()
	if len(names) == 0 {
		return claimed{}, false, nil
	}
	var got claimed
	found := false
	err := q.d.Tx(ctx, func(tx *db.Tx) error {
		in := ""
		args := []any{queue, q.now()}
		for i, n := range names {
			if i > 0 {
				in += ", "
			}
			in += fmt.Sprintf("$%d", i+3)
			args = append(args, n)
		}
		lock := ""
		if q.d.Engine == db.Postgres {
			lock = " FOR UPDATE SKIP LOCKED"
		}
		rows, err := tx.QueryContext(ctx, `SELECT id, name, args, attempts, limit_key FROM gantry_jobs
			WHERE queue = $1 AND state = 'ready' AND run_at <= $2 AND name IN (`+in+`)
			ORDER BY priority, run_at, id LIMIT 20`+lock, args...)
		if err != nil {
			return err
		}
		var candidates []claimed
		for rows.Next() {
			var c claimed
			var raw string
			if err := rows.Scan(&c.ID, &c.Name, &raw, &c.Attempts, &c.Key); err != nil {
				rows.Close()
				return err
			}
			c.Args = []byte(raw)
			candidates = append(candidates, c)
		}
		rows.Close()
		for _, c := range candidates {
			if c.Key != "" {
				ok, err := q.acquire(ctx, tx, c)
				if err != nil {
					return err
				}
				if !ok {
					q.mu.Lock()
					_, _, discard := q.defs[c.Name].limit()
					q.mu.Unlock()
					if discard {
						if _, err := tx.ExecContext(ctx, `UPDATE gantry_jobs SET state = 'failed', error = 'discarded: over its limit', finished_at = $1 WHERE id = $2`, q.now(), c.ID); err != nil {
							return err
						}
					}
					continue
				}
			}
			if _, err := tx.ExecContext(ctx, `UPDATE gantry_jobs SET state = 'claimed', claimed_by = $1, claimed_at = $2, attempts = attempts + 1 WHERE id = $3`,
				process, q.now(), c.ID); err != nil {
				return err
			}
			c.Attempts++
			got, found = c, true
			return nil
		}
		return nil
	})
	return got, found, err
}

// acquire takes a slot of c's limit, if one is free.
func (q *Queue) acquire(ctx context.Context, tx *db.Tx, c claimed) (bool, error) {
	q.mu.Lock()
	to, d, _ := q.defs[c.Name].limit()
	q.mu.Unlock()
	res, err := tx.ExecContext(ctx, `INSERT INTO gantry_job_semaphores (key, value, expires_at) VALUES ($1, $2, $3)
		ON CONFLICT (key) DO UPDATE SET value = gantry_job_semaphores.value - 1, expires_at = $3
		WHERE gantry_job_semaphores.value > 0`, c.Key, to-1, q.now().Add(d))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// release frees c's slot of its limit.
func (q *Queue) release(ctx context.Context, c claimed) {
	if c.Key == "" {
		return
	}
	if _, err := q.d.Write.ExecContext(ctx, `UPDATE gantry_job_semaphores SET value = value + 1 WHERE key = $1`, c.Key); err != nil {
		q.o.Log.Error("jobs: freeing a limit", "key", c.Key, "err", err)
	}
}

// execute runs a claimed job, and settles it: finished; or failed, retried
// or given up on; or, when stop ended its context, put back as it was.
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
	defer q.release(bg, c)
	now := q.now()
	switch {
	case err == nil:
		q.o.Log.Info("job finished", "job", c.Name, "id", c.ID, "took", time.Since(began))
		q.settle(bg, `UPDATE gantry_jobs SET state = 'finished', finished_at = $1 WHERE id = $2`, now, c.ID)
	case runCtx.Err() != nil:
		// Shutting down: back to the queue, this attempt not counted.
		q.o.Log.Info("job put back", "job", c.Name, "id", c.ID)
		q.settle(bg, `UPDATE gantry_jobs SET state = 'ready', attempts = attempts - 1, claimed_by = '', claimed_at = NULL WHERE id = $1`, c.ID)
	default:
		retry := r.retry()
		var d discarded
		if !errors.As(err, &d) && c.Attempts < retry.Attempts {
			wait := retry.wait(c.Attempts)
			q.o.Log.Warn("job failed; it'll be tried again", "job", c.Name, "id", c.ID, "attempt", c.Attempts, "in", wait, "err", err)
			q.settle(bg, `UPDATE gantry_jobs SET state = 'ready', run_at = $1, error = $2, claimed_by = '', claimed_at = NULL WHERE id = $3`, now.Add(wait), err.Error(), c.ID)
			return
		}
		q.o.Log.Error("job failed", "job", c.Name, "id", c.ID, "attempts", c.Attempts, "err", err)
		q.settle(bg, `UPDATE gantry_jobs SET state = 'failed', error = $1, finished_at = $2 WHERE id = $3`, err.Error(), now, c.ID)
		if !errors.As(err, &d) {
			r.exhausted(bg, c.Args, err)
		}
	}
}

func (q *Queue) settle(ctx context.Context, query string, args ...any) {
	if _, err := q.d.Write.ExecContext(ctx, query, args...); err != nil {
		q.o.Log.Error("jobs: settling a job", "err", err)
	}
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
// claimed go back, expired limits free, finished jobs go after Keep.
func (q *Queue) Run(ctx context.Context) error {
	host, _ := os.Hostname()
	raw := make([]byte, 4)
	rand.Read(raw)
	process := fmt.Sprintf("%s-%d-%s", host, os.Getpid(), hex.EncodeToString(raw))
	bg := context.WithoutCancel(ctx)
	if err := q.beat(bg, process); err != nil {
		return err
	}
	defer q.d.Write.ExecContext(bg, `DELETE FROM gantry_job_processes WHERE id = $1`, process)

	// Jobs run under their own context, which ends after the grace.
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
					q.signal(queue) // a slot's free: look again
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
	return nil
}

// beat says process is alive.
func (q *Queue) beat(ctx context.Context, process string) error {
	_, err := q.d.Write.ExecContext(ctx, `INSERT INTO gantry_job_processes (id, heartbeat_at) VALUES ($1, $2)
		ON CONFLICT (id) DO UPDATE SET heartbeat_at = $2`, process, q.now())
	return err
}

// maintain keeps the tables: a process silent for Dead has crashed, so the
// jobs it claimed go back (and those of no process at all); limits held
// past their time free; finished jobs older than Keep go.
func (q *Queue) maintain(ctx context.Context) error {
	now := q.now()
	return q.d.Tx(ctx, func(tx *db.Tx) error {
		for _, stmt := range []struct {
			query string
			args  []any
		}{
			{`DELETE FROM gantry_job_processes WHERE heartbeat_at < $1`, []any{now.Add(-q.o.Dead)}},
			{`UPDATE gantry_jobs SET state = 'ready', attempts = attempts - 1, claimed_by = '', claimed_at = NULL
				WHERE state = 'claimed' AND claimed_by <> 'drain' AND claimed_by NOT IN (SELECT id FROM gantry_job_processes)
				AND claimed_at < $1`, []any{now.Add(-q.o.Dead)}},
			{`DELETE FROM gantry_job_semaphores WHERE expires_at < $1`, []any{now}},
			{`DELETE FROM gantry_jobs WHERE state = 'finished' AND finished_at < $1`, []any{now.Add(-q.o.Keep)}},
		} {
			if _, err := tx.ExecContext(ctx, stmt.query, stmt.args...); err != nil {
				return err
			}
		}
		return nil
	})
}
