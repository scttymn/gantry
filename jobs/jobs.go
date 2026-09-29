// Package jobs runs an app's background work (Rails' Active Job and Solid
// Queue): in the app's own database and, by default, its own process.
//
//	q, err := jobs.New(ctx, d, jobs.Options{})
//	backup := jobs.Define(q, "backup", func(ctx context.Context, a BackupArgs) error { ... },
//		jobs.Opts[BackupArgs]{Retry: jobs.Retry{Attempts: 5, Wait: 3 * time.Second}})
//	backup.Enqueue(ctx, BackupArgs{Project: "shop"})
//	go q.Run(ctx)
//
// A job is a name, an argument struct (stored as JSON) and a function. The
// name, not the Go type, identifies it, so renaming code doesn't strand the
// jobs queued. Defaults are Rails': no retries unless asked, three workers
// a queue, finished jobs kept a day.
package jobs

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/scttymn/gantry/db"
)

//go:embed migrations
var migrations embed.FS

// Options are a queue set's settings; each zero value takes Rails' (or Solid
// Queue's) default.
type Options struct {
	// Queues is each queue's workers, how many of its jobs run at once:
	// {"default": 3} when nil.
	Queues    map[string]int
	Log       *slog.Logger
	Now       func() time.Time
	Poll      time.Duration // how often workers look for jobs that came due: 1 s
	Grace     time.Duration // on shutdown, what running jobs get before their contexts end: 5 s
	Heartbeat time.Duration // how often this process says it's alive: 60 s
	Dead      time.Duration // a process silent this long crashed, and its jobs go back: 5 min
	Keep      time.Duration // how long finished jobs are kept: a day
}

// Queue is an app's jobs: the ones it defines, and the workers that run
// them (Run).
type Queue struct {
	d       *db.DB
	o       Options
	mu      sync.Mutex
	defs    map[string]runner
	recur   []*recurring
	wake    map[string]chan struct{}
	process string
}

// New is a queue set on d, its tables brought up to date.
func New(ctx context.Context, d *db.DB, o Options) (*Queue, error) {
	if o.Queues == nil {
		o.Queues = map[string]int{"default": 3}
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	for p, v := range map[*time.Duration]time.Duration{&o.Poll: time.Second, &o.Grace: 5 * time.Second, &o.Heartbeat: time.Minute, &o.Dead: 5 * time.Minute, &o.Keep: 24 * time.Hour} {
		if *p == 0 {
			*p = v
		}
	}
	engine := "sqlite"
	if d.Engine == db.Postgres {
		engine = "postgres"
	}
	sub, _ := fs.Sub(migrations, "migrations/"+engine)
	if err := d.Migrate(ctx, sub, "gantry_jobs_migrations"); err != nil {
		return nil, err
	}
	q := &Queue{d: d, o: o, defs: map[string]runner{}, wake: map[string]chan struct{}{}}
	for name := range o.Queues {
		q.wake[name] = make(chan struct{}, 1)
	}
	return q, nil
}

func (q *Queue) now() time.Time { return q.o.Now().UTC() }

// runner is a defined job, whatever its argument type.
type runner interface {
	queue() string
	perform(ctx context.Context, args []byte) error
	exhausted(ctx context.Context, args []byte, err error)
	retry() Retry
	timeout() time.Duration
	limit() (to int, d time.Duration, discard bool)
}

// Retry is how a failing job is tried again (Rails' retry_on): Attempts in
// all, with Wait between, or Backoff's wait after each attempt. None
// (Active Job's default): one attempt.
type Retry struct {
	Attempts int
	Wait     time.Duration
	Backoff  func(attempt int) time.Duration
}

// Polynomial is Rails' :polynomially_longer wait after an attempt,
// attempt⁴ + 2 seconds, without its random jitter: 3 s, 18 s, 83 s...
func Polynomial(attempt int) time.Duration {
	return time.Duration(math.Pow(float64(attempt), 4)+2) * time.Second
}

func (r Retry) wait(attempt int) time.Duration {
	if r.Backoff != nil {
		return r.Backoff(attempt)
	}
	return r.Wait
}

// Limit is at most To of a job at once per Key (Solid Queue's
// limits_concurrency): one deploy per project, say. A job over the limit
// waits its turn, or with Discard is dropped. A crashed holder's slot
// frees after Duration (3 minutes when zero).
type Limit[A any] struct {
	To       int
	Key      func(A) string
	Duration time.Duration
	Discard  bool
}

// Opts are a job's settings.
type Opts[A any] struct {
	Queue    string // "default" when empty
	Priority int    // lower runs first; 0 by default
	Retry    Retry
	Timeout  time.Duration // the job's context ends after it; none by default
	Limit    *Limit[A]
	// OnExhausted runs when the last attempt fails (Rails' retry_on block).
	OnExhausted func(ctx context.Context, a A, err error)
}

// Job is a defined job, to enqueue.
type Job[A any] struct {
	q    *Queue
	name string
	opts Opts[A]
	fn   func(context.Context, A) error
}

// Define is a job named name, which runs perform with its arguments. A name
// is defined once per queue set.
func Define[A any](q *Queue, name string, perform func(context.Context, A) error, o Opts[A]) *Job[A] {
	if o.Queue == "" {
		o.Queue = "default"
	}
	j := &Job[A]{q: q, name: name, opts: o, fn: perform}
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.defs[name]; ok {
		panic("jobs: " + name + " is defined twice")
	}
	q.defs[name] = j
	return j
}

func (j *Job[A]) queue() string          { return j.opts.Queue }
func (j *Job[A]) retry() Retry           { return j.opts.Retry }
func (j *Job[A]) timeout() time.Duration { return j.opts.Timeout }
func (j *Job[A]) limit() (int, time.Duration, bool) {
	if j.opts.Limit == nil {
		return 0, 0, false
	}
	d := j.opts.Limit.Duration
	if d == 0 {
		d = 3 * time.Minute
	}
	return j.opts.Limit.To, d, j.opts.Limit.Discard
}

func (j *Job[A]) perform(ctx context.Context, raw []byte) error {
	var a A
	if err := json.Unmarshal(raw, &a); err != nil {
		return Discard(fmt.Errorf("jobs: %s's arguments don't read: %w", j.name, err))
	}
	return j.fn(ctx, a)
}

func (j *Job[A]) exhausted(ctx context.Context, raw []byte, err error) {
	if j.opts.OnExhausted == nil {
		return
	}
	var a A
	json.Unmarshal(raw, &a)
	j.opts.OnExhausted(ctx, a, err)
}

// Enqueue queues the job to run now.
func (j *Job[A]) Enqueue(ctx context.Context, a A) (int64, error) {
	return j.EnqueueAt(ctx, a, j.q.now())
}

// EnqueueIn queues the job to run after d.
func (j *Job[A]) EnqueueIn(ctx context.Context, a A, d time.Duration) (int64, error) {
	return j.EnqueueAt(ctx, a, j.q.now().Add(d))
}

// EnqueueAt queues the job to run at t.
func (j *Job[A]) EnqueueAt(ctx context.Context, a A, t time.Time) (int64, error) {
	id, err := j.insert(ctx, j.q.d.Write, a, t)
	if err == nil {
		j.q.signal(j.opts.Queue)
	}
	return id, err
}

// EnqueueTx queues the job inside tx: it's saved with the change, or not at
// all, and the workers hear of it once the transaction commits.
func (j *Job[A]) EnqueueTx(ctx context.Context, tx *db.Tx, a A) (int64, error) {
	id, err := j.insert(ctx, tx, a, j.q.now())
	if err == nil {
		tx.AfterCommit(func() { j.q.signal(j.opts.Queue) })
	}
	return id, err
}

func (j *Job[A]) insert(ctx context.Context, x db.Querier, a A, at time.Time) (int64, error) {
	args, err := json.Marshal(a)
	if err != nil {
		return 0, fmt.Errorf("jobs: %s's arguments: %w", j.name, err)
	}
	key := ""
	if j.opts.Limit != nil {
		key = j.name + ":" + j.opts.Limit.Key(a)
	}
	var id int64
	err = x.QueryRowContext(ctx, `INSERT INTO gantry_jobs (queue, name, args, priority, run_at, limit_key, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		j.opts.Queue, j.name, string(args), j.opts.Priority, at.UTC(), key, j.q.now()).Scan(&id)
	if err == nil {
		j.q.o.Log.Info("job enqueued", "job", j.name, "id", id, "queue", j.opts.Queue, "at", at.UTC())
	}
	return id, err
}

// signal wakes queue's workers, when they're waiting.
func (q *Queue) signal(queue string) {
	if ch, ok := q.wake[queue]; ok {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// discarded is a failure not to try again.
type discarded struct{ err error }

func (d discarded) Error() string { return d.err.Error() }
func (d discarded) Unwrap() error { return d.err }

// Discard gives up on the job at once, whatever its retries (Rails'
// discard_on): the project it was for is gone, say.
func Discard(err error) error { return discarded{err} }

// Entry is a job in the queue, for looking at.
type Entry struct {
	ID       int64
	Name     string
	Queue    string
	Args     json.RawMessage
	RunAt    time.Time
	Attempts int
	State    string // ready, claimed, finished or failed
	Error    string
}

// Pending is the jobs waiting or running, in the order they'll run.
func (q *Queue) Pending(ctx context.Context) ([]Entry, error) {
	return q.list(ctx, `state IN ('ready', 'claimed') ORDER BY priority, run_at, id`)
}

// Failed is the jobs that failed, newest first, to look at and Retry.
func (q *Queue) Failed(ctx context.Context) ([]Entry, error) {
	return q.list(ctx, `state = 'failed' ORDER BY finished_at DESC, id DESC`)
}

func (q *Queue) list(ctx context.Context, where string) ([]Entry, error) {
	rows, err := q.d.Read.QueryContext(ctx, `SELECT id, name, queue, args, run_at, attempts, state, error FROM gantry_jobs WHERE `+where)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		var args string
		if err := rows.Scan(&e.ID, &e.Name, &e.Queue, &args, &e.RunAt, &e.Attempts, &e.State, &e.Error); err != nil {
			return nil, err
		}
		e.Args = json.RawMessage(args)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Retry puts a failed job back in the queue, to run now with its attempts
// counted afresh.
func (q *Queue) Retry(ctx context.Context, id int64) error {
	res, err := q.d.Write.ExecContext(ctx, `UPDATE gantry_jobs SET state = 'ready', attempts = 0, error = '', finished_at = NULL, run_at = $1
		WHERE id = $2 AND state = 'failed'`, q.now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("jobs: no failed job with that id")
	}
	var queue string
	q.d.Read.QueryRowContext(ctx, `SELECT queue FROM gantry_jobs WHERE id = $1`, id).Scan(&queue)
	q.signal(queue)
	return nil
}
