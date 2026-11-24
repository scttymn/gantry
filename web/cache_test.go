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
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var flashKey = NewKey[string]("flash")

type cached struct {
	h       http.Handler
	renders atomic.Int64
	version atomic.Int64
	now     time.Time
	cache   *PageCache
	gate    chan struct{}
	steady  atomic.Value
}

func newCached(t *testing.T) *cached {
	t.Helper()
	c := &cached{now: time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)}
	c.cache = &PageCache{
		Version: func(context.Context) (int64, error) { return c.version.Load(), nil },
		Now:     func() time.Time { return c.now },
	}
	rt := NewRouter(slog.New(slog.DiscardHandler), nil)
	rt.Proxies = Proxies{ForwardedHost: true}
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
	rt.Cached("GET /slow-cookie", func(w http.ResponseWriter, r *http.Request) error {
		c.renders.Add(1)
		if c.gate != nil {
			<-c.gate
		}
		who := r.Header.Get("X-Who")
		http.SetCookie(w, &http.Cookie{Name: "who", Value: who})
		io.WriteString(w, "for "+who)
		return nil
	})
	rt.Cached("GET /file", func(w http.ResponseWriter, r *http.Request) error {
		c.renders.Add(1)
		http.ServeContent(w, r, "file.txt", time.Time{}, strings.NewReader("the file"))
		return nil
	})
	rt.Cached("GET /cache-control", func(w http.ResponseWriter, r *http.Request) error {
		c.renders.Add(1)
		w.Header().Set("Cache-Control", r.URL.Query().Get("is"))
		io.WriteString(w, "said "+r.URL.Query().Get("is"))
		return nil
	})
	rt.Cached("POST /page", page)
	membersOnly := func(w http.ResponseWriter, r *http.Request) error {
		if r.Header.Get("X-Member") == "" {
			http.Redirect(w, r, "/login", http.StatusFound)
		}
		return nil
	}
	staffOnly := func(w http.ResponseWriter, r *http.Request) error {
		if r.Header.Get("X-Staff") == "" {
			return Status(http.StatusForbidden, nil)
		}
		return nil
	}
	rt.Scope("/members", Pipeline{membersOnly}, func(s *Scope) {
		s.Cached("GET /page", page)
		s.Scope("/staff", Pipeline{staffOnly}, func(s *Scope) { s.Cached("GET /page", page) })
	})
	rt.Scope("/private", Pipeline{func(w http.ResponseWriter, r *http.Request) error {
		w.Header().Set("Cache-Control", "private")
		return nil
	}}, func(s *Scope) { s.Cached("GET /page", page) })
	takeFlash := func(w http.ResponseWriter, r *http.Request) error {
		if message := r.Header.Get("X-Flash"); message != "" {
			ClearCookie(w, "flash")
			Set(r, flashKey, message)
		}
		return nil
	}
	rt.Scope("/flash", Pipeline{takeFlash}, func(s *Scope) {
		s.Cached("GET /page", func(w http.ResponseWriter, r *http.Request) error {
			c.renders.Add(1)
			message, _ := Get(r, flashKey)
			io.WriteString(w, "<p>"+message+"</p>")
			return nil
		})
	})
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
	t.Run("a scope's filters run before the cache, on every request", func(t *testing.T) {
		c := newCached(t)
		member := c.get("/members/page", "X-Member", "ada")
		if member.Code != 200 || !strings.Contains(member.Body.String(), "render 1") {
			t.Fatalf("member: %d %q", member.Code, member.Body)
		}
		if again := c.get("/members/page", "X-Member", "grace"); again.Body.String() != member.Body.String() || c.renders.Load() != 1 {
			t.Fatalf("a member after the first wasn't served the kept page: renders %d", c.renders.Load())
		}
		anonymous := c.get("/members/page")
		if anonymous.Code != http.StatusFound || anonymous.Header().Get("Location") != "/login" || strings.Contains(anonymous.Body.String(), "render") {
			t.Fatalf("anonymous: %d %v %q", anonymous.Code, anonymous.Header(), anonymous.Body)
		}
	})
	t.Run("a nested scope runs the outer filters, then its own, before the cache", func(t *testing.T) {
		c := newCached(t)
		if staff := c.get("/members/staff/page", "X-Member", "ada", "X-Staff", "yes"); staff.Code != 200 {
			t.Fatalf("staff: %d", staff.Code)
		}
		if member := c.get("/members/staff/page", "X-Member", "grace"); member.Code != http.StatusForbidden || strings.Contains(member.Body.String(), "render") {
			t.Fatalf("a member who isn't staff: %d %q", member.Code, member.Body)
		}
		if anonymous := c.get("/members/staff/page"); anonymous.Code != http.StatusFound || anonymous.Header().Get("Location") != "/login" {
			t.Fatalf("anonymous: %d %v", anonymous.Code, anonymous.Header())
		}
	})
	for name, header := range map[string]http.Header{
		"an Authorization header":                {"Authorization": {"Bearer ada"}},
		"an Authorization header after an empty": {"Authorization": {"", "Bearer ada"}},
		"a session cookie, with no Skip":         {"Cookie": {"session=ada"}},
	} {
		t.Run("a request with "+name+" neither fills nor reads the cache", func(t *testing.T) {
			c := newCached(t)
			signedIn := func() *httptest.ResponseRecorder {
				r := httptest.NewRequest("GET", "/page", nil)
				r.Header = header.Clone()
				w := httptest.NewRecorder()
				c.h.ServeHTTP(w, r)
				return w
			}
			first := signedIn()
			anonymous := c.get("/page")
			if c.renders.Load() != 2 || anonymous.Body.String() == first.Body.String() {
				t.Fatalf("an anonymous visitor got the signed-in render: renders %d", c.renders.Load())
			}
			if signedIn(); c.renders.Load() != 3 {
				t.Fatalf("a signed-in request was served the kept page: renders %d", c.renders.Load())
			}
		})
	}
	t.Run("Skip, when set, decides in place of the session cookie", func(t *testing.T) {
		c := newCached(t)
		c.cache.Skip = func(r *http.Request) bool {
			_, err := r.Cookie("sid")
			return err == nil
		}
		c.get("/page", "Cookie", "session=ada")
		if c.get("/page", "Cookie", "session=ada"); c.renders.Load() != 1 {
			t.Fatalf("a session cookie Skip doesn't name bypassed the cache: renders %d", c.renders.Load())
		}
		if c.get("/page", "Cookie", "sid=ada"); c.renders.Load() != 2 {
			t.Fatalf("what Skip names was served the kept page: renders %d", c.renders.Load())
		}
	})
	t.Run("an empty session cookie doesn't skip the cache", func(t *testing.T) {
		c := newCached(t)
		c.get("/page", "Cookie", "session=")
		if c.get("/page", "Cookie", "session="); c.renders.Load() != 1 {
			t.Fatalf("renders %d", c.renders.Load())
		}
	})
	t.Run("each forwarded host is its own page", func(t *testing.T) {
		c := newCached(t)
		for _, host := range []string{"one.example", "two.example", "one.example"} {
			r := httptest.NewRequest("GET", "/page", nil)
			r.RemoteAddr, r.Host = "10.0.0.2:1", "app.internal"
			r.Header.Set("X-Forwarded-Host", host)
			c.h.ServeHTTP(httptest.NewRecorder(), r)
		}
		if c.renders.Load() != 2 {
			t.Fatalf("renders %d: forwarded hosts shared a page, or one wasn't kept", c.renders.Load())
		}
	})
	t.Run("a response a filter marked private is never kept", func(t *testing.T) {
		c := newCached(t)
		c.get("/private/page")
		if got := c.get("/private/page"); c.renders.Load() != 2 || got.Header().Get("Cache-Control") != "private" {
			t.Fatalf("renders %d, Cache-Control %q", c.renders.Load(), got.Header().Get("Cache-Control"))
		}
	})
	t.Run("a response a filter set a cookie on is never kept", func(t *testing.T) {
		c := newCached(t)
		flashed := c.get("/flash/page", "X-Flash", "saved")
		if !strings.Contains(flashed.Body.String(), "saved") || flashed.Header().Get("Set-Cookie") == "" {
			t.Fatalf("flashed: %v %q", flashed.Header(), flashed.Body)
		}
		if next := c.get("/flash/page"); strings.Contains(next.Body.String(), "saved") {
			t.Fatalf("the next visitor got the flash: %q", next.Body)
		}
	})
	t.Run("each host is its own page", func(t *testing.T) {
		c := newCached(t)
		c.get("http://one.example/page")
		c.get("http://two.example/page")
		if c.renders.Load() != 2 {
			t.Fatal("two hosts shared a page")
		}
		if c.get("http://ONE.example/page"); c.renders.Load() != 2 {
			t.Fatal("a host's page wasn't kept, whatever its case")
		}
	})
	t.Run("a HEAD that misses keeps the whole page, for the GET after", func(t *testing.T) {
		c := newCached(t)
		if head := do(c.h, "HEAD", "/file", nil); head.Code != 200 || head.Body.Len() != 0 {
			t.Fatalf("HEAD: %d %q", head.Code, head.Body)
		}
		if got := c.get("/file"); got.Body.String() != "the file" || c.renders.Load() != 1 {
			t.Fatalf("GET after HEAD: %d %q, renders %d", got.Code, got.Body, c.renders.Load())
		}
	})
	for _, said := range []string{"private", "no-store", "private, max-age=60", "max-age=60, No-Store", `no-cache="Set-Cookie", private`} {
		t.Run("a response that says "+said+" is never kept", func(t *testing.T) {
			c := newCached(t)
			for range 2 {
				if got := c.get("/cache-control?is=" + url.QueryEscape(said)); got.Header().Get("Cache-Control") != said {
					t.Fatalf("%v", got.Header())
				}
			}
			if c.renders.Load() != 2 {
				t.Fatal("kept")
			}
		})
	}
	t.Run("a response that says only max-age is kept", func(t *testing.T) {
		c := newCached(t)
		c.get("/cache-control?is=max-age%3D60")
		c.get("/cache-control?is=max-age%3D60")
		if c.renders.Load() != 1 {
			t.Fatalf("%d renders", c.renders.Load())
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
		time.Sleep(50 * time.Millisecond)
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
	t.Run("a render that isn't kept isn't shared with the requests waiting on it", func(t *testing.T) {
		c := newCached(t)
		c.gate = make(chan struct{})
		var wg sync.WaitGroup
		got := map[string]*httptest.ResponseRecorder{}
		var mu sync.Mutex
		for _, who := range []string{"ada", "grace"} {
			wg.Go(func() {
				rec := c.get("/slow-cookie", "X-Who", who)
				mu.Lock()
				got[who] = rec
				mu.Unlock()
			})
			time.Sleep(20 * time.Millisecond)
		}
		close(c.gate)
		wg.Wait()
		for who, rec := range got {
			if rec.Body.String() != "for "+who || rec.Header().Get("Set-Cookie") != "who="+who {
				t.Errorf("%s got %q and %v", who, rec.Body, rec.Header().Values("Set-Cookie"))
			}
		}
	})
	t.Run("a served page's headers are its own copy", func(t *testing.T) {
		c := newCached(t)
		c.get("/page")
		served := c.get("/page")
		served.Header()["Content-Type"][0] = "changed downstream"
		if next := c.get("/page"); next.Header().Get("Content-Type") != "text/html; charset=utf-8" {
			t.Fatalf("the kept page's header changed: %v", next.Header())
		}
	})
	t.Run("Vary names Accept-Encoding once", func(t *testing.T) {
		c := newCached(t)
		for range 2 {
			if vary := c.get("/page", "Accept-Encoding", "gzip").Header().Values("Vary"); len(vary) != 1 || vary[0] != "Accept-Encoding" {
				t.Fatalf("Vary %q", vary)
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
		c.now = c.now.Add(2 * time.Minute)
		c.version.Add(1)
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
