package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/adhocore/gronx"
)

// recurring is a job enqueued on a schedule (Solid Queue's recurring.yml,
// in code).
type recurring struct {
	task     string // the job, its schedule and its arguments: one tick each
	name     string
	queue    string
	priority int
	key      string
	args     []byte
	next     func(after time.Time) time.Time
	due      time.Time
}

// Every enqueues the job with a every d, at times that are multiples of d
// (every 10 minutes: at :00, :10, ...), so every process agrees on them.
func (j *Job[A]) Every(d time.Duration, a A) {
	j.recur(fmt.Sprintf("every %s", d), a, func(after time.Time) time.Time {
		return after.UTC().Truncate(d).Add(d)
	})
}

// Cron enqueues the job with a on a cron schedule ("0 3 * * *": at three
// every morning), in loc.
func (j *Job[A]) Cron(expr string, loc *time.Location, a A) error {
	if !gronx.IsValid(expr) {
		return fmt.Errorf("jobs: %q isn't a cron schedule", expr)
	}
	j.recur(fmt.Sprintf("cron %s %s", expr, loc), a, func(after time.Time) time.Time {
		next, err := gronx.NextTickAfter(expr, after.In(loc), false)
		if err != nil {
			return after.Add(24 * time.Hour * 366) // never, in practice
		}
		return next.UTC()
	})
	return nil
}

func (j *Job[A]) recur(schedule string, a A, next func(time.Time) time.Time) {
	args, err := json.Marshal(a)
	if err != nil {
		panic(fmt.Sprintf("jobs: %s's arguments: %v", j.name, err))
	}
	sum := sha256.Sum256(args)
	key := ""
	if j.opts.Limit != nil {
		key = j.name + ":" + j.opts.Limit.Key(a)
	}
	r := &recurring{task: j.name + " " + schedule + " " + hex.EncodeToString(sum[:4]), name: j.name, queue: j.opts.Queue,
		priority: j.opts.Priority, key: key, args: args, next: next}
	r.due = next(j.q.now())
	j.q.mu.Lock()
	j.q.recur = append(j.q.recur, r)
	j.q.mu.Unlock()
}

// tick enqueues each recurring job that's due, once however many processes
// tick: its run is recorded first, and only the process that records it
// enqueues it. Runs missed while nothing ticked aren't made up: the next is
// the next after now (Solid Queue's).
func (q *Queue) tick(ctx context.Context) error {
	now := q.now()
	q.mu.Lock()
	due := []*recurring{}
	for _, r := range q.recur {
		if !now.Before(r.due) {
			due = append(due, r)
		}
	}
	q.mu.Unlock()
	for _, r := range due {
		res, err := q.d.Write.ExecContext(ctx, `INSERT INTO gantry_job_ticks (task, run_at) VALUES ($1, $2) ON CONFLICT DO NOTHING`, r.task, r.due)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			if _, err := q.d.Write.ExecContext(ctx, `INSERT INTO gantry_jobs (queue, name, args, priority, run_at, limit_key, created_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7)`, r.queue, r.name, string(r.args), r.priority, r.due, r.key, now); err != nil {
				return err
			}
			q.o.Log.Info("job enqueued", "job", r.name, "recurring", r.task, "at", r.due)
			q.signal(r.queue)
		}
		q.mu.Lock()
		r.due = r.next(now)
		q.mu.Unlock()
	}
	return nil
}
