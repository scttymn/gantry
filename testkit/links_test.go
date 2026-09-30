package testkit

import (
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"testing"
)

// recorder is a testing.TB that keeps what Links reports.
type recorder struct {
	testing.TB
	errors []string
	fatal  bool
}

func (r *recorder) Helper() {}
func (r *recorder) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}
func (r *recorder) Fatalf(format string, args ...any) {
	r.Errorf(format, args...)
	r.fatal = true
	runtime.Goexit()
}
func (r *recorder) Fatal(args ...any) { r.Fatalf("%s", fmt.Sprint(args...)) }

func check(t *testing.T, h http.Handler, p Page) *recorder {
	r := &recorder{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		Links(r, h, p)
	}()
	<-done
	return r
}

const page = `<!DOCTYPE html><html><head>
<link rel="preload" href="/assets/font.woff2" as="font">
<link rel="icon" href="/favicon.svg?v=1">
<style>@font-face{src:url(/assets/font.woff2)} .x{background:url("data:image/gif;base64,R0")}
.n{background:url("data:image/svg+xml,%%3Csvg%%3E%%3Crect filter='url(%%23n)'/%%3E%%3C/svg%%3E")} .q{background:url( '/assets/q.png' )}</style>
<script src="/assets/site.js"></script>
</head><body>
<picture><source media="(max-width: 640px)" srcset="data:image/webp;base64,AAAA">
<img srcset="/photos/k/160w.webp 160w, /photos/k/320w.webp 320w" src="/photos/k/320w.webp"></picture>
<div style="--bg:url(/photos/k/bg.webp)"></div>
<a href="#top">Top</a> <a href="https://example.org/">Elsewhere</a> <a href="mailto:a@b.c">Mail</a>
<a href="schedule?week=1">Next week</a> <a href="/admin">Admin</a> <a href="/logout">Sign out</a>
%s
</body></html>`

func site(extra string, missing ...string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintf(w, page, extra) })
	mux.HandleFunc("GET /admin", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/login", http.StatusSeeOther) })
	mux.HandleFunc("GET /logout", func(w http.ResponseWriter, _ *http.Request) { panic("followed the sign-out link") })
	mux.HandleFunc("GET /schedule", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("week") != "1" {
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		for _, m := range missing {
			if r.URL.Path == m {
				http.NotFound(w, r)
				return
			}
		}
		if r.Header.Get("Cookie") != "session=s" && strings.HasPrefix(r.URL.Path, "/private") {
			http.Error(w, "sign in", http.StatusUnauthorized)
			return
		}
		// Only the files the page has: anything else it asks for is a bug.
		for _, prefix := range []string{"/assets/", "/photos/", "/favicon.svg", "/private/"} {
			if strings.HasPrefix(r.URL.Path, prefix) {
				return
			}
		}
		http.NotFound(w, r)
	})
	return mux
}

func TestSrcset(t *testing.T) {
	for srcset, want := range map[string]string{
		"/a.webp 160w, /b.webp 320w":            "/a.webp|/b.webp",
		"/a.webp":                               "/a.webp",
		"data:image/webp;base64,UklG+/=":        "data:image/webp;base64,UklG+/=",
		"data:image/gif;base64,R0,AA 1x, /b 2x": "data:image/gif;base64,R0,AA|/b",
		"/a.webp,/b.webp":                       "/a.webp,/b.webp", // one URL: no space after the comma
		" /a.webp 1x ,  /b.webp 2x ":            "/a.webp|/b.webp",
		"/a.webp, /b.webp":                      "/a.webp|/b.webp",
		"":                                      "",
	} {
		if got := strings.Join(srcsetURLs(srcset), "|"); got != want {
			t.Errorf("%q: %q, want %q", srcset, got, want)
		}
	}
}

func TestLinks(t *testing.T) {
	t.Run("a page whose references all load passes", func(t *testing.T) {
		if r := check(t, site(""), Page{Path: "/", Skip: []string{"/logout"}}); len(r.errors) != 0 {
			t.Error(r.errors)
		}
	})

	t.Run("each broken reference is reported, with where the page has it", func(t *testing.T) {
		r := check(t, site(`<img src="/photos/gone.webp"><a href="/admin/gone">Gone</a>`, "/photos/k/160w.webp", "/assets/font.woff2", "/photos/gone.webp", "/admin/gone", "/assets/q.png"), Page{Path: "/", Skip: []string{"/logout"}})
		got := strings.Join(r.errors, "\n")
		for _, want := range []string{
			"/: style /assets/q.png: 404",
			"/: link preload /assets/font.woff2: 404",
			"/: img srcset /photos/k/160w.webp: 404",
			"/: img src /photos/gone.webp: 404",
			"/: a /admin/gone: 404",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in:\n%s", want, got)
			}
		}
		if len(r.errors) != 5 {
			t.Errorf("each once:\n%s", got)
		}
	})

	t.Run("a file must be 200; a link may redirect", func(t *testing.T) {
		r := check(t, site(`<img src="/admin">`), Page{Path: "/", Skip: []string{"/logout"}})
		// /admin is checked once, as the first reference to it: the link.
		if len(r.errors) != 0 {
			t.Error(r.errors)
		}
		r = check(t, site(`<img src="/admin?as=img">`), Page{Path: "/", Skip: []string{"/logout"}})
		if len(r.errors) != 1 || !strings.Contains(r.errors[0], "img src /admin?as=img: 303") {
			t.Error(r.errors)
		}
	})

	t.Run("the header goes with every request", func(t *testing.T) {
		p := Page{Path: "/", Skip: []string{"/logout"}}
		if r := check(t, site(`<img src="/private/a.webp">`), p); len(r.errors) != 1 {
			t.Error(r.errors)
		}
		p.Header = http.Header{"Cookie": {"session=s"}}
		if r := check(t, site(`<img src="/private/a.webp">`), p); len(r.errors) != 0 {
			t.Error(r.errors)
		}
	})

	t.Run("a crawl checks every page it can reach within its section, once each", func(t *testing.T) {
		visits := map[string]int{}
		mux := http.NewServeMux()
		pages := map[string]string{
			"/admin":                 `<a href="/admin/programs">Programs</a> <a href="/admin/leads">Leads</a> <a href="/">Site</a>`,
			"/admin/programs":        `<a href="/admin/programs/1/edit">Edit</a> <a href="/admin">Back</a>`,
			"/admin/programs/1/edit": `<img src="/photos/gone.webp"> <a href="/admin/programs">Back</a>`,
			"/admin/leads":           `<a href="/admin/leads.csv">Export</a>`,
			"/":                      `<a href="/elsewhere">Not crawled</a>`,
		}
		for path, body := range pages {
			pattern := path // exact, without a trailing slash
			if path == "/" {
				pattern = "/{$}"
			}
			mux.HandleFunc("GET "+pattern, func(w http.ResponseWriter, _ *http.Request) {
				visits[path]++
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				fmt.Fprint(w, body)
			})
		}
		mux.HandleFunc("GET /admin/leads.csv", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/csv")
		})
		mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
		r := &recorder{TB: t}
		done := make(chan struct{})
		go func() {
			defer close(done)
			Crawl(r, mux, Page{Path: "/admin"}, "/admin")
		}()
		<-done
		if len(r.errors) != 1 || r.errors[0] != "/admin/programs/1/edit: img src /photos/gone.webp: 404" {
			t.Error(r.errors)
		}
		// Each admin page is fetched once as a page and once as a link from each page that links to it.
		if visits["/admin/programs/1/edit"] != 2 || visits["/elsewhere"] != 0 || visits["/"] != 1 {
			t.Error(visits)
		}
	})

	t.Run("a page that doesn't load stops the check", func(t *testing.T) {
		r := check(t, site("", "/nowhere/"), Page{Path: "/nowhere/", Skip: []string{"/logout"}})
		if !r.fatal {
			t.Error(r.errors)
		}
	})
}
