package testkit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/scttymn/gantry/web"
)

func club(cache *web.PageCache) *web.Router {
	rt := web.NewRouter(slog.New(slog.DiscardHandler), nil)
	rt.Cache = cache
	page := func(body string) web.Handler {
		return func(w http.ResponseWriter, r *http.Request) error {
			who, _ := r.Cookie("session")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			name := ""
			if who != nil {
				name = who.Value
			}
			fmt.Fprintf(w, "<!DOCTYPE html><main>%s <span class=who>%s</span></main>", body, name)
			return nil
		}
	}
	member := func(prefix string) web.Filter {
		return func(w http.ResponseWriter, r *http.Request) error {
			if who, err := r.Cookie("session"); err != nil || !strings.HasPrefix(who.Value, prefix) {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
			}
			return nil
		}
	}
	rt.Handle("GET /{$}", page("<h1>Club</h1>"))
	rt.Handle("GET /login", func(w http.ResponseWriter, r *http.Request) error {
		http.SetCookie(w, &http.Cookie{Name: "session", Value: r.URL.Query().Get("as"), Path: "/"})
		return nil
	})
	rt.Cached("GET /news", page("<h1>News</h1>"))
	rt.Scope("/account", web.Pipeline{member("")}, func(s *web.Scope) {
		s.Handle("GET /{$}", page("<h1>Account</h1>"))
	})
	rt.Scope("/admin", web.Pipeline{member("admin")}, func(s *web.Scope) {
		s.Handle("GET /{$}", page("<h1>Admin</h1>"))
	})
	rt.Mount("GET /assets/", http.NotFoundHandler())
	return rt
}

var roles = Roles{
	SignedIn: func(v *Visitor, n int) string {
		v.Get(fmt.Sprintf("/login?as=member%d", n))
		return fmt.Sprintf("member%d", n)
	},
	Admin: func(v *Visitor, n int) string {
		v.Get(fmt.Sprintf("/login?as=admin%d", n))
		return fmt.Sprintf("admin%d", n)
	},
}

const clubMap = `
features:
  - name: home
    routes: ["GET /{$}", GET /up, GET /login]
    role: public
    visit:
      - path: /
        selector: h1
  - name: news
    routes: [/news]
    role: public
    cacheable: true
    visit:
      - path: /news
  - name: account
    routes: [/account/]
    role: signed-in
    visit:
      - path: /account/
        selector: main h1
  - name: admin
    routes: [GET /admin/]
    role: admin
    visit:
      - path: /admin/
`

func features(t *testing.T, yaml string) string {
	path := filepath.Join(t.TempDir(), "features.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func verify(t *testing.T, rt *web.Router, yaml string, signIn Roles) *recorder {
	path := features(t, yaml)
	return failures(t, func(tb testing.TB) { Verify(tb, rt.Handler(), rt.Routes(), path, signIn) })
}

func TestVerify(t *testing.T) {
	everyone := func() *web.PageCache { return &web.PageCache{Skip: func(*http.Request) bool { return false }} }
	for _, tc := range []struct {
		name   string
		rt     *web.Router
		yaml   string
		signIn Roles
		want   []string
	}{
		{"every feature as every role", club(&web.PageCache{}), clubMap, roles, nil},
		{"a route in no feature", club(&web.PageCache{}), strings.Replace(clubMap, ", GET /login]", "]", 1), roles,
			[]string{"GET /login is in no feature"}},
		{"a feature's route that isn't Cached", club(nil), clubMap, roles,
			[]string{"news is cacheable, and GET /news isn't Cached", "GET /news has no ETag"}},
		{"a Cached route in a feature that isn't cacheable", club(&web.PageCache{}), strings.Replace(clubMap, "cacheable: true", "cacheable: false", 1), roles,
			[]string{"GET /news is Cached, and its feature news isn't cacheable"}},
		{"a member's page shown to everyone", club(everyone()), clubMap, roles,
			[]string{"news: GET /news showed signed-in member0's page to signed-in member1", "news: GET /news showed signed-in member0's page to a public visitor"}},
		{"a members' page open to the public", club(&web.PageCache{}), strings.Replace(clubMap, "GET /login]\n    role: public", "GET /login]\n    role: signed-in", 1), roles,
			[]string{"home as public: GET / = 200: a signed-in page"}},
		{"an admin page that's a member's", club(&web.PageCache{}), strings.Replace(clubMap, "role: admin", "role: signed-in", 1), roles,
			[]string{"admin as signed-in: GET /admin/ = 303, want 200"}},
		{"no selector", club(&web.PageCache{}), strings.Replace(clubMap, "selector: main h1", "selector: form", 1), roles,
			[]string{`account as signed-in: GET /account/ has no "form"`, `account as admin: GET /account/ has no "form"`}},
		{"a status", club(&web.PageCache{}), strings.Replace(clubMap, "      - path: /admin/\n", "      - path: /admin/missing\n        status: 404\n", 1), roles, nil},
		{"no way to sign in as a role", club(&web.PageCache{}), clubMap, Roles{SignedIn: roles[SignedIn]},
			[]string{"admin is for admin visitors, and Verify has no SignIn for admin"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := verify(t, tc.rt, tc.yaml, tc.signIn)
			got := strings.Join(r.errors, "\n")
			if len(tc.want) == 0 && len(r.errors) > 0 {
				t.Errorf("reported:\n%s", got)
			}
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("no %q in:\n%s", want, got)
				}
			}
		})
	}
}

func TestVerifyRefusesDevelopment(t *testing.T) {
	t.Setenv("GANTRY_ENV", "development")
	r := verify(t, club(&web.PageCache{}), clubMap, roles)
	got := strings.Join(r.errors, "\n")
	if !r.fatal || !strings.Contains(got, "GANTRY_ENV=development") || strings.Contains(got, "has no ETag") {
		t.Errorf("Verify under GANTRY_ENV=development, with a cacheable page:\n%s", got)
	}
}

func TestVerifyLeakForOneRole(t *testing.T) {
	rt := web.NewRouter(slog.New(slog.DiscardHandler), nil)
	rt.Cache = &web.PageCache{Skip: func(*http.Request) bool { return false }}
	rt.Handle("GET /login", func(w http.ResponseWriter, r *http.Request) error {
		http.SetCookie(w, &http.Cookie{Name: "session", Value: r.URL.Query().Get("as"), Path: "/"})
		return nil
	})
	rt.Cached("GET /board", func(w http.ResponseWriter, r *http.Request) error {
		toolbar := ""
		if who, err := r.Cookie("session"); err == nil && strings.HasPrefix(who.Value, "admin") {
			toolbar = "<nav>Signed in as " + who.Value + "</nav>"
		}
		fmt.Fprintf(w, "<!DOCTYPE html>%s<h1>Board</h1>", toolbar)
		return nil
	})
	r := verify(t, rt, `
features:
  - name: sign-in
    routes: [GET /up, GET /login]
    role: public
  - name: board
    routes: [GET /board]
    role: public
    cacheable: true
    visit:
      - path: /board
`, roles)
	got := strings.Join(r.errors, "\n")
	for _, want := range []string{"board: GET /board showed admin admin0's page to admin admin1", "board: GET /board showed admin admin0's page to a public visitor"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in:\n%s", want, got)
		}
	}
}

func TestVerifyLinksAsTheVisitor(t *testing.T) {
	rt := web.NewRouter(slog.New(slog.DiscardHandler), nil)
	token := func(w http.ResponseWriter, r *http.Request) error {
		if r.Header.Get("Authorization") != "Bearer ok" {
			return web.Status(http.StatusUnauthorized, nil)
		}
		return nil
	}
	admin := func(w http.ResponseWriter, r *http.Request) error {
		if c, err := r.Cookie("admin"); err != nil || c.Value != "yes" {
			return web.Status(http.StatusForbidden, nil)
		}
		return nil
	}
	page := func(ref string) web.Handler {
		return func(w http.ResponseWriter, r *http.Request) error {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, `<!DOCTYPE html><h1>Page</h1><img src="%s">`, ref)
			return nil
		}
	}
	file := func(w http.ResponseWriter, r *http.Request) error { return nil }
	rt.Handle("GET /admin/login", func(w http.ResponseWriter, r *http.Request) error {
		http.SetCookie(w, &http.Cookie{Name: "admin", Value: "yes", Path: "/admin"})
		return nil
	})
	rt.Scope("/reports", web.Pipeline{token}, func(s *web.Scope) {
		s.Handle("GET /{$}", page("/reports/chart.png"))
		s.Handle("GET /chart.png", file)
	})
	rt.Scope("/admin", web.Pipeline{token, admin}, func(s *web.Scope) {
		s.Handle("GET /{$}", page("/admin/logo.png"))
		s.Handle("GET /logo.png", file)
	})
	signIn := Roles{
		SignedIn: func(v *Visitor, n int) string { v.Header.Set("Authorization", "Bearer ok"); return "" },
		Admin: func(v *Visitor, n int) string {
			v.Header.Set("Authorization", "Bearer ok")
			v.Get("/admin/login")
			return ""
		},
	}
	r := verify(t, rt, `
features:
  - name: health
    routes: [GET /up, GET /admin/login]
    role: public
  - name: reports
    routes: [/reports/]
    role: signed-in
    visit: [{path: /reports/, selector: h1}]
  - name: admin
    routes: [/admin/]
    role: admin
    visit: [{path: /admin/, selector: h1}]
`, signIn)
	if len(r.errors) > 0 {
		t.Errorf("reported:\n%s", strings.Join(r.errors, "\n"))
	}
}

func TestProbe(t *testing.T) {
	for path, want := range map[string]string{
		"/news":              "/news?gantry-verify=admin",
		"/news?page=2":       "/news?gantry-verify=admin&page=2",
		"/news/?q=a+b&x=%2F": "/news/?gantry-verify=admin&q=a+b&x=%2F",
	} {
		if got := probe(t, path, Admin); got != want {
			t.Errorf("probe(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestFeaturesInvalid(t *testing.T) {
	for yaml, want := range map[string]string{
		"features:\n  - name: x\n    routes: [/x]\n    role: owner\n":                                       `x: role "owner": public, signed-in or admin`,
		"features:\n  - name: x\n    routes: [/x]\n":                                                        `x: role "": public, signed-in or admin`,
		"features:\n  - name: x\n    role: public\n":                                                        "x: no routes",
		"features:\n  - routes: [/x]\n    role: public\n":                                                   "a feature with no name",
		"features:\n  - name: x\n    routes: [/x]\n    role: public\n    cached: true\n":                    "field cached not found",
		"features:\n  - name: x\n    routes: [/x]\n    role: public\n    visit: [{path: x}]\n":              `x: visit "x": a path starts with /`,
		"features:\n  - {name: x, routes: [/x], role: public}\n  - {name: x, routes: [/y], role: public}\n": "x: two features have that name",
	} {
		path := features(t, yaml)
		r := failures(t, func(tb testing.TB) { Features(tb, path) })
		if got := strings.Join(r.errors, "\n"); !r.fatal || !strings.Contains(got, want) {
			t.Errorf("%q: no %q in %q", yaml, want, got)
		}
	}
}

func TestVerifyReport(t *testing.T) {
	runReport := func(t *testing.T, rt *web.Router, yaml string, signIn Roles) (VerifyReport, string) {
		t.Helper()
		var buf bytes.Buffer
		was := verifyJSONOut
		verifyJSONOut = &buf
		t.Cleanup(func() { verifyJSONOut = was })
		t.Setenv("GANTRY_VERIFY_JSON", "1")
		path := features(t, yaml)
		var report VerifyReport
		failures(t, func(tb testing.TB) { report = Verify(tb, rt.Handler(), rt.Routes(), path, signIn) })
		return report, buf.String()
	}

	t.Run("passing report and JSON output", func(t *testing.T) {
		report, raw := runReport(t, club(&web.PageCache{}), clubMap, roles)
		if !report.Passed || report.Failures != 0 || report.Routes != 6 || len(report.UnmappedRoutes) != 0 || len(report.Features) != 4 {
			t.Fatalf("unexpected report: %+v", report)
		}
		line, ok := strings.CutPrefix(strings.TrimSpace(raw), VerifyJSONPrefix)
		if !ok {
			t.Fatalf("no %q prefix in %q", VerifyJSONPrefix, raw)
		}
		var decoded VerifyReport
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, line)
		}
		if !reflect.DeepEqual(decoded, report) {
			t.Errorf("decoded %+v != report %+v", decoded, report)
		}
		if strings.Contains(line, `"unmapped_routes":null`) {
			t.Errorf("unmapped_routes is null in %s", line)
		}
		news := report.Features[1]
		if news.Name != "news" || !news.Passed || len(news.Cache) != 1 || !news.Cache[0].Passed || len(news.Leaks) != 2 {
			t.Errorf("news feature: %+v", news)
		}
	})

	t.Run("unmapped route", func(t *testing.T) {
		report, _ := runReport(t, club(&web.PageCache{}), strings.Replace(clubMap, ", GET /login]", "]", 1), roles)
		if report.Passed || report.Failures != 1 || !reflect.DeepEqual(report.UnmappedRoutes, []string{"GET /login"}) {
			t.Errorf("unmapped report: %+v", report)
		}
	})

	t.Run("cache leak", func(t *testing.T) {
		everyone := &web.PageCache{Skip: func(*http.Request) bool { return false }}
		report, _ := runReport(t, club(everyone), clubMap, roles)
		if report.Passed || report.Failures == 0 {
			t.Fatalf("expected failure: %+v", report)
		}
		news := report.Features[1]
		if news.Passed || len(news.Leaks) != 2 || news.Leaks[0].Passed || !strings.Contains(news.Leaks[0].Error, "showed signed-in member0's page") {
			t.Errorf("news leak report: %+v", news)
		}
	})

	t.Run("broken link captured in visit result", func(t *testing.T) {
		rt := web.NewRouter(slog.New(slog.DiscardHandler), nil)
		rt.Handle("GET /{$}", func(w http.ResponseWriter, r *http.Request) error {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, `<!DOCTYPE html><h1>Home</h1><img src="/missing.png">`)
			return nil
		})
		report, _ := runReport(t, rt, "features:\n  - name: home\n    routes: [\"GET /{$}\", GET /up]\n    role: public\n    visit: [{path: /, selector: h1}]\n", Roles{})
		if report.Passed || len(report.Features) != 1 || len(report.Features[0].Visits) != 1 {
			t.Fatalf("report: %+v", report)
		}
		v := report.Features[0].Visits[0]
		if v.Passed || !strings.Contains(v.Error, "/missing.png: 404") {
			t.Errorf("visit result: %+v", v)
		}
	})
}
