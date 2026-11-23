package testkit

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scttymn/gantry/web"
)

// club is an app with a page for everyone, one for members, one for
// admins, a cached news page, and files.
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

// roles sign in as two members and two admins (an admin is a member too).
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
	// The page cache skips no one: a page with who's signed in on it would
	// be shown to everyone after.
	everyone := func() *web.PageCache { return &web.PageCache{Skip: func(*http.Request) bool { return false }} }
	for _, tc := range []struct {
		name   string
		rt     *web.Router
		yaml   string
		signIn Roles
		want   []string // in what it reports; none: it passes
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

// A feature map that doesn't say what Verify needs fails before anything
// is visited.
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
