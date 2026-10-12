package web

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scttymn/gantry/devcheck"
)

func TestDevelopmentPanel(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "editor.js")
	if err := os.WriteFile(source, []byte(`document.querySelector('form')`), 0644); err != nil {
		t.Fatal(err)
	}
	page := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if err := DevelopmentWarnings().Render(r.Context(), w); err != nil {
			t.Fatal(err)
		}
	})
	dev := Development(page, root, nil)
	request := func(h http.Handler, accept string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Accept", accept)
		h.ServeHTTP(rec, r)
		return rec
	}
	rec := request(dev, "text/html")
	if !strings.Contains(rec.Body.String(), "editor.js:1") || !strings.Contains(rec.Body.String(), "form-scope") {
		t.Fatal(rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "<details open") {
		t.Fatal("panel should start collapsed")
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(rec.Header())
	}
	if rec := request(page, "text/html"); rec.Body.Len() != 0 {
		t.Fatal("production emitted panel")
	}
	if rec := request(dev, "application/json"); rec.Body.Len() != 0 {
		t.Fatal("non-HTML request emitted panel")
	}
	if err := os.WriteFile(source, []byte(`document.getElementById('editor')`), 0644); err != nil {
		t.Fatal(err)
	}
	if rec := request(dev, "text/html"); rec.Body.Len() != 0 {
		t.Fatal("old warnings survived reload")
	}
}

func TestDevelopmentPanelEscapesSource(t *testing.T) {
	ctx := context.WithValue(context.Background(), developmentWarningsKey{}, []devcheck.Warning{{File: "<img src=x onerror=alert(1)>", Line: 1, Rule: "<script>", Message: "<script>alert(1)</script>"}})
	var out bytes.Buffer
	if err := DevelopmentWarnings().Render(ctx, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "<script>") || strings.Contains(out.String(), "<img") {
		t.Fatal("unescaped source content")
	}
	if !strings.Contains(out.String(), "&lt;script&gt;") {
		t.Fatal(out.String())
	}
}

func TestDevelopmentExtendedRules(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "page.extra"), []byte("custom"), 0644); err != nil {
		t.Fatal(err)
	}
	rules := append(devcheck.DefaultRules(), devcheck.Rule{ID: "extra-feature", Severity: devcheck.Advice, Purpose: "Adopt a built-in feature", Guidance: "Use the helper", Extensions: []string{".extra"}, Check: func(s *devcheck.Source, report func(int, string)) error { report(0, ""); return nil }})
	page := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := DevelopmentWarnings().Render(r.Context(), w); err != nil {
			t.Fatal(err)
		}
	})
	rec := httptest.NewRecorder()
	DevelopmentWithRules(page, root, nil, rules).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(rec.Body.String(), "extra-feature") || !strings.Contains(rec.Body.String(), "Use the helper") {
		t.Fatal(rec.Body.String())
	}
}
