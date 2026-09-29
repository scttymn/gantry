package cache

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/scttymn/gantry/testkit"
)

type stats struct {
	Visits int
	Pages  []string
}

func TestFetch(t *testing.T) {
	ctx := context.Background()
	clock := testkit.Clock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	c := New(Options{Now: clock.Now})
	var calls int
	compute := func() (stats, error) { calls++; return stats{Visits: calls, Pages: []string{"/"}}, nil }
	a, err := Fetch(ctx, c, "stats", time.Hour, compute)
	if err != nil || a.Visits != 1 {
		t.Fatal(a, err)
	}
	a.Pages[0] = "changed" // a copy: what's kept isn't touched
	b, _ := Fetch(ctx, c, "stats", time.Hour, compute)
	if calls != 1 || b.Visits != 1 || b.Pages[0] != "/" {
		t.Errorf("the second fetch: %+v after %d calls", b, calls)
	}
	clock.Advance(59 * time.Minute)
	if _, ok := Read[stats](c, "stats"); !ok {
		t.Error("gone before its hour")
	}
	clock.Advance(time.Minute)
	if _, ok := Read[stats](c, "stats"); ok {
		t.Error("kept past its hour")
	}
	if b, _ := Fetch(ctx, c, "stats", 0, compute); calls != 2 || b.Visits != 2 {
		t.Errorf("after expiry: %+v, %d calls", b, calls)
	}
	clock.Advance(1000 * time.Hour)
	if _, ok := Read[stats](c, "stats"); !ok {
		t.Error("a ttl of 0 expired")
	}

	// An error isn't kept.
	boom := errors.New("boom")
	if _, err := Fetch(ctx, c, "bad", time.Hour, func() (int, error) { return 0, boom }); err != boom {
		t.Errorf("error: %v", err)
	}
	if v, err := Fetch(ctx, c, "bad", time.Hour, func() (int, error) { return 7, nil }); err != nil || v != 7 {
		t.Errorf("after an error: %v %v", v, err)
	}
}

func TestWriteReadDelete(t *testing.T) {
	c := New(Options{})
	if err := c.Write("n", 42, 0); err != nil {
		t.Fatal(err)
	}
	if v, ok := Read[int](c, "n"); !ok || v != 42 {
		t.Errorf("%v %v", v, ok)
	}
	if _, ok := Read[stats](c, "n"); ok {
		t.Error("an int read as a struct")
	}
	c.Write("n", 43, 0)
	if v, _ := Read[int](c, "n"); v != 43 {
		t.Errorf("overwritten: %v", v)
	}
	c.Delete("n")
	if _, ok := Read[int](c, "n"); ok || c.used != 0 {
		t.Errorf("deleted, still there, or counted: %d bytes", c.used)
	}
	c.Write("a", 1, 0)
	c.Write("b", 2, 0)
	c.Clear()
	if _, ok := Read[int](c, "a"); ok || c.used != 0 {
		t.Errorf("after Clear: %d bytes", c.used)
	}
}

// Full, the least recently used goes first.
func TestSize(t *testing.T) {
	c := New(Options{Size: 100})
	value := strings.Repeat("x", 18) // "xxx…" in JSON: 20 bytes, and a 1-byte key
	for _, k := range []string{"a", "b", "c", "d"} {
		c.Write(k, value, 0)
	}
	Read[string](c, "a") // a is used, so b is the oldest
	c.Write("e", value, 0)
	c.Write("f", value, 0)
	for k, want := range map[string]bool{"a": true, "b": false, "c": false, "d": true, "e": true, "f": true} {
		if _, ok := Read[string](c, k); ok != want {
			t.Errorf("%s kept: %v", k, ok)
		}
	}
	if c.used > 100 {
		t.Errorf("%d bytes kept", c.used)
	}
	if err := c.Write("huge", strings.Repeat("x", 200), 0); err != ErrTooBig {
		t.Errorf("a value bigger than the cache: %v", err)
	}
	if _, ok := Read[string](c, "a"); !ok {
		t.Error("a value too big to keep dropped the others")
	}
	if New(Options{}).o.Size != 32<<20 {
		t.Error("the default isn't 32 MB")
	}
}

// Many asking for one key at once compute it once.
func TestFetchOnce(t *testing.T) {
	c := New(Options{})
	var calls atomic.Int32
	release := make(chan struct{})
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			v, err := Fetch(context.Background(), c, "k", 0, func() (int, error) {
				calls.Add(1)
				<-release
				return 9, nil
			})
			if err != nil || v != 9 {
				t.Errorf("%v %v", v, err)
			}
		})
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Errorf("computed %d times", calls.Load())
	}

	// A caller whose context ends stops waiting.
	ctx, cancel := context.WithCancel(context.Background())
	stuck := make(chan struct{})
	go Fetch(context.Background(), c, "slow", 0, func() (int, error) { <-stuck; return 1, nil })
	time.Sleep(20 * time.Millisecond)
	cancel()
	if _, err := Fetch(ctx, c, "slow", 0, func() (int, error) { return 2, nil }); !errors.Is(err, context.Canceled) {
		t.Errorf("waiting past its context: %v", err)
	}
	close(stuck)
}
