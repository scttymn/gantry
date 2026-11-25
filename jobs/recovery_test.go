package jobs

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
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
