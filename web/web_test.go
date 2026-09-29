package web

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// app is a router whose error page names the status, with a few routes.
func app(t *testing.T) (http.Handler, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	rt := NewRouter(slog.New(slog.NewTextHandler(&logs, nil)), func(w http.ResponseWriter, r *http.Request, status int) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		fmt.Fprintf(w, "<h1>page %d</h1>", status)
	})
	rt.Handle("GET /posts/{id}", func(w http.ResponseWriter, r *http.Request) error {
		switch ID(r, "id") {
		case 0: // what a lookup of id 0 gives
			return sql.ErrNoRows
		case 1:
			io.WriteString(w, "post 1")
			return nil
		case 2:
			return fmt.Errorf("loading post: %w", sql.ErrNoRows)
		case 3:
			return Status(http.StatusUnprocessableEntity, errors.New("bad"))
		case 4:
			panic("boom")
		case 5:
			io.WriteString(w, "half a page")
			return errors.New("broke mid-page")
		}
		return errors.New("database down")
	})
	rt.Handle("POST /posts", func(w http.ResponseWriter, r *http.Request) error {
		io.WriteString(w, r.Method+" "+r.PostFormValue("title"))
		return nil
	})
	return rt.Handler(), &logs
}

func do(h http.Handler, method, target string, body io.Reader, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, body)
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestErrors(t *testing.T) {
	h, logs := app(t)
	for _, c := range []struct {
		name, path string
		status     int
		body       string
	}{
		{"a handler's answer", "/posts/1", 200, "post 1"},
		{"a missing row is the 404 page", "/posts/2", 404, "<h1>page 404</h1>"},
		{"a status the handler chose", "/posts/3", 422, "<h1>page 422</h1>"},
		{"a panic is the 500 page", "/posts/4", 500, "<h1>page 500</h1>"},
		{"any other error is the 500 page", "/posts/6", 500, "<h1>page 500</h1>"},
		{"an id that isn't a number finds nothing", "/posts/abc", 404, "<h1>page 404</h1>"},
		{"a path no route matches is the 404 page", "/nowhere", 404, "<h1>page 404</h1>"},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := do(h, "GET", c.path, nil, "Accept", "text/html")
			if rec.Code != c.status || rec.Body.String() != c.body {
				t.Fatalf("%d %q", rec.Code, rec.Body)
			}
		})
	}
	t.Run("an error on something that isn't a page is only a status", func(t *testing.T) {
		if rec := do(h, "GET", "/posts/2", nil, "Accept", "image/webp"); rec.Code != 404 || rec.Body.Len() != 0 {
			t.Errorf("an image: %d %q", rec.Code, rec.Body)
		}
		// Data is answered as data (docs/plans/mission-control.md, G1 item 8).
		if rec := do(h, "GET", "/posts/2", nil, "Accept", "application/json"); rec.Code != 404 || rec.Body.String() != "{\"error\":\"Not Found\"}\n" {
			t.Errorf("JSON: %d %q", rec.Code, rec.Body)
		}
		if rec := do(h, "GET", "/missing.png", nil); rec.Code != 404 || rec.Body.Len() != 0 {
			t.Errorf("a file: %d %q", rec.Code, rec.Body)
		}
	})
	t.Run("an error after the page started is logged, and the connection cut", func(t *testing.T) {
		// net/http cuts the connection on this panic (TestFailureMidStream
		// sees it from a client); called directly, it's the panic itself.
		defer func() {
			if v := recover(); v != http.ErrAbortHandler || !strings.Contains(logs.String(), "broke mid-page") {
				t.Fatalf("recovered %v, logs %s", v, logs)
			}
		}()
		do(h, "GET", "/posts/5", nil)
		t.Fatal("the page was left looking whole")
	})
	t.Run("500s and panics are logged with their path", func(t *testing.T) {
		if !strings.Contains(logs.String(), "database down") || !strings.Contains(logs.String(), "boom") {
			t.Fatal(logs.String())
		}
	})
	t.Run("the health check answers without any routes of the app's", func(t *testing.T) {
		if rec := do(h, "GET", "/up", nil); rec.Code != 200 || rec.Body.String() != "up\n" {
			t.Fatalf("%d %q", rec.Code, rec.Body)
		}
	})
}

func TestProtection(t *testing.T) {
	h, _ := app(t)
	t.Run("a form posted from another site is refused", func(t *testing.T) {
		rec := do(h, "POST", "/posts", strings.NewReader("title=x"), "Sec-Fetch-Site", "cross-site", "Origin", "https://evil.example", "Accept", "text/html")
		if rec.Code != 422 || rec.Body.String() != "<h1>page 422</h1>" {
			t.Fatalf("%d %q", rec.Code, rec.Body)
		}
		if rec := do(h, "POST", "/posts", strings.NewReader("title=x"), "Sec-Fetch-Site", "same-origin"); rec.Code != 200 {
			t.Fatalf("same origin: %d", rec.Code)
		}
	})
	t.Run("every response has Rails' default headers", func(t *testing.T) {
		for _, p := range []string{"/posts/1", "/posts/2", "/nope", "/up"} {
			h := do(h, "GET", p, nil).Header()
			if h.Get("X-Frame-Options") != "SAMEORIGIN" || h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Referrer-Policy") != "strict-origin-when-cross-origin" || h.Get("X-Permitted-Cross-Domain-Policies") != "none" || h.Get("X-XSS-Protection") != "0" {
				t.Errorf("%s: %v", p, h)
			}
		}
	})
	t.Run("a huge post is cut off", func(t *testing.T) {
		rec := do(h, "POST", "/posts", strings.NewReader("title="+strings.Repeat("x", 2<<20)))
		if rec.Code != 413 {
			t.Fatalf("%d", rec.Code)
		}
	})
	t.Run("an HTML form can PATCH and DELETE", func(t *testing.T) {
		rt := NewRouter(slog.New(slog.DiscardHandler), nil)
		rt.Handle("PATCH /posts/{id}", func(w http.ResponseWriter, r *http.Request) error {
			io.WriteString(w, "patched "+r.PostFormValue("title"))
			return nil
		})
		rt.Handle("DELETE /posts/{id}", func(w http.ResponseWriter, r *http.Request) error {
			io.WriteString(w, "deleted")
			return nil
		})
		h := rt.Handler()
		if rec := do(h, "POST", "/posts/1", strings.NewReader("_method=patch&title=x")); rec.Body.String() != "patched x" {
			t.Errorf("%d %q", rec.Code, rec.Body)
		}
		if rec := do(h, "POST", "/posts/1", strings.NewReader("_method=DELETE")); rec.Body.String() != "deleted" {
			t.Errorf("%d %q", rec.Code, rec.Body)
		}
		// Only those three: a form can't turn itself into a GET.
		if rec := do(h, "POST", "/posts/1", strings.NewReader("_method=GET")); rec.Code != 404 {
			t.Errorf("%d %q", rec.Code, rec.Body)
		}
	})
	t.Run("responses are gzipped for a browser that takes it", func(t *testing.T) {
		rec := do(h, "GET", "/nope", nil, "Accept-Encoding", "gzip", "Accept", "text/html")
		if rec.Header().Get("Content-Encoding") != "gzip" {
			t.Fatal(rec.Header())
		}
	})
}

func TestClientIP(t *testing.T) {
	t.Run("the forwarded address counts, not the proxy's", func(t *testing.T) {
		for chain, want := range map[string]string{
			"203.0.113.9":                    "203.0.113.9",
			"203.0.113.9, 172.18.0.5":        "203.0.113.9",
			"1.2.3.4, 203.0.113.9, 10.0.0.2": "203.0.113.9",
			"":                               "172.18.0.2",
			"garbage, 203.0.113.9":           "203.0.113.9",
		} {
			req := httptest.NewRequest("POST", "/leads", nil)
			req.RemoteAddr = "172.18.0.2:40000"
			if chain != "" {
				req.Header.Set("X-Forwarded-For", chain)
			}
			if got := ClientIP(req); got != want {
				t.Errorf("%q: %s, want %s", chain, got, want)
			}
		}
		// A visitor talking to the app directly can't forge where they're from.
		req := httptest.NewRequest("POST", "/leads", nil)
		req.RemoteAddr = "198.51.100.7:5555"
		req.Header.Set("X-Forwarded-For", "203.0.113.9")
		if got := ClientIP(req); got != "198.51.100.7" {
			t.Errorf("a forged header won: %s", got)
		}
	})
}

func TestLimits(t *testing.T) {
	var l Limits
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	for i := range 3 {
		if !l.Allow("login:1.2.3.4", 3, time.Minute, now) {
			t.Fatalf("request %d refused", i+1)
		}
	}
	if l.Allow("login:1.2.3.4", 3, time.Minute, now) {
		t.Fatal("the fourth was allowed")
	}
	if !l.Allow("login:5.6.7.8", 3, time.Minute, now) {
		t.Fatal("another key shares the count")
	}
	if !l.Allow("login:1.2.3.4", 3, time.Minute, now.Add(time.Minute)) {
		t.Fatal("a new window didn't start")
	}
}

// An error answer is never cached, whoever writes it: the router's pages,
// a handler's own status, a mounted handler's 404; and a success keeps its
// own caching.
func TestNoStoreOnErrors(t *testing.T) {
	rt := NewRouter(slog.New(slog.DiscardHandler), nil)
	rt.Handle("GET /boom", func(w http.ResponseWriter, r *http.Request) error { return Status(http.StatusConflict, nil) })
	rt.Handle("GET /own", func(w http.ResponseWriter, r *http.Request) error {
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.WriteHeader(http.StatusTeapot)
		return nil
	})
	rt.Handle("GET /fine", func(w http.ResponseWriter, r *http.Request) error {
		w.Header().Set("Cache-Control", "public, max-age=60")
		_, err := w.Write([]byte("ok"))
		return err
	})
	rt.Mount("GET /files/", http.NotFoundHandler())
	h := rt.Handler()
	for path, want := range map[string]string{"/nope": "no-store", "/boom": "no-store", "/own": "no-store", "/files/x.css": "no-store", "/fine": "public, max-age=60", "/up": ""} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if got := w.Header().Get("Cache-Control"); got != want {
			t.Errorf("%s (%d): %q, want %q", path, w.Code, got, want)
		}
	}
}
