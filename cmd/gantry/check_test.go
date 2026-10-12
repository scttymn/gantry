package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDevelopmentChecks(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"app/editor.html":         "<!doctype html>\n<script src=\"/menu-assets/editor.js?v=1\"></script>\n<link rel=\"stylesheet\" href=\"editor.css\">",
		"app/editor.js":           "// editor\nconst form = document.querySelector('form');",
		"app/safe.templ":          `<script src={ assets.Path("editor.js") }></script><script src="https://cdn.example/app.js"></script><script src="//cdn.example/app.js"></script><script src="/assets/app-abcd1234.js"></script><script src='{{asset "editor.js"}}'></script>`,
		"assets/js/vendor/lib.js": `document.querySelector("form")`,
		"assets/built/editor.js":  `document.querySelector("form")`,
	}
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	warnings, err := checkDevelopment(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 3 {
		t.Fatalf("warnings: %+v", warnings)
	}
	if warnings[0].Line != 2 || warnings[1].Line != 3 || warnings[2].Rule != "form-scope" {
		t.Fatalf("locations: %+v", warnings)
	}
	var out bytes.Buffer
	if got := run([]string{"check", "--strict"}, root, &out, &out); got != 1 {
		t.Fatalf("strict exit=%d: %s", got, out.String())
	}
	if got := run([]string{"check"}, root, &out, &out); got != 0 {
		t.Fatalf("advisory exit=%d", got)
	}
	if !strings.Contains(out.String(), "app/editor.js:2") {
		t.Fatal(out.String())
	}
	if got := run([]string{"check"}, t.TempDir(), &out, &out); got != 0 {
		t.Fatal(got)
	}
}

func TestDevChecksDoNotBlockHouston(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "editor.js"), []byte(`document.querySelector('form')`), 0644); err != nil {
		t.Fatal(err)
	}
	calls := steps(t, "")
	code, _, stderr := gantry(t, root, "dev")
	if code != 0 || len(*calls) != 1 || !strings.Contains(stderr, "form-scope") {
		t.Fatalf("code=%d calls=%v stderr=%s", code, *calls, stderr)
	}
}

func TestNewAppDevelopmentDefaults(t *testing.T) {
	root := t.TempDir()
	code, _, stderr := gantry(t, root, "new", "shop", "--skip-houston")
	if code != 0 {
		t.Fatal(stderr)
	}
	dir := filepath.Join(root, "shop")
	warnings, err := checkDevelopment(dir)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("%v: %+v", err, warnings)
	}
	data, err := os.ReadFile(filepath.Join(dir, "app/shared/layout/layout.templ"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "DevelopmentPage") {
		t.Fatal("generated views should not need a diagnostics hook")
	}

}

func TestStrictAllowsAdvice(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "editor.js"), []byte(`document.querySelector('form')`), 0644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := run([]string{"check", "--strict"}, root, &out, &out); code != 0 {
		t.Fatal(code, out.String())
	}
	if !strings.Contains(out.String(), "advice [form-scope]") {
		t.Fatal(out.String())
	}
}

func TestRuleCatalog(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"check", "--rules"}, t.TempDir(), &out, &out); code != 0 {
		t.Fatal(code, out.String())
	}
	if !strings.Contains(out.String(), "asset-url [error]") || !strings.Contains(out.String(), "cache invalidation") {
		t.Fatal(out.String())
	}
}
