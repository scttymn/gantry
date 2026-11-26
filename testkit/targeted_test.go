package testkit

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/scttymn/gantry/web"
)

type silentTB struct {
	testing.TB
	failed bool
	errs   []string
}

func (s *silentTB) Helper() {}

func (s *silentTB) Logf(string, ...any) {}

func (s *silentTB) Errorf(format string, args ...any) {
	s.failed = true
	s.errs = append(s.errs, fmt.Sprintf(format, args...))
}

func (s *silentTB) Fatalf(format string, args ...any) {
	s.failed = true
	s.errs = append(s.errs, fmt.Sprintf(format, args...))
	runtime.Goexit()
}

func TestVerifySelectedFeaturesOnlyVisitsSelected(t *testing.T) {
	t.Setenv("GANTRY_ENV", "test")
	t.Setenv("GANTRY_VERIFY_FEATURES", "posts")

	var homeHits, postHits atomic.Int32
	rt := web.NewRouter(slog.New(slog.DiscardHandler), nil)
	rt.Handle("GET /{$}", func(w http.ResponseWriter, r *http.Request) error {
		homeHits.Add(1)
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<h1>Home</h1>"))
		return nil
	})
	rt.Handle("GET /posts", func(w http.ResponseWriter, r *http.Request) error {
		postHits.Add(1)
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<h1>Posts</h1>"))
		return nil
	})

	fmap := filepath.Join(t.TempDir(), "features.yaml")
	body := `features:
  - name: home
    routes: ["GET /{$}", "GET /up"]
    role: public
    visit:
      - path: /
        selector: h1
  - name: posts
    routes: ["GET /posts"]
    role: public
    visit:
      - path: /posts
        selector: h1
`
	if err := os.WriteFile(fmap, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	report := Verify(t, rt.Handler(), rt.Routes(), fmap, nil)
	if !report.Passed || report.Routes != 3 {
		t.Fatalf("report = %+v", report)
	}
	if len(report.Features) != 1 || report.Features[0].Name != "posts" {
		t.Fatalf("Features = %+v, want only [posts]", report.Features)
	}
	if homeHits.Load() != 0 {
		t.Errorf("home was visited %d times, want 0", homeHits.Load())
	}
	if postHits.Load() == 0 {
		t.Errorf("posts was never visited")
	}
}

func TestVerifySelectedFeaturesStillChecksUnmappedRoutes(t *testing.T) {
	t.Setenv("GANTRY_ENV", "test")
	t.Setenv("GANTRY_VERIFY_FEATURES", "home")

	rt := web.NewRouter(slog.New(slog.DiscardHandler), nil)
	rt.Handle("GET /{$}", func(w http.ResponseWriter, r *http.Request) error {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<h1>Home</h1>"))
		return nil
	})
	rt.Handle("GET /orphan", func(w http.ResponseWriter, r *http.Request) error {
		return nil
	})

	fmap := filepath.Join(t.TempDir(), "features.yaml")
	body := `features:
  - name: home
    routes: ["GET /{$}", "GET /up"]
    role: public
    visit:
      - path: /
        selector: h1
`
	if err := os.WriteFile(fmap, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := &silentTB{TB: t}
	report := Verify(rec, rt.Handler(), rt.Routes(), fmap, nil)
	if report.Passed || len(report.UnmappedRoutes) != 1 || report.UnmappedRoutes[0] != "GET /orphan" {
		t.Fatalf("expected unmapped route detection even when filtered: %+v", report)
	}
}

func TestFilterFeaturesRejectsUnknownName(t *testing.T) {
	rec := &silentTB{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		filterFeatures(rec, "test/features.yaml", []Feature{{Name: "home"}}, []string{"missing"})
	}()
	<-done
	if !rec.failed || len(rec.errs) == 0 || !strings.Contains(rec.errs[0], `no feature "missing"`) {
		t.Fatalf("expected fatal on unknown feature, got failed=%v errs=%v", rec.failed, rec.errs)
	}
}

func TestVerifyReportsItsSelection(t *testing.T) {
	t.Setenv("GANTRY_ENV", "test")
	page := func(w http.ResponseWriter, r *http.Request) error {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<h1>Page</h1>"))
		return nil
	}
	rt := web.NewRouter(slog.New(slog.DiscardHandler), nil)
	rt.Handle("GET /{$}", page)
	rt.Handle("GET /admin", page)
	h := rt.Handler()
	fmap := filepath.Join(t.TempDir(), "features.yaml")
	body := `features:
  - name: home
    routes: ["GET /{$}", "GET /up"]
    role: public
  - name: admin
    routes: ["GET /admin"]
    role: admin
`
	if err := os.WriteFile(fmap, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		env      string
		selected []string
	}{
		{"", []string{}},
		{"home", []string{"home"}},
	} {
		t.Setenv("GANTRY_VERIFY_FEATURES", tc.env)
		rec := &silentTB{TB: t}
		report := Verify(rec, h, rt.Routes(), fmap, nil)
		if !slices.Equal(report.Selected, tc.selected) || report.Selected == nil {
			t.Errorf("GANTRY_VERIFY_FEATURES=%q: selected %#v, want %#v", tc.env, report.Selected, tc.selected)
		}
		if report.Passed || !slices.ContainsFunc(rec.errs, func(msg string) bool {
			return strings.Contains(msg, "admin is for admin visitors, and Verify has no SignIn for admin")
		}) {
			t.Errorf("GANTRY_VERIFY_FEATURES=%q: passed=%v errors %q; a feature whose role can't sign in fails the run, chosen or not", tc.env, report.Passed, rec.errs)
		}
	}
}

func TestVerifyBrowserVisitsOnlyTheChosenFeatures(t *testing.T) {
	t.Setenv("GANTRY_ENV", "test")
	fmap := features(t, `
features:
  - name: home
    routes: ["GET /{$}", GET /up, GET /login, /news]
    role: public
    visit:
      - path: /
        selector: h1
  - name: account
    routes: [/account/, /admin/]
    role: signed-in
    visit:
      - path: /account/
        selector: main h1
`)
	signIn := Roles{SignedIn: func(v *Visitor, n int) string {
		v.Header.Set("X-Role", "member")
		v.Get("/login?as=member0")
		return "member0"
	}}
	for _, tc := range []struct {
		env     string
		options []string
		want    string
	}{
		{"account", nil, "account"},
		{"account", []string{"home"}, "home"},
	} {
		t.Setenv("GANTRY_VERIFY_FEATURES", tc.env)
		cdpSrv, _ := startFakeCDP(t, fakeCDPScenario{})
		manifest := VerifyBrowser(t, club(&web.PageCache{}).Handler(), fmap, signIn, BrowserOptions{CDPURL: cdpSrv.URL, Features: tc.options})
		if len(manifest.Checks) == 0 {
			t.Fatalf("GANTRY_VERIFY_FEATURES=%s, Features %q: no checks", tc.env, tc.options)
		}
		for _, c := range manifest.Checks {
			if c.Feature != tc.want {
				t.Errorf("GANTRY_VERIFY_FEATURES=%s, Features %q: rendered %s, want only %s", tc.env, tc.options, c.Feature, tc.want)
			}
		}
	}
}

func TestVerifyReportsFailuresOfFeaturesLeftOut(t *testing.T) {
	t.Setenv("GANTRY_ENV", "test")
	t.Setenv("GANTRY_VERIFY_FEATURES", "posts")
	page := func(w http.ResponseWriter, r *http.Request) error {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<h1>Page</h1>"))
		return nil
	}
	rt := web.NewRouter(slog.New(slog.DiscardHandler), nil)
	rt.Handle("GET /{$}", page)
	rt.Handle("GET /posts", page)
	fmap := filepath.Join(t.TempDir(), "features.yaml")
	body := `features:
  - name: home
    routes: ["GET /{$}", "GET /up"]
    role: public
    cacheable: true
  - name: posts
    routes: ["GET /posts"]
    role: public
    visit:
      - path: /posts
        selector: h1
`
	if err := os.WriteFile(fmap, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := &silentTB{TB: t}
	report := Verify(rec, rt.Handler(), rt.Routes(), fmap, nil)
	if report.Passed || report.Failures != 2 || len(report.Errors) != 2 {
		t.Fatalf("report = %+v, want home's two uncached routes in Errors", report)
	}
	for _, msg := range report.Errors {
		if !strings.Contains(msg, "home is cacheable") {
			t.Errorf("error %q doesn't name home's failure", msg)
		}
	}
	if !slices.Equal(report.Errors, rec.errs) {
		t.Errorf("Errors = %q, the test reported %q", report.Errors, rec.errs)
	}
	raw, err := json.Marshal(report)
	if err != nil || !strings.Contains(string(raw), `"errors":["`) {
		t.Errorf("JSON %s has no top-level errors (%v)", raw, err)
	}
}
