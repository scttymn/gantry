package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/testkit"
)

type backupArgs struct {
	Project string `json:"project"`
	Keep    int    `json:"keep"`
}

var start = time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

// queue is a queue on a fresh database, on a clock the test moves.
func queue(t *testing.T, o Options) (*Queue, *db.DB, *testkit.FakeClock) {
	t.Helper()
	// On Postgres when there's one to test on (a database per test), else SQLite.
	var d *db.DB
	if os.Getenv("TEST_DATABASE_URL") != "" {
		d = testkit.Postgres(t, nil)
	} else {
		var err error
		if d, err = db.Open(context.Background(), "sqlite://"+filepath.Join(t.TempDir(), "jobs.sqlite3")); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { d.Close() })
	}
	clock := testkit.Clock(start)
	if o.Now == nil {
		o.Now = clock.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	q, err := New(context.Background(), d, o)
	if err != nil {
		t.Fatal(err)
	}
	return q, d, clock
}

// ran records a job's runs.
type ran struct {
	mu   sync.Mutex
	args []backupArgs
}

func (r *ran) add(a backupArgs) { r.mu.Lock(); r.args = append(r.args, a); r.mu.Unlock() }
func (r *ran) count() int       { r.mu.Lock(); defer r.mu.Unlock(); return len(r.args) }

func drain(t *testing.T, q *Queue) {
	t.Helper()
	if err := q.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestEnqueueAndRun(t *testing.T) {
	ctx := context.Background()
	q, _, _ := queue(t, Options{})
	var r ran
	backup := Define(q, "backup", func(ctx context.Context, a backupArgs) error { r.add(a); return nil }, Opts[backupArgs]{})
	if _, err := backup.Enqueue(ctx, backupArgs{Project: "shop", Keep: 7}); err != nil {
		t.Fatal(err)
	}
	if p, _ := q.Pending(ctx); len(p) != 1 || p[0].Name != "backup" || string(p[0].Args) != `{"project":"shop","keep":7}` || p[0].Queue != "default" {
		t.Fatalf("pending: %+v", p)
	}
	drain(t, q)
	if r.count() != 1 || r.args[0] != (backupArgs{"shop", 7}) {
		t.Fatalf("ran %+v", r.args)
	}
	if p, _ := q.Pending(ctx); len(p) != 0 {
		t.Errorf("still pending: %+v", p)
	}
}

// Inside a transaction, a job is saved with the change or not at all.
func TestEnqueueTx(t *testing.T) {
	ctx := context.Background()
	q, d, _ := queue(t, Options{})
	var r ran
	backup := Define(q, "backup", func(ctx context.Context, a backupArgs) error { r.add(a); return nil }, Opts[backupArgs]{})
	d.Tx(ctx, func(tx *db.Tx) error {
		backup.EnqueueTx(ctx, tx, backupArgs{Project: "rolled back"})
		return errors.New("no")
	})
	d.Tx(ctx, func(tx *db.Tx) error {
		_, err := backup.EnqueueTx(ctx, tx, backupArgs{Project: "committed"})
		return err
	})
	drain(t, q)
	if r.count() != 1 || r.args[0].Project != "committed" {
		t.Errorf("ran %+v", r.args)
	}
}

func TestScheduledAndPriority(t *testing.T) {
	ctx := context.Background()
	q, _, clock := queue(t, Options{})
	var order []string
	var mu sync.Mutex
	job := Define(q, "note", func(ctx context.Context, s string) error {
		mu.Lock()
		order = append(order, s)
		mu.Unlock()
		return nil
	}, Opts[string]{})
	urgent := Define(q, "urgent", func(ctx context.Context, s string) error {
		mu.Lock()
		order = append(order, s)
		mu.Unlock()
		return nil
	}, Opts[string]{Priority: -1})
	job.EnqueueIn(ctx, "in an hour", time.Hour)
	job.Enqueue(ctx, "now")
	urgent.Enqueue(ctx, "urgent")
	job.EnqueueAt(ctx, "tomorrow", start.Add(24*time.Hour))
	drain(t, q)
	if !slices.Equal(order, []string{"urgent", "now"}) {
		t.Fatalf("ran %q; want the urgent one first, nothing scheduled", order)
	}
	clock.Advance(time.Hour)
	drain(t, q)
	clock.Advance(24 * time.Hour)
	drain(t, q)
	if !slices.Equal(order, []string{"urgent", "now", "in an hour", "tomorrow"}) {
		t.Errorf("ran %q", order)
	}
}

func TestRetries(t *testing.T) {
	ctx := context.Background()
	q, _, clock := queue(t, Options{})
	var tries atomic.Int32
	var exhausted []string
	flaky := Define(q, "flaky", func(ctx context.Context, a backupArgs) error {
		if tries.Add(1) < 3 {
			return errors.New("the network blinked")
		}
		return nil
	}, Opts[backupArgs]{Retry: Retry{Attempts: 3, Wait: 3 * time.Second}})
	hopeless := Define(q, "hopeless", func(ctx context.Context, s string) error { return errors.New("never") },
		Opts[string]{Retry: Retry{Attempts: 2, Wait: time.Second}, OnExhausted: func(ctx context.Context, s string, err error) {
			exhausted = append(exhausted, s+": "+err.Error())
		}})
	flaky.Enqueue(ctx, backupArgs{})
	hopeless.Enqueue(ctx, "it")
	drain(t, q)
	if tries.Load() != 1 {
		t.Fatalf("tried %d times at once; the retry waits", tries.Load())
	}
	clock.Advance(3 * time.Second)
	drain(t, q)
	clock.Advance(3 * time.Second)
	drain(t, q)
	if tries.Load() != 3 {
		t.Errorf("flaky tried %d times, want 3", tries.Load())
	}
	if len(exhausted) != 1 || exhausted[0] != "it: never" {
		t.Errorf("exhausted %q", exhausted)
	}
	failed, _ := q.Failed(ctx)
	if len(failed) != 1 || failed[0].Name != "hopeless" || failed[0].Error != "never" || failed[0].Attempts != 2 {
		t.Errorf("failed %+v", failed)
	}
	// Retried by hand, it runs again.
	if err := q.Retry(ctx, failed[0].ID); err != nil {
		t.Fatal(err)
	}
	if p, _ := q.Pending(ctx); len(p) != 1 || p[0].Name != "hopeless" {
		t.Errorf("after Retry: %+v", p)
	}
}

// No retries unless asked (Active Job's default), and Discard gives up at
// once whatever the retries.
func TestNoRetriesAndDiscard(t *testing.T) {
	ctx := context.Background()
	q, _, clock := queue(t, Options{})
	var once, discarded atomic.Int32
	Define(q, "once", func(ctx context.Context, s string) error { once.Add(1); return errors.New("no") }, Opts[string]{}).Enqueue(ctx, "x")
	Define(q, "discarded", func(ctx context.Context, s string) error {
		discarded.Add(1)
		return Discard(errors.New("the project is gone"))
	},
		Opts[string]{Retry: Retry{Attempts: 5, Wait: time.Second}}).Enqueue(ctx, "x")
	for range 3 {
		drain(t, q)
		clock.Advance(time.Minute)
	}
	if once.Load() != 1 || discarded.Load() != 1 {
		t.Errorf("once %d, discarded %d; want 1 each", once.Load(), discarded.Load())
	}
	if failed, _ := q.Failed(ctx); len(failed) != 2 {
		t.Errorf("failed %+v", failed)
	}
}

func TestPolynomial(t *testing.T) {
	for attempt, want := range map[int]time.Duration{1: 3 * time.Second, 2: 18 * time.Second, 3: 83 * time.Second} {
		if got := Polynomial(attempt); got != want {
			t.Errorf("attempt %d: %v, want %v", attempt, got, want)
		}
	}
}

func TestTimeout(t *testing.T) {
	ctx := context.Background()
	q, _, _ := queue(t, Options{})
	var err error
	Define(q, "slow", func(ctx context.Context, s string) error {
		select {
		case <-ctx.Done():
			err = ctx.Err()
			return err
		case <-time.After(5 * time.Second):
			return nil
		}
	}, Opts[string]{Timeout: 50 * time.Millisecond}).Enqueue(ctx, "x")
	drain(t, q)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("the job's context: %v", err)
	}
}

// A panic is a failure like any other: logged, and the queue goes on.
func TestPanic(t *testing.T) {
	ctx := context.Background()
	q, _, _ := queue(t, Options{})
	var after atomic.Int32
	Define(q, "boom", func(ctx context.Context, s string) error { panic("boom") }, Opts[string]{}).Enqueue(ctx, "x")
	Define(q, "after", func(ctx context.Context, s string) error { after.Add(1); return nil }, Opts[string]{}).Enqueue(ctx, "x")
	drain(t, q)
	failed, _ := q.Failed(ctx)
	if len(failed) != 1 || failed[0].Name != "boom" || after.Load() != 1 {
		t.Errorf("failed %+v, after %d", failed, after.Load())
	}
}

// At most N at a time per key: a job over the limit waits its turn, or is
// discarded when the limit says so; another key runs.
func TestLimits(t *testing.T) {
	ctx := context.Background()
	q, _, _ := queue(t, Options{})
	release := make(chan struct{})
	var running, most atomic.Int32
	deploy := Define(q, "deploy", func(ctx context.Context, a backupArgs) error {
		n := running.Add(1)
		for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
		}
		<-release
		running.Add(-1)
		return nil
	}, Opts[backupArgs]{Limit: &Limit[backupArgs]{To: 1, Key: func(a backupArgs) string { return a.Project }}})
	deploy.Enqueue(ctx, backupArgs{Project: "shop"})
	deploy.Enqueue(ctx, backupArgs{Project: "shop"})
	deploy.Enqueue(ctx, backupArgs{Project: "blog"})

	claimed := func() []string {
		t.Helper()
		var keys []string
		for {
			e, ok, err := q.claim(ctx, "default", "test")
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				return keys
			}
			keys = append(keys, fmt.Sprint(e.ID))
		}
	}
	if got := claimed(); len(got) != 2 {
		t.Fatalf("claimed %v at once; want one per project", got)
	}
	close(release)

	// A finished job frees its slot for the next.
	q3, _, _ := queue(t, Options{})
	var ran3 atomic.Int32
	one := Define(q3, "deploy", func(ctx context.Context, s string) error { ran3.Add(1); return nil },
		Opts[string]{Limit: &Limit[string]{To: 1, Key: func(s string) string { return s }}})
	one.Enqueue(ctx, "shop")
	one.Enqueue(ctx, "shop")
	drain(t, q3)
	if ran3.Load() != 2 {
		t.Errorf("%d of two ran one after the other", ran3.Load())
	}

	// Discarding instead of waiting.
	q2, _, _ := queue(t, Options{})
	var ran atomic.Int32
	once := Define(q2, "sync", func(ctx context.Context, s string) error { ran.Add(1); return nil },
		Opts[string]{Limit: &Limit[string]{To: 1, Key: func(s string) string { return s }, Discard: true}})
	once.Enqueue(ctx, "a")
	once.Enqueue(ctx, "a")
	if _, ok, _ := q2.claim(ctx, "default", "test"); !ok {
		t.Fatal("nothing claimed")
	}
	if _, ok, _ := q2.claim(ctx, "default", "test"); ok {
		t.Error("a second over the limit was claimed")
	}
	if p, _ := q2.Pending(ctx); len(p) != 1 {
		t.Errorf("the duplicate wasn't discarded: %+v", p)
	}
}

// A limit held by a process that crashed expires (Solid Queue's 3 minutes).
func TestLimitExpires(t *testing.T) {
	ctx := context.Background()
	q, _, clock := queue(t, Options{})
	job := Define(q, "deploy", func(ctx context.Context, s string) error { return nil },
		Opts[string]{Limit: &Limit[string]{To: 1, Key: func(s string) string { return s }}})
	job.Enqueue(ctx, "shop")
	job.Enqueue(ctx, "shop")
	if _, ok, _ := q.claim(ctx, "default", "gone"); !ok {
		t.Fatal("nothing claimed")
	}
	clock.Advance(3*time.Minute + time.Second)
	if err := q.maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := q.claim(ctx, "default", "test"); !ok {
		t.Error("the crashed holder's limit didn't expire")
	}
}

func TestRecurring(t *testing.T) {
	ctx := context.Background()
	q, d, clock := queue(t, Options{})
	var every, nightly atomic.Int32
	Define(q, "sweep", func(ctx context.Context, s string) error { every.Add(1); return nil }, Opts[string]{}).Every(10*time.Minute, "x")
	chicago, _ := time.LoadLocation("America/Chicago")
	if err := Define(q, "backup", func(ctx context.Context, s string) error { nightly.Add(1); return nil }, Opts[string]{}).Cron("0 3 * * *", chicago, "x"); err != nil {
		t.Fatal(err)
	}
	// A second process running the same schedules enqueues nothing twice.
	q2, err := New(ctx, d, Options{Now: clock.Now, Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	Define(q2, "sweep", func(ctx context.Context, s string) error { every.Add(1); return nil }, Opts[string]{}).Every(10*time.Minute, "x")

	for range 6 { // an hour, ten minutes at a time
		clock.Advance(10 * time.Minute)
		q2.Drain(ctx)
		drain(t, q)
	}
	if every.Load() != 6 {
		t.Errorf("every ten minutes for an hour: %d runs, want 6", every.Load())
	}
	// 3:00 in Chicago is 8:00 UTC (daylight time): after 20 hours, once.
	clock.Advance(20 * time.Hour)
	drain(t, q)
	if nightly.Load() != 1 {
		t.Errorf("the nightly backup ran %d times", nightly.Load())
	}
	// The twenty hours' missed sweeps aren't made up: one, then on schedule.
	drain(t, q)
	if every.Load() != 7 {
		t.Errorf("after twenty hours idle: %d sweeps, want 7 (one, not a backlog)", every.Load())
	}
	if err := Define(q, "bad", func(ctx context.Context, s string) error { return nil }, Opts[string]{}).Cron("not cron", time.UTC, "x"); err == nil {
		t.Error("a bad cron expression")
	}
}

// Finished jobs are kept a day, then cleared; failed ones stay.
func TestClearing(t *testing.T) {
	ctx := context.Background()
	q, d, clock := queue(t, Options{})
	Define(q, "ok", func(ctx context.Context, s string) error { return nil }, Opts[string]{}).Enqueue(ctx, "x")
	Define(q, "bad", func(ctx context.Context, s string) error { return errors.New("no") }, Opts[string]{}).Enqueue(ctx, "x")
	drain(t, q)
	count := func() int {
		var n int
		d.Read.QueryRow(`SELECT COUNT(*) FROM gantry_jobs`).Scan(&n)
		return n
	}
	clock.Advance(23 * time.Hour)
	q.maintain(ctx)
	if count() != 2 {
		t.Fatalf("%d jobs before a day", count())
	}
	clock.Advance(2 * time.Hour)
	q.maintain(ctx)
	if failed, _ := q.Failed(ctx); count() != 1 || len(failed) != 1 {
		t.Errorf("%d jobs after a day, failed %+v", count(), failed)
	}
}

// A process that stops heartbeating (it crashed) has its claimed jobs put
// back, for another to run.
func TestCrashedProcess(t *testing.T) {
	ctx := context.Background()
	q, _, clock := queue(t, Options{})
	var r atomic.Int32
	work := Define(q, "work", func(ctx context.Context, s string) error { r.Add(1); return nil }, Opts[string]{})
	work.Enqueue(ctx, "x")
	work.Enqueue(ctx, "y")
	for _, p := range []string{"crashed", "alive"} {
		if err := q.beat(ctx, p); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := q.claim(ctx, "default", p); !ok {
			t.Fatal("nothing claimed")
		}
	}
	clock.Advance(4 * time.Minute)
	q.maintain(ctx)
	drain(t, q)
	if r.Load() != 0 {
		t.Fatal("released while its process might be alive")
	}
	clock.Advance(2 * time.Minute) // silent for six; the other, still beating
	q.beat(ctx, "alive")
	q.maintain(ctx)
	drain(t, q)
	if r.Load() != 1 {
		t.Errorf("%d jobs went back; want the crashed process's one, not the live one's", r.Load())
	}
}

// Run: workers per queue, at most the queue's concurrency at once, woken by
// an enqueue; on shutdown, running jobs get their grace, then their
// contexts end and they go back to the queue.
func TestRun(t *testing.T) {
	ctx := context.Background()
	q, _, _ := queue(t, Options{Queues: map[string]int{"default": 2}, Now: time.Now, Poll: 20 * time.Millisecond, Grace: 100 * time.Millisecond})
	var running, most, done atomic.Int32
	work := Define(q, "work", func(ctx context.Context, s string) error {
		n := running.Add(1)
		defer running.Add(-1)
		for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
		}
		if s == "stuck" {
			<-ctx.Done()
			return ctx.Err()
		}
		time.Sleep(20 * time.Millisecond)
		done.Add(1)
		return nil
	}, Opts[string]{})
	runCtx, stop := context.WithCancel(ctx)
	stopped := make(chan error, 1)
	go func() { stopped <- q.Run(runCtx) }()
	for range 6 {
		work.Enqueue(ctx, "x")
	}
	deadline := time.Now().Add(5 * time.Second)
	for done.Load() < 6 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if done.Load() != 6 || most.Load() != 2 {
		t.Fatalf("done %d, at most %d at once; want 6 and 2", done.Load(), most.Load())
	}
	work.Enqueue(ctx, "stuck")
	for running.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run didn't return after its grace")
	}
	if p, _ := q.Pending(ctx); len(p) != 1 || p[0].State != "ready" || p[0].Attempts != 0 {
		t.Errorf("the stuck job wasn't put back: %+v", p)
	}
}
