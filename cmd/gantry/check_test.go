package main

import (
	"bytes"
	"encoding/json"
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

// check --json is the same findings and counts, for tools, with field names
// that stay: --strict still fails on a violation, after printing them.
func TestCheckJSON(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "app"), 0o755)
	os.WriteFile(filepath.Join(root, "app", "editor.html"), []byte("<!doctype html>\n<script src=\"/menu-assets/editor.js?v=1\"></script>"), 0o644)
	os.WriteFile(filepath.Join(root, "app", "editor.js"), []byte("// editor\nconst form = document.querySelector('form');"), 0o644)
	var out, errOut bytes.Buffer
	if code := run([]string{"check", "--json", "--strict"}, root, &out, &errOut); code != 1 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	var report struct {
		Violations int `json:"violations"`
		Advisories int `json:"advisories"`
		Findings   []struct {
			File     string `json:"file"`
			Line     int    `json:"line"`
			Rule     string `json:"rule"`
			Severity string `json:"severity"`
			Message  string `json:"message"`
		} `json:"findings"`
	}
	d := json.NewDecoder(&out)
	d.DisallowUnknownFields()
	if err := d.Decode(&report); err != nil {
		t.Fatal(err)
	}
	if report.Violations != 1 || report.Advisories != 1 || len(report.Findings) != 2 {
		t.Fatalf("%+v", report)
	}
	if f := report.Findings[0]; f.File != "app/editor.html" || f.Line != 2 || f.Severity != "error" || f.Rule == "" || f.Message == "" {
		t.Errorf("%+v", f)
	}
	if f := report.Findings[1]; f.Rule != "form-scope" || f.Severity != "advice" {
		t.Errorf("%+v", f)
	}

	// None is an empty list, not null.
	out.Reset()
	if code := run([]string{"check", "--json"}, t.TempDir(), &out, &errOut); code != 0 || strings.TrimSpace(out.String()) != `{"violations":0,"advisories":0,"findings":[]}` {
		t.Errorf("exit %d: %s", code, out.String())
	}

	// --rules --json is the catalog.
	out.Reset()
	if code := run([]string{"check", "--rules", "--json"}, root, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	var rules []map[string]string
	if err := json.Unmarshal(out.Bytes(), &rules); err != nil || len(rules) == 0 || rules[0]["id"] == "" || rules[0]["severity"] == "" || rules[0]["purpose"] == "" || rules[0]["guidance"] == "" || len(rules[0]) != 4 {
		t.Errorf("%v: %s", err, out.String())
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

func TestStaleExceptionIsAdvice(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gantry-check.json"), []byte(`{"exceptions":[{"rule":"asset-url","file":"app/gone.html","reason":"Deleted page."}]}`), 0644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := run([]string{"check", "--strict"}, root, &out, &out); code != 0 {
		t.Fatal(code, out.String())
	}
	if !strings.Contains(out.String(), ".gantry-check.json: advice [stale-exception]: exception asset-url app/gone.html matched nothing") {
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
