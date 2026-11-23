//go:build nodevcheck

package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDevelopmentWithoutChecks(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if err := os.WriteFile(filepath.Join(root, "page.html"), []byte(`<script src="/app.js"></script>`), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GANTRY_ENV", "development")
	t.Setenv("GANTRY_DEV_RELOAD", "false")
	original := `<!doctype html><html><head><title>App</title></head><body><main>Plain HTML template</main></body></html>`
	rt := NewRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	rt.Handle("GET /{$}", func(w http.ResponseWriter, r *http.Request) error {
		w.Header().Set("Content-Type", "text/html")
		_, err := io.WriteString(w, original)
		return err
	})
	rec := httptest.NewRecorder()
	rt.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Body.String() != original {
		t.Fatalf("a nodevcheck build changed the page: %s", rec.Body.String())
	}
	if !strings.HasPrefix(rec.Header().Get("Server-Timing"), "gantry;") {
		t.Fatalf("no Server-Timing: %v", rec.Header())
	}
}
