package web

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scttymn/gantry/devcheck"
)

func TestRouterAutomaticallyInjectsDevelopmentPanel(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	// A real source finding ensures the router must scan; no template hook.
	if err := os.WriteFile(filepath.Join(root, "page.html"), []byte(`<script src="/app.js"></script>`), 0644); err != nil {
		t.Fatal(err)
	}
	original := `<!doctype html><html><head><title>App</title></head><body><main>Plain HTML template</main></body></html>`
	for _, tc := range []struct {
		env, disabled string
		want          bool
	}{{"development", "", true}, {"development", "false", false}, {"production", "", false}, {"test", "", false}, {"", "", false}} {
		t.Run(tc.env+tc.disabled, func(t *testing.T) {
			t.Setenv("GANTRY_ENV", tc.env)
			t.Setenv("GANTRY_DEV_WARNINGS", tc.disabled)
			t.Setenv("GANTRY_DEV_RELOAD", "false")
			rt := NewRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
			rt.Handle("GET /{$}", func(w http.ResponseWriter, r *http.Request) error {
				w.Header().Set("Content-Type", "text/html")
				w.Header().Set("Content-Length", fmt.Sprint(len(original)))
				w.Header().Set("ETag", "old")
				_, err := io.WriteString(w, original)
				return err
			})
			rec := httptest.NewRecorder()
			rt.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
			has := strings.Contains(rec.Body.String(), "data-gantry-development-layout")
			if has != tc.want {
				t.Fatalf("panel=%v body=%s", has, rec.Body.String())
			}
			if tc.want {
				if !strings.Contains(rec.Body.String(), "asset-url") || rec.Header().Get("ETag") != "" || rec.Header().Get("Content-Length") != "" {
					t.Fatal(rec.Header(), rec.Body.String())
				}
			} else if rec.Body.String() != original {
				t.Fatal("nondevelopment/disabled response changed")
			}
		})
	}
}

// The panel keeps working under the app's Content-Security-Policy, which
// allows styles from the app itself and by hash: its style is a stylesheet
// the middleware serves, not style attributes.
func TestDevelopmentPanelUnderCSP(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("page.html", []byte(`<script src="/app.js"></script>`), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GANTRY_ENV", "development")
	t.Setenv("GANTRY_DEV_RELOAD", "false")
	rt := NewRouter(slog.New(slog.DiscardHandler), nil)
	rt.Security = Security{CSP: ContentSecurityPolicy("", "")}
	rt.Handle("GET /{$}", func(w http.ResponseWriter, r *http.Request) error {
		w.Header().Set("Content-Type", "text/html")
		_, err := io.WriteString(w, `<!doctype html><html><head></head><body><main>Page</main></body></html>`)
		return err
	})
	h := rt.Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	page := rec.Body.String()
	if !strings.Contains(page, "asset-url") || strings.Contains(page, "style=") || strings.Contains(page, "<style") {
		t.Fatalf("a panel that needs inline style: %s", page)
	}
	if strings.Count(page, `<link rel="stylesheet" href="/_gantry/development.css">`) != 1 {
		t.Fatalf("not one stylesheet: %s", page)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/_gantry/development.css", nil))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "text/css; charset=utf-8" || !strings.Contains(rec.Body.String(), "[data-gantry-development-layout]") {
		t.Fatalf("the stylesheet: %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
}

func warningContext() context.Context {
	return context.WithValue(context.Background(), developmentWarningsKey{}, []devcheck.Warning{{File: "page.html", Line: 1, Rule: "asset-url", Severity: "error", Message: "Use assets.Path"}})
}
func TestDevelopmentInjectionBoundaries(t *testing.T) {
	original := `<html><body><script>const text = "</body>";</script><p>content</p></body></html>`
	result, ok := injectDevelopmentHTML(warningContext(), []byte(original))
	if !ok || !bytes.Contains(result, []byte(`<script>const text = "</body>";</script>`)) {
		t.Fatal(string(result))
	}
	if again, changed := injectDevelopmentHTML(warningContext(), result); changed || !bytes.Equal(result, again) {
		t.Fatal("double injection")
	}
	for _, fragment := range []string{`<p>Turbo fragment</p>`, `{"body":"example"}`} {
		if _, changed := injectDevelopmentHTML(warningContext(), []byte(fragment)); changed {
			t.Fatal("modified fragment")
		}
	}
}
func TestDevelopmentInjectionPreservesSpecialResponses(t *testing.T) {
	for _, mode := range []string{"json", "stream", "large", "encoded", "range", "head"} {
		t.Run(mode, func(t *testing.T) {
			original := `<html><body>content</body></html>`
			if mode == "large" {
				original = strings.Repeat("x", developmentBufferLimit) + original
			}
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				switch mode {
				case "json":
					w.Header().Set("Content-Type", "application/json")
				case "encoded":
					w.Header().Set("Content-Encoding", "gzip")
				case "range":
					w.Header().Set("Content-Range", "bytes 0-10/30")
				}
				w.WriteHeader(http.StatusAccepted)
				if mode == "stream" {
					if err := http.NewResponseController(w).Flush(); err != nil {
						t.Fatal(err)
					}
				}
				io.WriteString(w, original)
			})
			req := httptest.NewRequest("GET", "/", nil).WithContext(warningContext())
			if mode == "head" {
				req.Method = "HEAD"
			}
			rec := httptest.NewRecorder()
			serveDevelopmentPage(h, rec, req, injectDevelopmentHTML)
			if rec.Code != http.StatusAccepted || rec.Body.String() != original {
				t.Fatal("response altered", mode, rec.Code)
			}
		})
	}
}
