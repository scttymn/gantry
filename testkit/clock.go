package testkit

import (
	"sync"
	"time"
)

// FakeClock is a clock a test sets and moves (Rails' travel_to). gantry's
// parts take a Now func: hand them its Now, and time moves only when the
// test says so, for this test alone.
type FakeClock struct {
	mu  sync.Mutex
	now time.Time
}

// Clock is a clock stopped at start.
func Clock(start time.Time) *FakeClock { return &FakeClock{now: start} }

// Now is the clock's time.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Set moves the clock to t.
func (c *FakeClock) Set(t time.Time) {
	c.mu.Lock()
	c.now = t
	c.mu.Unlock()
}

// Advance moves the clock on by d.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}
