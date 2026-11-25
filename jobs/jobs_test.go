package jobs

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/internal/testenv"
	"github.com/scttymn/gantry/testkit"
)

type backupArgs struct {
	Project string `json:"project"`
	Keep    int    `json:"keep"`
}

var start = time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

func onEngines(t *testing.T, test func(t *testing.T, engine string)) {
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) { test(t, engine) })
	}
}

func queue(t *testing.T, engine string, o Options) (*Queue, *db.DB, *testkit.FakeClock) {
	t.Helper()
	var d *db.DB
	if engine == "postgres" {
		t.Setenv("TEST_DATABASE_URL", testenv.Postgres(t))
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

func TestEnqueueAndRun(t *testing.T) { onEngines(t, testEnqueueAndRun) }

func testEnqueueAndRun(t *testing.T, engine string) {
	ctx := context.Background()
	q, _, _ := queue(t, engine, Options{})
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

func TestEnqueueTx(t *testing.T) { onEngines(t, testEnqueueTx) }

func testEnqueueTx(t *testing.T, engine string) {
	ctx := context.Background()
	q, d, _ := queue(t, engine, Options{})
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

func TestScheduledAndPriority(t *testing.T) { onEngines(t, testScheduledAndPriority) }

func testScheduledAndPriority(t *testing.T, engine string) {
	ctx := context.Background()
	q, _, clock := queue(t, engine, Options{})
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

func TestRetries(t *testing.T) { onEngines(t, testRetries) }

func testRetries(t *testing.T, engine string) {
	ctx := context.Background()
	q, _, clock := queue(t, engine, Options{})
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
	if err := q.Retry(ctx, failed[0].ID); err != nil {
		t.Fatal(err)
	}
	if p, _ := q.Pending(ctx); len(p) != 1 || p[0].Name != "hopeless" {
		t.Errorf("after Retry: %+v", p)
	}
}

func TestNoRetriesAndDiscard(t *testing.T) { onEngines(t, testNoRetriesAndDiscard) }

func testNoRetriesAndDiscard(t *testing.T, engine string) {
	ctx := context.Background()
	q, _, clock := queue(t, engine, Options{})
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

func TestTimeout(t *testing.T) { onEngines(t, testTimeout) }

func testTimeout(t *testing.T, engine string) {
	ctx := context.Background()
	q, _, _ := queue(t, engine, Options{})
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

func TestPanic(t *testing.T) { onEngines(t, testPanic) }

func testPanic(t *testing.T, engine string) {
	ctx := context.Background()
	q, _, _ := queue(t, engine, Options{})
	var after atomic.Int32
	Define(q, "boom", func(ctx context.Context, s string) error { panic("boom") }, Opts[string]{}).Enqueue(ctx, "x")
	Define(q, "after", func(ctx context.Context, s string) error { after.Add(1); return nil }, Opts[string]{}).Enqueue(ctx, "x")
	drain(t, q)
	failed, _ := q.Failed(ctx)
	if len(failed) != 1 || failed[0].Name != "boom" || after.Load() != 1 {
		t.Errorf("failed %+v, after %d", failed, after.Load())
	}
}

func TestLimits(t *testing.T) { onEngines(t, testLimits) }

func testLimits(t *testing.T, engine string) {
	ctx := context.Background()
	q, _, _ := queue(t, engine, Options{})
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

	q3, _, _ := queue(t, engine, Options{})
	var ran3 atomic.Int32
	one := Define(q3, "deploy", func(ctx context.Context, s string) error { ran3.Add(1); return nil },
		Opts[string]{Limit: &Limit[string]{To: 1, Key: func(s string) string { return s }}})
	one.Enqueue(ctx, "shop")
	one.Enqueue(ctx, "shop")
	drain(t, q3)
	if ran3.Load() != 2 {
		t.Errorf("%d of two ran one after the other", ran3.Load())
	}

	q2, _, _ := queue(t, engine, Options{})
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

func TestLimitExpires(t *testing.T) { onEngines(t, testLimitExpires) }

func testLimitExpires(t *testing.T, engine string) {
	ctx := context.Background()
	q, _, clock := queue(t, engine, Options{})
	job := Define(q, "deploy", func(ctx context.Context, s string) error { return nil },
		Opts[string]{Limit: &Limit[string]{To: 1, Key: func(s string) string { return s }}})
	job.Enqueue(ctx, "shop")
	job.Enqueue(ctx, "shop")
	if _, ok, _ := q.claim(ctx, "default", "gone"); !ok {
		t.Fatal("nothing claimed")
	}
	clock.Advance(3*time.Minute + time.Second)
	if err := q.maintain(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := q.claim(ctx, "default", "test"); !ok {
		t.Error("the crashed holder's limit didn't expire")
	}
}

func TestLimitOutlived(t *testing.T) { onEngines(t, testLimitOutlived) }

func testLimitOutlived(t *testing.T, engine string) {
	ctx := context.Background()
	q, _, clock := queue(t, engine, Options{})
	deploy := Define(q, "deploy", func(ctx context.Context, s string) error { return nil },
		Opts[string]{Limit: &Limit[string]{To: 1, Key: func(s string) string { return s }}})
	for range 4 {
		deploy.Enqueue(ctx, "shop")
	}
	long, ok, _ := q.claim(ctx, "default", "test")
	if !ok {
		t.Fatal("nothing claimed")
	}
	clock.Advance(3*time.Minute + time.Second)
	if err := q.maintain(ctx, nil); err != nil {
		t.Fatal(err)
	}
	next, ok, _ := q.claim(ctx, "default", "test")
	if !ok {
		t.Fatal("the outlived slot didn't expire")
	}
	q.execute(ctx, long)
	if _, ok, _ := q.claim(ctx, "default", "test"); ok {
		t.Fatal("claimed beside the running one: the outlived one freed its slot")
	}
	q.execute(ctx, next)
	if _, ok, _ := q.claim(ctx, "default", "test"); !ok {
		t.Fatal("nothing claimed once both finished")
	}
	if _, ok, _ := q.claim(ctx, "default", "test"); ok {
		t.Error("two claimed at once with a limit of one")
	}
}

func TestLimitConcurrentClaims(t *testing.T) { onEngines(t, testLimitConcurrentClaims) }

func testLimitConcurrentClaims(t *testing.T, engine string) {
	ctx := context.Background()
	q, _, _ := queue(t, engine, Options{})
	deploy := Define(q, "deploy", func(ctx context.Context, s string) error { return nil },
		Opts[string]{Limit: &Limit[string]{To: 1, Key: func(s string) string { return s }}})
	for range 100 {
		deploy.Enqueue(ctx, "shop")
	}
	var wg sync.WaitGroup
	var n atomic.Int32
	for i := range 5 {
		wg.Go(func() {
			_, ok, err := q.claim(ctx, "default", fmt.Sprint("worker ", i))
			if err != nil {
				t.Error(err)
			}
			if ok {
				n.Add(1)
			}
		})
	}
	wg.Wait()
	if n.Load() != 1 {
		t.Errorf("%d claimed at once with a limit of one", n.Load())
	}
}

func TestLimitDiscardsMany(t *testing.T) { onEngines(t, testLimitDiscardsMany) }

func testLimitDiscardsMany(t *testing.T, engine string) {
	ctx := context.Background()
	q, _, _ := queue(t, engine, Options{})
	var ran []string
	mirror := Define(q, "mirror", func(ctx context.Context, s string) error { ran = append(ran, s); return nil },
		Opts[string]{Limit: &Limit[string]{To: 1, Key: func(s string) string { return s }, Discard: true}})
	for range 22 {
		mirror.Enqueue(ctx, "a")
	}
	mirror.Enqueue(ctx, "b")
	if _, ok, _ := q.claim(ctx, "default", "busy"); !ok {
		t.Fatal("nothing claimed")
	}
	drain(t, q)
	if !slices.Equal(ran, []string{"b"}) {
		t.Errorf("ran %q; want b, behind 21 dropped", ran)
	}
	if failed, _ := q.Failed(ctx); len(failed) != 21 {
		t.Errorf("%d dropped; want 21", len(failed))
	}
}

func TestStaleWorkerLimit(t *testing.T) { onEngines(t, testStaleWorkerLimit) }

func testStaleWorkerLimit(t *testing.T, engine string) {
	ctx := context.Background()
	q, _, clock := queue(t, engine, Options{})
	deploy := Define(q, "deploy", func(ctx context.Context, s string) error { return nil },
		Opts[string]{Limit: &Limit[string]{To: 1, Key: func(s string) string { return s }}})
	deploy.Enqueue(ctx, "shop")
	deploy.Enqueue(ctx, "shop")
	stale, ok, _ := q.claim(ctx, "default", "stalled")
	if !ok {
		t.Fatal("nothing claimed")
	}
	clock.Advance(6 * time.Minute)
	q.maintain(ctx, nil)
	q.beat(ctx, "alive")
	if _, ok, _ := q.claim(ctx, "default", "alive"); !ok {
		t.Fatal("the stalled worker's job didn't go back")
	}
	q.execute(ctx, stale)
	if _, ok, _ := q.claim(ctx, "default", "alive"); ok {
		t.Error("claimed beside the new claim: the stalled worker freed its slot")
	}
}

func TestSettleFails(t *testing.T) { onEngines(t, testSettleFails) }

func testSettleFails(t *testing.T, engine string) {
	ctx := context.Background()
	q, d, _ := queue(t, engine, Options{})
	var exhausted atomic.Int32
	Define(q, "work", func(ctx context.Context, s string) error { return errors.New("no") },
		Opts[string]{OnExhausted: func(ctx context.Context, s string, err error) { exhausted.Add(1) }}).Enqueue(ctx, "x")
	failWrites(t, d, "UPDATE", "NEW.state = 'failed'")
	drain(t, q)
	if p, _ := q.Pending(ctx); len(p) != 1 || p[0].State != "claimed" || exhausted.Load() != 0 {
		t.Errorf("pending %+v, exhausted %d; want it claimed, and OnExhausted not run", p, exhausted.Load())
	}
}

func failWrites(t *testing.T, d *db.DB, event, when string) (undo func()) {
	t.Helper()
	ctx := context.Background()
	stmts := []string{`CREATE TRIGGER fail BEFORE ` + event + ` ON gantry_jobs FOR EACH ROW WHEN (` + when + `) BEGIN SELECT RAISE(ABORT, 'disk full'); END`}
	drop := `DROP TRIGGER fail`
	if d.Engine == db.Postgres {
		stmts = []string{`CREATE FUNCTION fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'disk full'; END $$`,
			`CREATE TRIGGER fail BEFORE ` + event + ` ON gantry_jobs FOR EACH ROW WHEN (` + when + `) EXECUTE FUNCTION fail()`}
		drop = `DROP TRIGGER fail ON gantry_jobs`
	}
	for _, stmt := range stmts {
		if _, err := d.Write.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	return func() {
		if _, err := d.Write.ExecContext(ctx, drop); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLimitHeadOfLine(t *testing.T) { onEngines(t, testLimitHeadOfLine) }

func testLimitHeadOfLine(t *testing.T, engine string) {
	ctx := context.Background()
	q, _, _ := queue(t, engine, Options{})
	deploy := Define(q, "deploy", func(ctx context.Context, s string) error { return nil },
		Opts[string]{Limit: &Limit[string]{To: 1, Key: func(s string) string { return s }}})
	for range 25 {
		deploy.Enqueue(ctx, "shop")
	}
	deploy.Enqueue(ctx, "blog")
	if c, ok, _ := q.claim(ctx, "default", "test"); !ok || string(c.Args) != `"shop"` {
		t.Fatalf("claimed %q, %v; want the first shop deploy", c.Args, ok)
	}
	if c, ok, err := q.claim(ctx, "default", "test"); err != nil || !ok || string(c.Args) != `"blog"` {
		t.Errorf("claimed %q, %v, %v; want the blog deploy, behind 24 waiting for shop's limit", c.Args, ok, err)
	}
}

func TestStaleWorker(t *testing.T) { onEngines(t, testStaleWorker) }

func testStaleWorker(t *testing.T, engine string) {
	for _, again := range []string{"alive", "stalled"} {
		t.Run("claimed again by "+again, func(t *testing.T) {
			ctx := context.Background()
			q, _, clock := queue(t, engine, Options{})
			var calls, exhausted atomic.Int32
			work := Define(q, "work", func(ctx context.Context, s string) error {
				if calls.Add(1) == 1 {
					return errors.New("too late")
				}
				return nil
			}, Opts[string]{OnExhausted: func(ctx context.Context, s string, err error) { exhausted.Add(1) }})
			work.Enqueue(ctx, "x")
			stale, ok, _ := q.claim(ctx, "default", "stalled")
			if !ok {
				t.Fatal("nothing claimed")
			}
			clock.Advance(6 * time.Minute)
			q.maintain(ctx, nil)
			q.beat(ctx, again)
			fresh, ok, _ := q.claim(ctx, "default", again)
			if !ok {
				t.Fatal("the stalled worker's job didn't go back")
			}
			q.execute(ctx, stale)
			if p, _ := q.Pending(ctx); len(p) != 1 || p[0].State != "claimed" {
				t.Fatalf("the stalled worker settled a job claimed again: %+v", p)
			}
			if failed, _ := q.Failed(ctx); len(failed) != 0 || exhausted.Load() != 0 {
				t.Fatalf("failed %+v, exhausted %d; want neither", failed, exhausted.Load())
			}
			q.execute(ctx, fresh)
			if p, _ := q.Pending(ctx); len(p) != 0 {
				t.Errorf("the new claim's run didn't finish the job: %+v", p)
			}
		})
	}
}

var errReset = errors.New("connection reset")

type brokenRead struct{}

func (brokenRead) Connect(context.Context) (driver.Conn, error) { return brokenRead{}, nil }
func (brokenRead) Driver() driver.Driver                        { return nil }
func (brokenRead) Prepare(string) (driver.Stmt, error)          { return nil, errors.New("not prepared") }
func (brokenRead) Close() error                                 { return nil }
func (brokenRead) Begin() (driver.Tx, error)                    { return brokenRead{}, nil }
func (brokenRead) Commit() error                                { return nil }
func (brokenRead) Rollback() error                              { return nil }
func (brokenRead) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(1), nil
}
func (brokenRead) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if strings.HasPrefix(query, "SELECT id, name, args") {
		return &rows{cols: []string{"id", "name", "args", "attempts", "limit_key"},
			row: []driver.Value{int64(1), "work", `"x"`, int64(0), ""}, end: errReset}, nil
	}
	return &rows{end: io.EOF}, nil
}

type rows struct {
	cols []string
	row  []driver.Value
	end  error
}

func (r *rows) Columns() []string { return r.cols }
func (r *rows) Close() error      { return nil }
func (r *rows) Next(dest []driver.Value) error {
	if r.row == nil {
		return r.end
	}
	copy(dest, r.row)
	r.row = nil
	return nil
}

func TestClaimReadFails(t *testing.T) {
	broken := sql.OpenDB(brokenRead{})
	defer broken.Close()
	q := &Queue{d: &db.DB{Engine: db.SQLite, Read: broken, Write: broken},
		o: Options{Log: slog.New(slog.DiscardHandler), Now: time.Now}, defs: map[string]runner{}}
	Define(q, "work", func(ctx context.Context, s string) error { return nil }, Opts[string]{})
	if c, ok, err := q.claim(context.Background(), "default", "test"); !errors.Is(err, errReset) {
		t.Errorf("claimed %+v, %v, %v; want the read's error", c, ok, err)
	}
}

func TestRecurring(t *testing.T) { onEngines(t, testRecurring) }

func testRecurring(t *testing.T, engine string) {
	ctx := context.Background()
	q, d, clock := queue(t, engine, Options{})
	var every, nightly atomic.Int32
	Define(q, "sweep", func(ctx context.Context, s string) error { every.Add(1); return nil }, Opts[string]{}).Every(10*time.Minute, "x")
	chicago, _ := time.LoadLocation("America/Chicago")
	if err := Define(q, "backup", func(ctx context.Context, s string) error { nightly.Add(1); return nil }, Opts[string]{}).Cron("0 3 * * *", chicago, "x"); err != nil {
		t.Fatal(err)
	}
	q2, err := New(ctx, d, Options{Now: clock.Now, Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	Define(q2, "sweep", func(ctx context.Context, s string) error { every.Add(1); return nil }, Opts[string]{}).Every(10*time.Minute, "x")

	for range 6 {
		clock.Advance(10 * time.Minute)
		q2.Drain(ctx)
		drain(t, q)
	}
	if every.Load() != 6 {
		t.Errorf("every ten minutes for an hour: %d runs, want 6", every.Load())
	}
	clock.Advance(20 * time.Hour)
	drain(t, q)
	if nightly.Load() != 1 {
		t.Errorf("the nightly backup ran %d times", nightly.Load())
	}
	drain(t, q)
	if every.Load() != 7 {
		t.Errorf("after twenty hours idle: %d sweeps, want 7 (one, not a backlog)", every.Load())
	}
	if err := Define(q, "bad", func(ctx context.Context, s string) error { return nil }, Opts[string]{}).Cron("not cron", time.UTC, "x"); err == nil {
		t.Error("a bad cron expression")
	}
}

func TestRecurringInsertFails(t *testing.T) { onEngines(t, testRecurringInsertFails) }

func testRecurringInsertFails(t *testing.T, engine string) {
	ctx := context.Background()
	q, d, clock := queue(t, engine, Options{})
	var runs atomic.Int32
	Define(q, "sweep", func(ctx context.Context, s string) error { runs.Add(1); return nil }, Opts[string]{}).Every(10*time.Minute, "x")
	undo := failWrites(t, d, "INSERT", "true")
	clock.Advance(10 * time.Minute)
	if err := q.Drain(ctx); err == nil {
		t.Fatal("the job wasn't saved, and the tick didn't say so")
	}
	undo()
	drain(t, q)
	if runs.Load() != 1 {
		t.Errorf("%d runs after the failed tick; want 1", runs.Load())
	}
}

func TestRecurringStalledPastKeep(t *testing.T) { onEngines(t, testRecurringStalledPastKeep) }

func testRecurringStalledPastKeep(t *testing.T, engine string) {
	ctx := context.Background()
	q, d, clock := queue(t, engine, Options{})
	var runs atomic.Int32
	sweep := func(q *Queue) {
		Define(q, "sweep", func(ctx context.Context, s string) error { runs.Add(1); return nil }, Opts[string]{}).Every(time.Hour, "x")
	}
	sweep(q)
	stalled, err := New(ctx, d, Options{Now: clock.Now, Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	sweep(stalled)
	clock.Advance(time.Hour)
	drain(t, q)
	clock.Advance(25 * time.Hour)
	q.maintain(ctx, nil)
	if err := stalled.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if runs.Load() != 1 {
		t.Errorf("the 13:00 sweep ran %d times; want 1", runs.Load())
	}
}

func TestTicksCleared(t *testing.T) { onEngines(t, testTicksCleared) }

func testTicksCleared(t *testing.T, engine string) {
	ctx := context.Background()
	q, d, clock := queue(t, engine, Options{})
	Define(q, "sweep", func(ctx context.Context, s string) error { return nil }, Opts[string]{}).Every(time.Hour, "x")
	clock.Advance(time.Hour)
	drain(t, q)
	ticks := func() int {
		var n int
		d.Read.QueryRow(`SELECT COUNT(*) FROM gantry_job_ticks`).Scan(&n)
		return n
	}
	clock.Advance(25*time.Hour - time.Second)
	q.maintain(ctx, nil)
	if ticks() != 1 {
		t.Fatalf("%d ticks a second before a day and an hour; want 1: kept an hour past Keep, so a tick that read its clock just inside Keep finds it", ticks())
	}
	clock.Advance(2 * time.Second)
	q.maintain(ctx, nil)
	if ticks() != 0 {
		t.Errorf("%d ticks a second after a day and an hour; want 0", ticks())
	}
}

func TestClearing(t *testing.T) { onEngines(t, testClearing) }

func testClearing(t *testing.T, engine string) {
	ctx := context.Background()
	q, d, clock := queue(t, engine, Options{})
	Define(q, "ok", func(ctx context.Context, s string) error { return nil }, Opts[string]{}).Enqueue(ctx, "x")
	Define(q, "bad", func(ctx context.Context, s string) error { return errors.New("no") }, Opts[string]{}).Enqueue(ctx, "x")
	drain(t, q)
	count := func() int {
		var n int
		d.Read.QueryRow(`SELECT COUNT(*) FROM gantry_jobs`).Scan(&n)
		return n
	}
	clock.Advance(23 * time.Hour)
	q.maintain(ctx, nil)
	if count() != 2 {
		t.Fatalf("%d jobs before a day", count())
	}
	clock.Advance(2 * time.Hour)
	q.maintain(ctx, nil)
	if failed, _ := q.Failed(ctx); count() != 1 || len(failed) != 1 {
		t.Errorf("%d jobs after a day, failed %+v", count(), failed)
	}
}

func TestCrashedProcess(t *testing.T) { onEngines(t, testCrashedProcess) }

func testCrashedProcess(t *testing.T, engine string) {
	ctx := context.Background()
	q, _, clock := queue(t, engine, Options{})
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
	q.maintain(ctx, nil)
	drain(t, q)
	if r.Load() != 0 {
		t.Fatal("released while its process might be alive")
	}
	clock.Advance(2 * time.Minute)
	q.beat(ctx, "alive")
	q.maintain(ctx, nil)
	drain(t, q)
	if r.Load() != 1 {
		t.Errorf("%d jobs went back; want the crashed process's one, not the live one's", r.Load())
	}
}

func TestRun(t *testing.T) { onEngines(t, testRun) }

func testRun(t *testing.T, engine string) {
	ctx := context.Background()
	q, _, _ := queue(t, engine, Options{Queues: map[string]int{"default": 2}, Now: time.Now, Poll: 20 * time.Millisecond, Grace: 100 * time.Millisecond})
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
