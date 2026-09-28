package web

import (
	"sync"
	"time"
)

// Limits counts requests in fixed windows, as Rails' rate_limit does. It's
// in memory: one process's count, reset by a restart, which is what a small
// app on one host needs. The zero value is ready to use.
type Limits struct {
	mu      sync.Mutex
	windows map[string]*window
}

type window struct {
	count int
	ends  time.Time
}

// Allow counts a request under key and reports whether it's within to per
// within.
func (l *Limits) Allow(key string, to int, within time.Duration, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.windows == nil {
		l.windows = map[string]*window{}
	}
	if len(l.windows) > 10000 { // forget finished windows rather than grow
		for k, w := range l.windows {
			if !now.Before(w.ends) {
				delete(l.windows, k)
			}
		}
	}
	w, ok := l.windows[key]
	if !ok || !now.Before(w.ends) {
		w = &window{ends: now.Add(within)}
		l.windows[key] = w
	}
	w.count++
	return w.count <= to
}
