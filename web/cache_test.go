package web

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// cached is a router with a page cache over a few routes that count their
// renders, at a clock and data version the test moves.
type cached struct {
	h       http.Handler
	renders atomic.Int64
	version atomic.Int64
	now     time.Time
	cache   *PageCache
	gate    chan struct{} // when set, /slow waits for it
	steady  atomic.Value  // what /steady says: the same every render until changed
}

func newCached(t *testing.T) *cached {
	t.Helper()
	c := &cached{now: time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)}
	c.cache = &PageCache{
		Version: func(context.Context) (int64, error) { return c.version.Load(), nil },
		Now:     func() time.Time { return c.now },
	}
	rt := NewRouter(slog.New(slog.DiscardHandler), nil)
	rt.Cache = c.cache
	page := func(w http.ResponseWriter, r *http.Request) error {
		n := c.renders.Add(1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, "<p>render %d of %s</p>%s", n, r.URL.RequestURI(), strings.Repeat("<i>padding</i>", 100))
		return nil
	}
	rt.Cached("GET /page", page)
	rt.Cached("GET /missing", func(w http.ResponseWriter, r *http.Request) error {
		c.renders.Add(1)
		return NotFound
	})
	rt.Cached("GET /cookie", func(w http.ResponseWriter, r *http.Request) error {
		c.renders.Add(1)
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "x"})
		io.WriteString(w, "yours")
		return nil
	})
	rt.Cached("GET /slow", func(w http.ResponseWriter, r *http.Request) error {
		if c.gate != nil {
			<-c.gate
		}
		return page(w, r)
	})
	c.steady.Store("the schedule")
	rt.Cached("GET /steady", func(w http.ResponseWriter, r *http.Request) error {
		c.renders.Add(1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, "<p>"+c.steady.Load().(string)+"</p>")
		return nil
	})
	rt.Cached("POST /page", page)
	c.h = rt.Handler()
	return c
}

func (c *cached) get(path string, headers ...string) *httptest.ResponseRecorder {
	return do(c.h, "GET", path, nil, headers...)
}

func TestPageCache(t *testing.T) {
	t.Run("a second request is served without rendering", func(t *testing.T) {
		c := newCached(t)
		first, second := c.get("/page"), c.get("/page")
		if c.renders.Load() != 1 || first.Body.String() != second.Body.String() || second.Code != 200 {
			t.Fatalf("renders %d; %q vs %q", c.renders.Load(), first.Body.String()[:20], second.Body.String()[:20])
		}
		if second.Header().Get("Content-Type") != "text/html; charset=utf-8" || second.Header().Get("X-Frame-Options") != "SAMEORIGIN" {
			t.Errorf("headers %v", second.Header())
		}
	})
	t.Run("a write makes the next request render again", func(t *testing.T) {
		c := newCached(t)
		c.get("/page")
		c.version.Add(1)
		if c.get("/page").Body.String() == "" || c.renders.Load() != 2 {
			t.Fatalf("renders %d", c.renders.Load())
		}
	})
	t.Run("a page older than its max age is rendered again", func(t *testing.T) {
		c := newCached(t)
		c.get("/page")
		c.now = c.now.Add(59 * time.Second)
		c.get("/page")
		if c.renders.Load() != 1 {
			t.Fatal("rendered again before a minute")
		}
		c.now = c.now.Add(time.Second)
		c.get("/page")
		if c.renders.Load() != 2 {
			t.Fatal("kept past a minute")
		}
	})
	t.Run("each query is its own page", func(t *testing.T) {
		c := newCached(t)
		a, b := c.get("/page?week=1"), c.get("/page?week=2")
		if a.Body.String() == b.Body.String() || c.renders.Load() != 2 {
			t.Fatal("two queries shared a page")
		}
	})
	t.Run("an error page is never kept", func(t *testing.T) {
		c := newCached(t)
		for range 2 {
			if rec := c.get("/missing"); rec.Code != 404 {
				t.Fatalf("%d", rec.Code)
			}
		}
		if c.renders.Load() != 2 {
			t.Fatal("a 404 was cached")
		}
	})
	t.Run("a response that sets a cookie is never kept", func(t *testing.T) {
		c := newCached(t)
		for range 2 {
			if rec := c.get("/cookie"); rec.Header().Get("Set-Cookie") == "" || rec.Body.String() != "yours" {
				t.Fatalf("%v %q", rec.Header(), rec.Body)
			}
		}
		if c.renders.Load() != 2 {
			t.Fatal("a response with a cookie was cached")
		}
	})
	t.Run("gzipped once for a client that takes it, as rendered for one that doesn't", func(t *testing.T) {
		c := newCached(t)
		plain := c.get("/page")
		zipped := c.get("/page", "Accept-Encoding", "gzip")
		if zipped.Header().Get("Content-Encoding") != "gzip" || !strings.Contains(zipped.Header().Get("Vary"), "Accept-Encoding") {
			t.Fatalf("%v", zipped.Header())
		}
		r, err := gzip.NewReader(zipped.Body)
		if err != nil {
			t.Fatal(err)
		}
		unzipped, _ := io.ReadAll(r)
		if !bytes.Equal(unzipped, plain.Body.Bytes()) || zipped.Body.Len() >= plain.Body.Len() {
			t.Fatalf("gzipped %d bytes, plain %d", zipped.Body.Len(), plain.Body.Len())
		}
		if plain.Header().Get("Content-Encoding") != "" {
			t.Error("plain was encoded")
		}
	})
	t.Run("a browser with the current copy gets a 304", func(t *testing.T) {
		c := newCached(t)
		first := c.get("/page")
		etag := first.Header().Get("ETag")
		if etag == "" || first.Header().Get("Cache-Control") != "no-cache" {
			t.Fatalf("%v", first.Header())
		}
		if rec := c.get("/page", "If-None-Match", etag); rec.Code != 304 || rec.Body.Len() != 0 {
			t.Fatalf("%d %q", rec.Code, rec.Body)
		}
		c.version.Add(1)
		if rec := c.get("/page", "If-None-Match", etag); rec.Code != 200 {
			t.Fatalf("a stale copy got %d", rec.Code)
		}
	})
	t.Run("HEAD gets the headers from the cache, and no body", func(t *testing.T) {
		c := newCached(t)
		c.get("/page")
		rec := do(c.h, "HEAD", "/page", nil, "Accept-Encoding", "gzip")
		if rec.Header().Get("Content-Encoding") != "" {
			t.Error("a HEAD said gzip")
		}
		if rec.Code != 200 || rec.Body.Len() != 0 || rec.Header().Get("ETag") == "" || c.renders.Load() != 1 {
			t.Fatalf("%d %q %v renders %d", rec.Code, rec.Body, rec.Header(), c.renders.Load())
		}
	})
	t.Run("other methods aren't cached", func(t *testing.T) {
		c := newCached(t)
		do(c.h, "POST", "/page", strings.NewReader("x=1"))
		do(c.h, "POST", "/page", strings.NewReader("x=1"))
		if c.renders.Load() != 2 {
			t.Fatal("a POST was cached")
		}
	})
	t.Run("requests that arrive together share one render", func(t *testing.T) {
		c := newCached(t)
		c.gate = make(chan struct{})
		var wg sync.WaitGroup
		bodies := make([]string, 20)
		for i := range bodies {
			wg.Go(func() { bodies[i] = c.get("/slow").Body.String() })
		}
		time.Sleep(50 * time.Millisecond) // let them all arrive
		close(c.gate)
		wg.Wait()
		if c.renders.Load() != 1 {
			t.Fatalf("%d renders", c.renders.Load())
		}
		for _, b := range bodies {
			if b != bodies[0] || b == "" {
				t.Fatal("a request got another page, or none")
			}
		}
	})
	t.Run("the number of pages kept is capped", func(t *testing.T) {
		c := newCached(t)
		c.cache.MaxEntries = 3
		for i := range 10 {
			c.get(fmt.Sprintf("/page?n=%d", i))
			c.now = c.now.Add(time.Millisecond)
		}
		c.cache.mu.Lock()
		n := len(c.cache.entries)
		c.cache.mu.Unlock()
		if n > 3 {
			t.Fatalf("%d pages kept", n)
		}
		before := c.renders.Load()
		c.get("/page?n=9")
		if c.renders.Load() != before {
			t.Error("the newest page was evicted")
		}
	})
	t.Run("Clear drops every page", func(t *testing.T) {
		c := newCached(t)
		c.get("/page")
		c.cache.Clear()
		c.get("/page")
		if c.renders.Load() != 2 {
			t.Fatal("kept after Clear")
		}
	})
	t.Run("without a version, pages go by max age alone", func(t *testing.T) {
		c := newCached(t)
		c.cache.Version = func(context.Context) (int64, error) { return 0, fmt.Errorf("no version") }
		c.get("/page")
		c.get("/page")
		if c.renders.Load() != 1 {
			t.Fatal("not cached")
		}
	})
	t.Run("a router without a cache serves cached routes directly", func(t *testing.T) {
		rt := NewRouter(slog.New(slog.DiscardHandler), nil)
		n := 0
		rt.Cached("GET /p", func(w http.ResponseWriter, r *http.Request) error { n++; return nil })
		h := rt.Handler()
		do(h, "GET", "/p", nil)
		do(h, "GET", "/p", nil)
		if n != 2 {
			t.Fatal("cached with no cache")
		}
	})
}

// A kept page says when it last changed: its first render after a start (a
// deploy), or the first render whose content differed from the one before
// (an edit in the admin, a class that's begun). Rendered again the same, it
// keeps its date. If-Modified-Since gets a 304 then, as If-None-Match does:
// a proxy in front that drops ETags (Cloudflare does, when it may rewrite
// HTML) passes Last-Modified on.
func TestPageCacheLastModified(t *testing.T) {
	lastModified := func(t *testing.T, rec *httptest.ResponseRecorder) time.Time {
		t.Helper()
		at, err := http.ParseTime(rec.Header().Get("Last-Modified"))
		if err != nil {
			t.Fatalf("Last-Modified %q: %v", rec.Header().Get("Last-Modified"), err)
		}
		return at
	}
	since := func(at time.Time) string { return at.UTC().Format(http.TimeFormat) }

	t.Run("the first render's time, and a 304 to a browser that has it", func(t *testing.T) {
		c := newCached(t)
		c.now = c.now.Add(250 * time.Millisecond)
		rec := c.get("/steady")
		if at := lastModified(t, rec); !at.Equal(c.now.Truncate(time.Second)) {
			t.Fatalf("Last-Modified %v, want %v", at, c.now.Truncate(time.Second))
		}
		if got := c.get("/steady", "If-Modified-Since", since(c.now)); got.Code != http.StatusNotModified || got.Body.Len() != 0 {
			t.Errorf("If-Modified-Since the page's time: %d, %d bytes", got.Code, got.Body.Len())
		}
		if got := c.get("/steady", "If-Modified-Since", since(c.now.Add(-time.Minute))); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), "the schedule") {
			t.Errorf("If-Modified-Since a minute before: %d", got.Code)
		}
	})

	t.Run("rendered again the same, it keeps its time", func(t *testing.T) {
		c := newCached(t)
		first := lastModified(t, c.get("/steady"))
		c.now = c.now.Add(2 * time.Minute) // past its max age
		c.version.Add(1)                   // and a write elsewhere
		rec := c.get("/steady", "If-Modified-Since", since(first))
		if c.renders.Load() != 2 {
			t.Fatalf("%d renders; the page should have been made again", c.renders.Load())
		}
		if rec.Code != http.StatusNotModified {
			t.Errorf("unchanged content, If-Modified-Since its first time: %d", rec.Code)
		}
		if at := lastModified(t, c.get("/steady")); !at.Equal(first) {
			t.Errorf("Last-Modified %v, want %v kept", at, first)
		}
	})

	t.Run("an edit moves it to when the page changed", func(t *testing.T) {
		c := newCached(t)
		first := lastModified(t, c.get("/steady"))
		c.now = c.now.Add(10 * time.Minute)
		c.steady.Store("the schedule, edited")
		c.version.Add(1)
		rec := c.get("/steady", "If-Modified-Since", since(first))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "edited") {
			t.Fatalf("after an edit, If-Modified-Since the old time: %d %q", rec.Code, rec.Body.String())
		}
		if at := lastModified(t, rec); !at.Equal(c.now.Truncate(time.Second)) {
			t.Errorf("Last-Modified %v, want %v", at, c.now.Truncate(time.Second))
		}
	})

	t.Run("two changes in one second get two times", func(t *testing.T) {
		c := newCached(t)
		first := lastModified(t, c.get("/steady"))
		c.now = c.now.Add(300 * time.Millisecond)
		c.steady.Store("changed within the second")
		c.version.Add(1)
		rec := c.get("/steady", "If-Modified-Since", since(first))
		if rec.Code != http.StatusOK {
			t.Fatalf("a change in the same second, If-Modified-Since the first: %d (the browser would keep the old page)", rec.Code)
		}
		if at := lastModified(t, rec); !at.After(first) {
			t.Errorf("Last-Modified %v isn't after %v", at, first)
		}
	})

	t.Run("an ETag that doesn't match wins over a date that does", func(t *testing.T) {
		c := newCached(t)
		c.get("/steady")
		if got := c.get("/steady", "If-None-Match", `"elsewhere"`, "If-Modified-Since", since(c.now.Add(time.Hour))); got.Code != http.StatusOK {
			t.Errorf("If-None-Match another page, If-Modified-Since later: %d", got.Code)
		}
	})

	t.Run("a page that isn't kept has no date", func(t *testing.T) {
		c := newCached(t)
		for _, path := range []string{"/missing", "/cookie"} {
			if got := c.get(path).Header().Get("Last-Modified"); got != "" {
				t.Errorf("%s: Last-Modified %q", path, got)
			}
		}
	})
}
