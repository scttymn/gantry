package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/scttymn/gantry/compress"
)

// PageCache keeps whole responses for pages that are the same for every
// visitor: rendered once, gzipped once, and served from memory until the
// data changes or they reach MaxAge. A page with anything per-visitor in it
// (a session's name, a CSRF token) mustn't be cached; that part belongs in
// a fragment of its own.
//
// Only 200s are kept, and never a response that sets a cookie. Browsers are
// told no-cache with an ETag, so they check back each time and get a 304
// when nothing changed.
type PageCache struct {
	// Version changes whenever the data does (db.DB.Version); a page made
	// under another version is stale. nil, or an error: pages go by MaxAge
	// alone.
	Version func(context.Context) (int64, error)
	// MaxAge covers what changes with time rather than data ("today", a
	// countdown, the year). One minute when zero.
	MaxAge time.Duration
	// MaxEntries caps how many pages are kept, so query strings can't grow
	// the cache without bound. 1,000 when zero.
	MaxEntries int
	// Now is time.Now when nil; tests set it.
	Now func() time.Time

	mu      sync.Mutex
	entries map[string]*cachedPage
	making  singleflight.Group
}

type cachedPage struct {
	status  int
	kept    bool        // cacheable, and in the cache
	header  http.Header // the handler's own headers
	body    []byte
	gzipped []byte
	etag    string
	version int64
	made    time.Time
}

func (c *PageCache) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *PageCache) maxAge() time.Duration {
	if c.MaxAge > 0 {
		return c.MaxAge
	}
	return time.Minute
}

func (c *PageCache) maxEntries() int {
	if c.MaxEntries > 0 {
		return c.MaxEntries
	}
	return 1000
}

// Handler serves next's GETs and HEADs from the cache, keyed by path and
// query. Anything else goes straight to next.
func (c *PageCache) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		version, versioned := int64(0), false
		if c.Version != nil {
			if v, err := c.Version(r.Context()); err == nil {
				version, versioned = v, true
			}
		}
		key := r.URL.RequestURI()
		page := c.fresh(key, version, versioned)
		if page == nil {
			made, err, _ := c.making.Do(key, func() (any, error) {
				if p := c.fresh(key, version, versioned); p != nil {
					return p, nil // made while this one waited
				}
				return c.make(key, version, next, r), nil
			})
			if err != nil {
				return
			}
			page = made.(*cachedPage)
		}
		c.serve(w, r, page)
	})
}

// Clear drops every kept page. Version covers the database; Clear is for
// what pages show that isn't in it (files made in the background, say).
func (c *PageCache) Clear() {
	c.mu.Lock()
	c.entries = nil
	c.mu.Unlock()
}

// fresh is the kept page for key, if it's still good.
func (c *PageCache) fresh(key string, version int64, versioned bool) *cachedPage {
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.entries[key]
	if p == nil || c.now().Sub(p.made) >= c.maxAge() || (versioned && p.version != version) {
		return nil
	}
	return p
}

// make renders the page into memory and keeps it if it's cacheable. A page
// that isn't is still returned, uncached, to be replayed to this request
// (and to any that waited on the same render).
func (c *PageCache) make(key string, version int64, next http.Handler, r *http.Request) *cachedPage {
	// The render is shared by whoever waits on it, so it isn't cut short
	// when this request's client goes.
	rec := &recorder{header: http.Header{}, status: http.StatusOK}
	next.ServeHTTP(rec, r.Clone(context.WithoutCancel(r.Context())))
	page := &cachedPage{status: rec.status, header: rec.header, body: rec.body.Bytes(), version: version, made: c.now()}
	if rec.status != http.StatusOK || rec.header.Get("Set-Cookie") != "" || rec.header.Get("Content-Encoding") != "" {
		return page // replayed to whoever waited on it, never kept
	}
	page.kept = true
	sum := sha256.Sum256(page.body)
	page.etag = `"` + hex.EncodeToString(sum[:])[:20] + `"`
	if compress.Compressible(rec.header.Get("Content-Type")) {
		page.gzipped = compress.Bytes(page.body)
	}
	c.mu.Lock()
	if c.entries == nil {
		c.entries = map[string]*cachedPage{}
	}
	if len(c.entries) >= c.maxEntries() {
		c.evict()
	}
	c.entries[key] = page
	c.mu.Unlock()
	return page
}

// evict drops the stale pages, and when none are, the oldest. Called with
// c.mu held.
func (c *PageCache) evict() {
	now := c.now()
	for k, p := range c.entries {
		if now.Sub(p.made) >= c.maxAge() {
			delete(c.entries, k)
		}
	}
	for len(c.entries) >= c.maxEntries() {
		var oldest string
		for k, p := range c.entries {
			if oldest == "" || p.made.Before(c.entries[oldest].made) {
				oldest = k
			}
		}
		delete(c.entries, oldest)
	}
}

// serve writes a page: a 304 when the browser's copy is current, the
// gzipped bytes to a client that takes them, else the body as rendered.
func (c *PageCache) serve(w http.ResponseWriter, r *http.Request, p *cachedPage) {
	h := w.Header()
	for k, v := range p.header {
		h[k] = v
	}
	if !p.kept { // an error page, say: as it was written
		w.WriteHeader(p.status)
		if r.Method != http.MethodHead {
			w.Write(p.body)
		}
		return
	}
	h.Set("ETag", p.etag)
	if h.Get("Cache-Control") == "" {
		h.Set("Cache-Control", "no-cache")
	}
	if p.gzipped != nil {
		h.Add("Vary", "Accept-Encoding")
	}
	if match := r.Header.Get("If-None-Match"); match != "" && (match == p.etag || match == "W/"+p.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	body := p.body
	// A HEAD's headers are the plain page's, as compress.Handler leaves them.
	if p.gzipped != nil && r.Method == http.MethodGet && compress.Accepts(r) {
		h.Set("Content-Encoding", "gzip")
		body = p.gzipped
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		w.Write(body)
	}
}

// recorder keeps a response in memory.
type recorder struct {
	header http.Header
	status int
	wrote  bool
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header { return r.header }

func (r *recorder) WriteHeader(status int) {
	if !r.wrote {
		r.status, r.wrote = status, true
	}
}

func (r *recorder) Write(b []byte) (int, error) {
	r.wrote = true
	return r.body.Write(b)
}

// Cached routes pattern to h through the router's page cache (rt.Cache),
// or straight to h when the router has none.
func (rt *Router) Cached(pattern string, h Handler) {
	if rt.Cache == nil {
		rt.Handle(pattern, h)
		return
	}
	rt.mux.Handle(pattern, rt.Cache.Handler(rt.Wrap(h)))
}
