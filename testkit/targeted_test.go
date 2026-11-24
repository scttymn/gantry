package testkit

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
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
