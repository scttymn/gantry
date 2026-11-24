package testkit

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/scttymn/gantry/web"
)

func TestWalk(t *testing.T) {
	t.Run("healthy app with lock_version and roles passes", func(t *testing.T) {
		var mu sync.Mutex
		title := "Initial"
		lockVersion := 0
		rt := web.NewRouter(slog.New(slog.DiscardHandler), nil)
		rt.Cache = &web.PageCache{}
		rt.Handle("GET /{$}", func(w http.ResponseWriter, r *http.Request) error {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, `<!DOCTYPE html><h1>Home</h1><a href="/posts/1/edit">Edit</a><a href="/news">News</a>`)
			return nil
		})
		rt.Cached("GET /news", func(w http.ResponseWriter, r *http.Request) error {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, `<!DOCTYPE html><h1>News</h1><a href="/">Home</a>`)
			return nil
		})
		rt.Handle("GET /login", func(w http.ResponseWriter, r *http.Request) error {
			http.SetCookie(w, &http.Cookie{Name: "session", Value: r.URL.Query().Get("as"), Path: "/"})
			return nil
		})
		rt.Handle("GET /posts/1/edit", func(w http.ResponseWriter, r *http.Request) error {
			mu.Lock()
			curTitle, curVer := title, lockVersion
			mu.Unlock()
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, `<!DOCTYPE html><form action="/posts/1" method="post"><input type="hidden" name="post[lock_version]" value="%d"><input name="post[title]" value="%s"><button>Save</button></form>`, curVer, curTitle)
			return nil
		})
		rt.Handle("POST /posts/1", func(w http.ResponseWriter, r *http.Request) error {
			sent := web.Sent(r, "post")
			mu.Lock()
			defer mu.Unlock()
			if sent["lock_version"] != strconv.Itoa(lockVersion) {
				w.WriteHeader(http.StatusConflict)
				fmt.Fprint(w, "conflict")
				return nil
			}
			if strings.TrimSpace(sent["title"]) == "" {
				w.WriteHeader(http.StatusUnprocessableEntity)
				return nil
			}
			title = sent["title"]
			lockVersion++
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return nil
		})
		report := Walk(t, rt.Handler(), WalkOptions{
			Seed:       42,
			Steps:      40,
			CheckLinks: true,
			Roles: Roles{
				SignedIn: func(v *Visitor, n int) string {
					v.Get("/login?as=secret-member")
					return "secret-member"
				},
			},
		})
		if !report.Passed || report.Steps != 40 || len(report.Errors) != 0 {
			t.Fatalf("unexpected report: %+v", report)
		}
	})

	t.Run("catches 500 error with reproducible seed and trace", func(t *testing.T) {
		rt := web.NewRouter(slog.New(slog.DiscardHandler), nil)
		rt.Handle("GET /{$}", func(w http.ResponseWriter, r *http.Request) error {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, `<!DOCTYPE html><a href="/boom">Boom</a>`)
			return nil
		})
		rt.Handle("GET /boom", func(w http.ResponseWriter, r *http.Request) error {
			w.WriteHeader(http.StatusInternalServerError)
			return nil
		})
		var report WalkReport
		r := failures(t, func(tb testing.TB) {
			report = Walk(tb, rt.Handler(), WalkOptions{Seed: 7, Steps: 25})
		})
		got := strings.Join(r.errors, "\n")
		if report.Passed || !strings.Contains(got, "seed=7") || !strings.Contains(got, "returned 500") {
			t.Errorf("report=%+v, errors=%q", report, got)
		}
	})

	t.Run("catches stale lock_version overwrite", func(t *testing.T) {
		rt := web.NewRouter(slog.New(slog.DiscardHandler), nil)
		rt.Handle("GET /{$}", func(w http.ResponseWriter, r *http.Request) error {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, `<!DOCTYPE html><form action="/save" method="post"><input type="hidden" name="item[lock_version]" value="0"><input name="item[name]" value="x"><button>Save</button></form>`)
			return nil
		})
		rt.Handle("POST /save", func(w http.ResponseWriter, r *http.Request) error {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return nil
		})
		var report WalkReport
		r := failures(t, func(tb testing.TB) {
			report = Walk(tb, rt.Handler(), WalkOptions{Seed: 3, Steps: 25})
		})
		got := strings.Join(r.errors, "\n")
		if report.Passed || !strings.Contains(got, "want 409") {
			t.Errorf("report=%+v, errors=%q", report, got)
		}
	})

	t.Run("catches public leak of signed-in marker", func(t *testing.T) {
		rt := web.NewRouter(slog.New(slog.DiscardHandler), nil)
		rt.Handle("GET /{$}", func(w http.ResponseWriter, r *http.Request) error {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, `<!DOCTYPE html><p>Hello alice@example.com</p>`)
			return nil
		})
		r := failures(t, func(tb testing.TB) {
			Walk(tb, rt.Handler(), WalkOptions{
				Seed:  1,
				Steps: 3,
				Roles: Roles{
					SignedIn: func(v *Visitor, n int) string { return "alice@example.com" },
				},
			})
		})
		if !strings.Contains(strings.Join(r.errors, "\n"), "leaked signed-in marker") {
			t.Errorf("errors: %q", r.errors)
		}
	})
}
