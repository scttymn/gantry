package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/scttymn/gantry/db"
)

func TestCrashLoopFailsAfterMaxCrashes(t *testing.T) { onEngines(t, testCrashLoopFailsAfterMaxCrashes) }

func testCrashLoopFailsAfterMaxCrashes(t *testing.T, engine string) {
	ctx := context.Background()
	q, _, clock := queue(t, engine, Options{})
	exhausted := make(chan error, 2)
	Define(q, "poison", func(ctx context.Context, s string) error { return nil },
		Opts[string]{Retry: Retry{Attempts: 2}, OnExhausted: func(ctx context.Context, s string, err error) { exhausted <- err }}).Enqueue(ctx, "x")
	crash := func(process string) bool {
		t.Helper()
		_, ok, err := q.claim(ctx, "default", process)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			clock.Advance(6 * time.Minute)
			if err := q.maintain(ctx, nil); err != nil {
				t.Fatal(err)
			}
		}
		return ok
	}
	claims := 0
	for i := range 6 {
		if !crash(fmt.Sprint("crashed-", i)) {
			break
		}
		claims++
	}
	failed, _ := q.Failed(ctx)
	if claims != 3 || len(failed) != 1 || failed[0].Error != "its process died 3 times" || failed[0].Attempts != 0 {
		t.Fatalf("claimed %d times, each by a process that died; failed %+v; want 3 claims, then failed with \"its process died 3 times\" and no attempt used", claims, failed)
	}
	select {
	case err := <-exhausted:
		if !strings.Contains(err.Error(), "died 3 times") {
			t.Errorf("OnExhausted's error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OnExhausted didn't run for a job whose process died 3 times")
	}
	if err := q.Retry(ctx, failed[0].ID); err != nil {
		t.Fatal(err)
	}
	crash("crashed-after-retry")
	if p, _ := q.Pending(ctx); len(p) != 1 || p[0].State != "ready" {
		t.Errorf("one crash after Retry: %+v; want it back in the queue, its crashes counted afresh", p)
	}
}

func TestShutdownKeepsHeartbeatWhileJobRuns(t *testing.T) {
	onEngines(t, testShutdownKeepsHeartbeatWhileJobRuns)
}

func testShutdownKeepsHeartbeatWhileJobRuns(t *testing.T, engine string) {
	ctx := context.Background()
	o := Options{Now: time.Now, Poll: 10 * time.Millisecond, Grace: 20 * time.Millisecond,
		Heartbeat: 50 * time.Millisecond, Dead: 300 * time.Millisecond}
	a, d, _ := queue(t, engine, o)
	release := make(chan struct{})
	var inside, most atomic.Int32
	perform := func(ctx context.Context, s string) error {
		n := inside.Add(1)
		defer inside.Add(-1)
		for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
		}
		<-release
		return nil
	}
	work := Define(a, "work", perform, Opts[string]{})
	o.Log = slog.New(slog.DiscardHandler)
	b, err := New(ctx, d, o)
	if err != nil {
		t.Fatal(err)
	}
	Define(b, "work", perform, Opts[string]{})

	runCtx, stop := context.WithCancel(ctx)
	stopped := make(chan error, 1)
	go func() { stopped <- a.Run(runCtx) }()
	work.Enqueue(ctx, "x")
	for deadline := time.Now().Add(5 * time.Second); inside.Load() == 0 && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	if inside.Load() == 0 {
		t.Fatal("the job never started")
	}
	stop()
	time.Sleep(o.Dead + 3*o.Heartbeat)
	if err := b.beat(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if err := b.maintain(ctx, nil); err != nil {
		t.Fatal(err)
	}
	c, ok, err := b.claim(ctx, "default", "b")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		go b.execute(ctx, c)
		for deadline := time.Now().Add(2 * time.Second); most.Load() < 2 && time.Now().Before(deadline); {
			time.Sleep(5 * time.Millisecond)
		}
	}
	close(release)
	<-stopped
	if ok || most.Load() > 1 {
		t.Errorf("a stopping process ran a job that ignores its context for longer than Dead; another process claimed it (%v) and ran it beside it, %d at once; want 1", ok, most.Load())
	}
}

func TestFailedSettleIsRetriedOnHeartbeat(t *testing.T) {
	onEngines(t, testFailedSettleIsRetriedOnHeartbeat)
}

func testFailedSettleIsRetriedOnHeartbeat(t *testing.T, engine string) {
	for _, c := range []struct {
		state string
		err   error
	}{{"finished", nil}, {"failed", errors.New("no")}} {
		t.Run(c.state, func(t *testing.T) {
			ctx := context.Background()
			q, d, _ := queue(t, engine, Options{Now: time.Now, Poll: 10 * time.Millisecond, Grace: 50 * time.Millisecond,
				Heartbeat: 20 * time.Millisecond, Dead: time.Minute})
			var runs, exhausted atomic.Int32
			work := Define(q, "work", func(ctx context.Context, s string) error { runs.Add(1); return c.err },
				Opts[string]{OnExhausted: func(ctx context.Context, s string, err error) { exhausted.Add(1) }})
			undo := failWrites(t, d, "UPDATE", "NEW.state = '"+c.state+"'")
			runCtx, stop := context.WithCancel(ctx)
			stopped := make(chan error, 1)
			go func() { stopped <- q.Run(runCtx) }()
			work.Enqueue(ctx, "x")
			for deadline := time.Now().Add(5 * time.Second); runs.Load() == 0 && time.Now().Before(deadline); {
				time.Sleep(5 * time.Millisecond)
			}
			time.Sleep(100 * time.Millisecond)
			undo()
			states := func() string {
				var s string
				d.Read.QueryRow(`SELECT state FROM gantry_jobs`).Scan(&s)
				return s
			}
			for deadline := time.Now().Add(5 * time.Second); states() != c.state && time.Now().Before(deadline); {
				time.Sleep(10 * time.Millisecond)
			}
			stop()
			<-stopped
			want := map[string]int32{"finished": 0, "failed": 1}[c.state]
			if states() != c.state || runs.Load() != 1 || exhausted.Load() != want {
				t.Errorf("its settle to %s failed once, and the database recovered: the job is %q, run %d times, OnExhausted %d; want %s, run once, OnExhausted %d",
					c.state, states(), runs.Load(), exhausted.Load(), c.state, want)
			}
		})
	}
}

func TestOwnOrphanedClaimIsReleased(t *testing.T) { onEngines(t, testOwnOrphanedClaimIsReleased) }

func testOwnOrphanedClaimIsReleased(t *testing.T, engine string) {
	ctx := context.Background()
	q, d, _ := queue(t, engine, Options{Now: time.Now, Poll: 10 * time.Millisecond, Grace: 50 * time.Millisecond,
		Heartbeat: 20 * time.Millisecond, Dead: time.Minute})
	var runs atomic.Int32
	work := Define(q, "work", func(ctx context.Context, s string) error { runs.Add(1); return nil }, Opts[string]{})
	runCtx, stop := context.WithCancel(ctx)
	stopped := make(chan error, 1)
	go func() { stopped <- q.Run(runCtx) }()
	defer func() { stop(); <-stopped }()
	orphan(t, d, work, process(t, d), time.Now().UTC().Add(-time.Second))
	for deadline := time.Now().Add(5 * time.Second); runs.Load() == 0 && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	if runs.Load() != 1 {
		t.Errorf("a job claimed by this live process, which no worker of it runs (a claim whose commit errored but applied), ran %d times; want it released and run once", runs.Load())
	}
}

func process(t *testing.T, d *db.DB) string {
	t.Helper()
	var id string
	for deadline := time.Now().Add(5 * time.Second); id == "" && time.Now().Before(deadline); {
		d.Read.QueryRow(`SELECT id FROM gantry_job_processes`).Scan(&id)
		time.Sleep(5 * time.Millisecond)
	}
	if id == "" {
		t.Fatal("Run's process never beat")
	}
	return id
}

func orphan(t *testing.T, d *db.DB, work *Job[string], by string, at time.Time) int64 {
	t.Helper()
	ctx := context.Background()
	id, err := work.EnqueueIn(ctx, "x", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.ExecContext(ctx, `UPDATE gantry_jobs SET state = 'claimed', claimed_by = $1, claimed_at = $2, attempts = 1, run_at = $3 WHERE id = $4`,
		by, at, time.Now().UTC(), id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestStopReleasesOwnOrphanedClaim(t *testing.T) { onEngines(t, testStopReleasesOwnOrphanedClaim) }

func testStopReleasesOwnOrphanedClaim(t *testing.T, engine string) {
	for name, ahead := range map[string]time.Duration{"just claimed": 0, "claimed before the clock stepped back": time.Hour} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			q, d, _ := queue(t, engine, Options{Now: time.Now, Poll: 10 * time.Millisecond, Grace: 50 * time.Millisecond,
				Heartbeat: time.Minute, Dead: 2 * time.Minute})
			work := Define(q, "work", func(ctx context.Context, s string) error { return nil }, Opts[string]{})
			runCtx, stop := context.WithCancel(ctx)
			stopped := make(chan error, 1)
			go func() { stopped <- q.Run(runCtx) }()
			orphan(t, d, work, process(t, d), time.Now().UTC().Add(ahead))
			stop()
			<-stopped
			if p, _ := q.Pending(ctx); len(p) != 1 || p[0].State != "ready" || p[0].Attempts != 0 {
				t.Errorf("Run stopped with a claim of its own that no worker ran, claimed_at %v from now: %+v; want it put back, its attempt undone, not left for another process to count as a crash", ahead, p)
			}
		})
	}
}

func crashTwice(t *testing.T, q *Queue, clock interface{ Advance(time.Duration) }) {
	t.Helper()
	ctx := context.Background()
	for i := range 2 {
		if _, ok, err := q.claim(ctx, "default", fmt.Sprint("crashed-", i)); !ok || err != nil {
			t.Fatalf("nothing claimed: %v", err)
		}
		clock.Advance(6 * time.Minute)
		if err := q.maintain(ctx, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStaleSettleAfterCrashFailure(t *testing.T) { onEngines(t, testStaleSettleAfterCrashFailure) }

func testStaleSettleAfterCrashFailure(t *testing.T, engine string) {
	ctx := context.Background()
	q, _, clock := queue(t, engine, Options{})
	var exhausted atomic.Int32
	Define(q, "work", func(ctx context.Context, s string) error { return errors.New("too late") },
		Opts[string]{OnExhausted: func(ctx context.Context, s string, err error) { exhausted.Add(1) }}).Enqueue(ctx, "x")
	crashTwice(t, q, clock)
	stale, ok, _ := q.claim(ctx, "default", "stalled")
	if !ok {
		t.Fatal("nothing claimed")
	}
	clock.Advance(6 * time.Minute)
	if err := q.maintain(ctx, nil); err != nil {
		t.Fatal(err)
	}
	q.execute(ctx, stale)
	if failed, _ := q.Failed(ctx); len(failed) != 1 || failed[0].Error != "its process died 3 times" || exhausted.Load() != 1 {
		t.Errorf("a stalled live worker settled to failed after maintenance failed its job for its 3rd crash: failed %+v, OnExhausted %d; want the crash failure kept and OnExhausted once", failed, exhausted.Load())
	}
}

func TestStaleSettleAfterRetry(t *testing.T) { onEngines(t, testStaleSettleAfterRetry) }

func testStaleSettleAfterRetry(t *testing.T, engine string) {
	for _, c := range []struct {
		name     string
		attempts int
		stopping bool
	}{{"put back", 1, true}, {"tried again", 3, false}} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			q, _, clock := queue(t, engine, Options{})
			Define(q, "work", func(ctx context.Context, s string) error { return errors.New("too late") },
				Opts[string]{Retry: Retry{Attempts: c.attempts, Wait: time.Minute}}).Enqueue(ctx, "x")
			crashTwice(t, q, clock)
			stale, ok, _ := q.claim(ctx, "default", "stalled")
			if !ok {
				t.Fatal("nothing claimed")
			}
			clock.Advance(6 * time.Minute)
			if err := q.maintain(ctx, nil); err != nil {
				t.Fatal(err)
			}
			failed, _ := q.Failed(ctx)
			if len(failed) != 1 {
				t.Fatalf("failed %+v; want the job failed for its 3rd crash", failed)
			}
			if err := q.Retry(ctx, failed[0].ID); err != nil {
				t.Fatal(err)
			}
			runCtx, cancel := context.WithCancel(ctx)
			if c.stopping {
				cancel()
			}
			defer cancel()
			q.execute(runCtx, stale)
			if p, _ := q.Pending(ctx); len(p) != 1 || p[0].State != "ready" || p[0].Attempts != 0 || p[0].Error != "" || !p[0].RunAt.Equal(clock.Now()) {
				t.Errorf("a stalled live worker's settle (%s) after its job failed for crashes and was retried: %+v; want the retried job as Retry left it", c.name, p)
			}
		})
	}
}

func TestRunWaitsForItsOwnHooks(t *testing.T) { onEngines(t, testRunWaitsForItsOwnHooks) }

func testRunWaitsForItsOwnHooks(t *testing.T, engine string) {
	ctx := context.Background()
	q, d, _ := queue(t, engine, Options{Now: time.Now, Poll: 10 * time.Millisecond, Grace: 50 * time.Millisecond,
		Heartbeat: 20 * time.Millisecond, Dead: 100 * time.Millisecond})
	entered, release := make(chan struct{}, 2), make(chan struct{})
	var exhausted atomic.Int32
	work := Define(q, "work", func(ctx context.Context, s string) error { return nil },
		Opts[string]{OnExhausted: func(ctx context.Context, s string, err error) {
			exhausted.Add(1)
			entered <- struct{}{}
			<-release
		}})
	id := orphan(t, d, work, "crashed", time.Now().UTC())
	if _, err := d.Write.ExecContext(ctx, `UPDATE gantry_jobs SET crashes = 2 WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	stopped := make(chan error, 2)
	var stops []context.CancelFunc
	for range 2 {
		runCtx, stop := context.WithCancel(ctx)
		stops = append(stops, stop)
		go func() { stopped <- q.Run(runCtx) }()
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no Run's maintenance failed the job of a process that died 3 times")
	}
	for _, stop := range stops {
		stop()
	}
	early, timeout := 0, time.After(time.Second)
collect:
	for early < 2 {
		select {
		case <-stopped:
			early++
		case <-timeout:
			break collect
		}
	}
	close(release)
	for range 2 - early {
		<-stopped
	}
	if early != 1 || exhausted.Load() != 1 {
		t.Errorf("two Runs on one Queue, one running OnExhausted for a job it failed: %d returned before the hook did, OnExhausted %d; want the other Run returned, and OnExhausted once", early, exhausted.Load())
	}
}

func TestClaimCommitFailureDoesNotRun(t *testing.T) {
	ctx := context.Background()
	q, d, _ := queue(t, "postgres", Options{Queues: map[string]int{"default": 2}, Now: time.Now,
		Poll: 10 * time.Millisecond, Grace: 50 * time.Millisecond})
	release := make(chan struct{})
	var runs, inside, most atomic.Int32
	work := Define(q, "work", func(ctx context.Context, s string) error {
		runs.Add(1)
		n := inside.Add(1)
		defer inside.Add(-1)
		for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}, Opts[string]{})
	for _, stmt := range []string{
		`CREATE FUNCTION fail_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'commit failed'; END $$`,
		`CREATE CONSTRAINT TRIGGER fail_commit AFTER UPDATE ON gantry_jobs DEFERRABLE INITIALLY DEFERRED
			FOR EACH ROW WHEN (NEW.state = 'claimed') EXECUTE FUNCTION fail_commit()`,
	} {
		if _, err := d.Write.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	work.Enqueue(ctx, "x")
	if c, ok, err := q.claim(ctx, "default", "direct"); ok || err == nil {
		t.Errorf("a claim whose commit failed: %+v, %v, %v; want nothing claimed, and the error", c, ok, err)
	}
	runCtx, stop := context.WithCancel(ctx)
	stopped := make(chan error, 1)
	go func() { stopped <- q.Run(runCtx) }()
	time.Sleep(300 * time.Millisecond)
	stop()
	close(release)
	<-stopped
	if p, _ := q.Pending(ctx); runs.Load() != 0 || len(p) != 1 || p[0].State != "ready" {
		t.Errorf("every claim's commit failed, yet the job ran %d times, %d at once; it's %+v; want no runs, and it ready", runs.Load(), most.Load(), p)
	}
}

func TestTickNotRerunAtKeepBoundary(t *testing.T) { onEngines(t, testTickNotRerunAtKeepBoundary) }

func testTickNotRerunAtKeepBoundary(t *testing.T, engine string) {
	ctx := context.Background()
	q, d, clock := queue(t, engine, Options{})
	var runs atomic.Int32
	sweep := func(q *Queue) {
		Define(q, "sweep", func(ctx context.Context, s string) error { runs.Add(1); return nil }, Opts[string]{}).Every(time.Hour, "x")
	}
	sweep(q)
	var fired atomic.Bool
	maintainsWhileTickWaits := func() time.Time {
		now := clock.Now()
		if fired.CompareAndSwap(false, true) {
			clock.Advance(time.Second)
			if err := q.maintain(ctx, nil); err != nil {
				t.Error(err)
			}
		}
		return now
	}
	stalled, err := New(ctx, d, Options{Now: clock.Now, Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	sweep(stalled)
	clock.Advance(time.Hour)
	drain(t, q)
	clock.Advance(24 * time.Hour)
	stalled.o.Now = maintainsWhileTickWaits
	if err := stalled.tick(ctx); err != nil {
		t.Fatal(err)
	}
	stalled.o.Now = clock.Now
	drain(t, stalled)
	if runs.Load() != 1 {
		t.Errorf("a process stalled a day ticked 13:00 just inside Keep while another's maintenance ran a second later: the 13:00 sweep ran %d times; want 1", runs.Load())
	}
}

func TestTickNotRerunAtKeepBoundaryControl(t *testing.T) {
	onEngines(t, func(t *testing.T, engine string) {
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
		clock.Advance(24*time.Hour + time.Second)
		q.maintain(ctx, nil)
		if err := stalled.tick(ctx); err != nil {
			t.Fatal(err)
		}
		drain(t, stalled)
		if runs.Load() != 1 {
			t.Errorf("maintenance before the stalled tick read its clock: the 13:00 sweep ran %d times; want 1", runs.Load())
		}
	})
}

func TestNewRejectsHeartbeatAtOrPastDead(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, "sqlite://"+filepath.Join(t.TempDir(), "jobs.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for name, o := range map[string]Options{
		"Heartbeat at Dead":               {Heartbeat: time.Minute, Dead: time.Minute},
		"Heartbeat past the default Dead": {Heartbeat: 6 * time.Minute},
		"the default Heartbeat past Dead": {Dead: 30 * time.Second},
	} {
		if _, err := New(ctx, d, o); err == nil {
			t.Errorf("%s: New took Heartbeat %v and Dead %v; a process would be dead between its beats", name, o.Heartbeat, o.Dead)
		}
	}
	if _, err := New(ctx, d, Options{Heartbeat: time.Minute, Dead: 2 * time.Minute}); err != nil {
		t.Errorf("Heartbeat under Dead: %v", err)
	}
}
