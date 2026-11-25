package jobs

import (
	"context"
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
			if err := q.maintain(ctx); err != nil {
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
	if err := b.maintain(ctx); err != nil {
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
			if err := q.maintain(ctx); err != nil {
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
		q.maintain(ctx)
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
