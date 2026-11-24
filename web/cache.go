package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"slices"
	"strconv"
	"strings"
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
// Only 200s are kept, never a response that sets a cookie or says private
// or no-store, and never one to a request with an Authorization header.
// Browsers are told no-cache with an ETag and a Last-Modified, so they check
// back each time and get a 304 when nothing changed: by the ETag when the
// request has If-None-Match (RFC 9110: it wins), else by the date. Last-Modified is when
// the page last changed: its first render since the app started (a
// deploy), or the first render that came out different from the one before
// (an edit in the admin, say). Rendered again the same, it keeps its date.
// It's there for what's between a browser and the app: a proxy that may
// rewrite HTML (Cloudflare) drops ETags, and passes Last-Modified on.
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
	// Skip names more requests that go straight to the handler, neither
	// served from the cache nor kept in it, so a page that looks different
	// to someone signed in (auth.SignedIn) isn't kept for, or served to,
	// the wrong visitor. nil: a request with a "session" cookie, auth's
	// default. An app that renames its cookie sets a.Auth.HasSessionCookie;
	// one whose pages never change for a signed-in visitor can say none.
	Skip func(*http.Request) bool

	mu      sync.Mutex
	entries map[string]*cachedPage
	making  singleflight.Group
}

type cachedPage struct {
	status   int
	kept     bool
	header   http.Header
	body     []byte
	gzipped  []byte
	etag     string
	modified time.Time
	version  int64
	made     time.Time
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

// Handler serves next's GETs and HEADs from the cache, keyed by host, path
// and query (RequestHost: a trusted proxy's X-Forwarded-Host); a HEAD shares
// its GET's page. Anything else goes straight to next, as does a request
// with credentials (an Authorization header) or one Skip names, and one
// whose response a filter before the cache already set a cookie on (a flash
// taken, say) or marked private or no-store: none is the same for every
// visitor. A filter that decides who may see a page goes outside it, as
// Cached puts a scope's: inside, a kept page is served without it.
func (c *PageCache) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.Method != http.MethodGet && r.Method != http.MethodHead) ||
			len(r.Header.Values("Authorization")) > 0 || c.skip(r) ||
			len(w.Header().Values("Set-Cookie")) > 0 || private(w.Header()) {
			next.ServeHTTP(w, r)
			return
		}
		version, versioned := int64(0), false
		if c.Version != nil {
			if v, err := c.Version(r.Context()); err == nil {
				version, versioned = v, true
			}
		}
		key := RequestHost(r) + r.URL.RequestURI()
		page := c.fresh(key, version, versioned)
		if page == nil {
			var own *cachedPage
			made, err, _ := c.making.Do(key, func() (any, error) {
				if p := c.fresh(key, version, versioned); p != nil {
					return p, nil
				}
				own = c.make(key, version, next, r)
				return own, nil
			})
			if err != nil {
				return
			}
			page = made.(*cachedPage)
			if page != own && !page.kept {
				next.ServeHTTP(w, r)
				return
			}
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

func (c *PageCache) fresh(key string, version int64, versioned bool) *cachedPage {
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.entries[key]
	if p == nil || c.now().Sub(p.made) >= c.maxAge() || (versioned && p.version != version) {
		return nil
	}
	return p
}

func (c *PageCache) make(key string, version int64, next http.Handler, r *http.Request) *cachedPage {
	rec := &recorder{header: http.Header{}, status: http.StatusOK}
	get := r.Clone(context.WithoutCancel(r.Context()))
	get.Method = http.MethodGet
	next.ServeHTTP(rec, get)
	page := &cachedPage{status: rec.status, header: rec.header, body: rec.body.Bytes(), version: version, made: c.now()}
	if rec.status != http.StatusOK || len(rec.header.Values("Set-Cookie")) > 0 || rec.header.Get("Content-Encoding") != "" || private(rec.header) {
		return page
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
	page.modified = page.made.Truncate(time.Second)
	if before := c.entries[key]; before != nil {
		switch {
		case before.etag == page.etag:
			page.modified = before.modified
		case !page.modified.After(before.modified):
			page.modified = before.modified.Add(time.Second)
		}
	}
	if len(c.entries) >= c.maxEntries() {
		c.evict()
	}
	c.entries[key] = page
	c.mu.Unlock()
	return page
}

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

func (c *PageCache) serve(w http.ResponseWriter, r *http.Request, p *cachedPage) {
	h := w.Header()
	for k, v := range p.header {
		h[k] = slices.Clone(v)
	}
	if !p.kept {
		w.WriteHeader(p.status)
		if r.Method != http.MethodHead {
			w.Write(p.body)
		}
		return
	}
	h.Set("ETag", p.etag)
	h.Set("Last-Modified", p.modified.UTC().Format(http.TimeFormat))
	if h.Get("Cache-Control") == "" {
		h.Set("Cache-Control", "no-cache")
	}
	if p.gzipped != nil && !slices.Contains(h.Values("Vary"), "Accept-Encoding") {
		h.Add("Vary", "Accept-Encoding")
	}
	if unchanged(r, p) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	body := p.body
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

func (c *PageCache) skip(r *http.Request) bool {
	if c.Skip != nil {
		return c.Skip(r)
	}
	s, err := r.Cookie("session")
	return err == nil && s.Value != ""
}

func private(h http.Header) bool {
	for _, v := range h.Values("Cache-Control") {
		for d := range strings.SplitSeq(v, ",") {
			name, _, _ := strings.Cut(strings.TrimSpace(d), "=")
			if strings.EqualFold(name, "private") || strings.EqualFold(name, "no-store") {
				return true
			}
		}
	}
	return false
}

func unchanged(r *http.Request, p *cachedPage) bool {
	if match := r.Header.Get("If-None-Match"); match != "" {
		return match == p.etag || match == "W/"+p.etag
	}
	since, err := http.ParseTime(r.Header.Get("If-Modified-Since"))
	return err == nil && !p.modified.After(since)
}

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
