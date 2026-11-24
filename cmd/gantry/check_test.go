package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/internal/pins"
	"github.com/scttymn/gantry/testkit"
)

func TestDevelopmentChecks(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"app/editor.html":         "<!doctype html>\n<script src=\"/menu-assets/editor.js?v=1\"></script>\n<link rel=\"stylesheet\" href=\"editor.css\">",
		"app/editor.js":           "import \"./menu.js\"\nconst form = document.querySelector('form');",
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

func TestStrictFollowsCachedResources(t *testing.T) {
	root := t.TempDir()
	repo, _ := filepath.Abs("../..")
	if code, _, stderr := gantry(t, root, "new", "shop", "--db", "sqlite", "--module", "example.com/shop", "--skip-houston", "--gantry", repo); code != 0 {
		t.Fatalf("gantry new: exit %d: %s", code, stderr)
	}
	dir := filepath.Join(root, "shop")
	code, out, stderr := gantry(t, dir, "g", "resource", "posts", "title:string")
	if code != 0 {
		t.Fatalf("gantry g resource: exit %d: %s", code, stderr)
	}
	wire(t, dir, out)
	if code, out, _ := gantry(t, dir, "check", "--strict"); code != 0 {
		t.Fatalf("the routed resource fails Strict before it's cached: exit %d\n%s", code, out)
	}
	edit(t, filepath.Join(dir, "app", "routes.go"), "}}, nil)", "}}, rt.Cache.Handler)")
	code, out, _ = gantry(t, dir, "check", "--strict")
	if code != 1 {
		t.Fatalf("check --strict = %d with the resource behind the page cache, want 1\n%s", code, out)
	}
	for _, want := range []string{"cached-scope", "cache-set-cookie", "posts.Controller.Index → resource.Controller.Render → web.Flash.Take at app/shared/resource/resource.go:"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in\n%s", want, out)
		}
	}
}

func TestCheckWithoutFeatureMap(t *testing.T) {
	dir := shopApp(t, "sqlite")
	for _, f := range []string{"test/features.yaml", "app/features_test.go"} {
		if err := os.Remove(filepath.Join(dir, f)); err != nil {
			t.Fatal(err)
		}
	}
	if code, out, stderr := gantry(t, dir, "check", "--strict"); code != 0 || !strings.Contains(out, "0 violation(s), 0 advisory finding(s)") {
		t.Errorf("an app made before the feature map: check --strict exit %d\n%s%s", code, out, stderr)
	}
	steps(t, "")
	if code, _, stderr := gantry(t, dir, "verify"); code != 1 || !strings.Contains(stderr, "no test/features.yaml") {
		t.Errorf("verify, with no feature map: exit %d: %s", code, stderr)
	}
}

func TestCheckJSON(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "app"), 0o755)
	os.WriteFile(filepath.Join(root, "app", "editor.html"), []byte("<!doctype html>\n<script src=\"/menu-assets/editor.js?v=1\"></script>"), 0o644)
	os.WriteFile(filepath.Join(root, "app", "editor.js"), []byte("import \"./menu.js\"\nconst form = document.querySelector('form');"), 0o644)
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

	out.Reset()
	if code := run([]string{"check", "--json"}, t.TempDir(), &out, &errOut); code != 0 || strings.TrimSpace(out.String()) != `{"violations":0,"advisories":0,"findings":[]}` {
		t.Errorf("exit %d: %s", code, out.String())
	}

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

func TestCheckGenerated(t *testing.T) {
	made := map[string]string{
		"app/posts/index_templ.go": "// Code generated by templ - DO NOT EDIT.\n\n// templ: version: " + pins.Templ + "\n\npackage posts\n",
		"app/models/posts.sql.go":  "// Code generated by sqlc. DO NOT EDIT.\n// versions:\n//   sqlc v1.31.1\n// source: posts.sql\n\npackage models\n",
		"app/models/tables.go":     "// Code generated by sqlc. DO NOT EDIT.\n// versions:\n//   sqlc v1.31.1\n\npackage models\n",
	}
	root := t.TempDir()
	for name, body := range map[string]string{
		"sqlc.yaml":             "version: \"2\"\nsql:\n  - engine: sqlite\n    schema: db/schema.sql\n    queries: app/models/*.sql\n",
		"db/schema.sql":         "CREATE TABLE posts (id integer);\n",
		"app/models/posts.sql":  "-- name: ListPosts :many\nSELECT * FROM posts;\n",
		"app/posts/index.templ": "package posts\n",
		".git/HEAD":             "ref: refs/heads/main\n",
	} {
		os.MkdirAll(filepath.Join(root, filepath.Dir(name)), 0o755)
		os.WriteFile(filepath.Join(root, name), []byte(body), 0o644)
	}
	for name, body := range made {
		os.WriteFile(filepath.Join(root, name), []byte(body), 0o644)
	}
	ran := steps(t, "")
	runQuietlyIn = func(dir, name string, args ...string) ([]byte, error) {
		*ran = append(*ran, append([]string{name}, args...))
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			t.Errorf("%s ran in a copy with .git", name)
		}
		for file, source := range map[string]string{
			"app/posts/index_templ.go": "app/posts/index.templ",
			"app/models/posts.sql.go":  "app/models/posts.sql",
			"app/models/tables.go":     "db/schema.sql",
		} {
			if _, err := os.Stat(filepath.Join(dir, source)); err == nil && strings.HasSuffix(file, "_templ.go") == (name == "templ") {
				os.WriteFile(filepath.Join(dir, file), []byte(made[file]), 0o644)
			}
		}
		return nil, nil
	}
	check := func() (int, string) {
		t.Helper()
		*ran = nil
		code, out, stderr := gantry(t, root, "check", "--generated")
		return code, out + stderr
	}

	if code, out := check(); code != 0 || !strings.Contains(out, "0 stale") || len(*ran) != 2 {
		t.Fatalf("fresh: exit %d, ran %q\n%s", code, *ran, out)
	}

	os.WriteFile(filepath.Join(root, "app/posts/index_templ.go"), []byte(made["app/posts/index_templ.go"]+"// by hand\n"), 0o644)
	os.WriteFile(filepath.Join(root, "app/models/posts.sql.go"), []byte(made["app/models/posts.sql.go"]+"// by hand\n"), 0o644)
	os.Remove(filepath.Join(root, "app/models/tables.go"))
	code, out := check()
	for _, want := range []string{
		"app/posts/index_templ.go differs from templ generate of app/posts/index.templ; edit app/posts/index.templ",
		"app/models/posts.sql.go differs from sqlc generate of app/models/posts.sql; edit app/models/posts.sql",
		"app/models/tables.go is missing: sqlc generate makes it from db/schema.sql",
		"3 stale",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("hand edits: no %q in\n%s", want, out)
		}
	}
	if code != 1 {
		t.Errorf("hand edits: exit %d, want 1", code)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "app/posts/index_templ.go")); !strings.HasSuffix(string(b), "// by hand\n") {
		t.Error("the app's own file was regenerated")
	}

	os.Remove(filepath.Join(root, "app/models/posts.sql"))
	os.Remove(filepath.Join(root, "app/models/posts.sql.go"))
	os.WriteFile(filepath.Join(root, "app/posts/index_templ.go"), []byte(made["app/posts/index_templ.go"]), 0o644)
	if code, out := check(); code != 0 || len(*ran) != 1 || (*ran)[0][0] != "templ" {
		t.Errorf("no queries: exit %d, ran %q\n%s", code, *ran, out)
	}

	os.WriteFile(filepath.Join(root, "app/models/posts.sql.go"), []byte(made["app/models/posts.sql.go"]), 0o644)
	os.Remove(filepath.Join(root, "app/posts/index.templ"))
	code, out = check()
	for _, want := range []string{
		"app/models/posts.sql.go isn't made by sqlc generate any more: delete it, or restore app/models/posts.sql",
		"app/posts/index_templ.go isn't made by templ generate any more: delete it, or restore app/posts/index.templ",
		"2 stale",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("orphans: no %q in\n%s", want, out)
		}
	}
	if code != 1 {
		t.Errorf("orphans: exit %d, want 1", code)
	}
	os.Remove(filepath.Join(root, "app/models/posts.sql.go"))
	os.WriteFile(filepath.Join(root, "app/posts/index.templ"), []byte("package posts\n"), 0o644)

	os.WriteFile(filepath.Join(root, "app/posts/index_templ.go"), []byte(strings.Replace(made["app/posts/index_templ.go"], pins.Templ, "v0.3.900", 1)), 0o644)
	code, out = check()
	if want := "app/posts/index_templ.go was made by templ v0.3.900, and the templ here is " + pins.Templ + ": check with the app's, the version its Dockerfile installs"; code != 1 || !strings.Contains(out, want) || strings.Contains(out, "edit app/posts/index.templ") {
		t.Errorf("another templ: exit %d, want 1 and %q, not an edit, in\n%s", code, want, out)
	}
	os.WriteFile(filepath.Join(root, "app/posts/index_templ.go"), []byte(made["app/posts/index_templ.go"]), 0o644)

	lookPath = func(name string) (string, error) { return "", exec.ErrNotFound }
	if code, out := check(); code != 1 || !strings.Contains(out, "templ isn't installed") {
		t.Errorf("no templ: exit %d\n%s", code, out)
	}
}

func TestCheckGeneratedTools(t *testing.T) {
	for _, tool := range []string{"templ", "sqlc"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " isn't installed: run it in the toolchain (bin/go)")
		}
	}
	dir := shopApp(t, "sqlite")
	generate(t, dir, "posts", "title:string")
	d := testkit.DB(t, func(ctx context.Context, d *db.DB) error {
		return d.Migrate(ctx, os.DirFS(filepath.Join(dir, "db", "migrations")), "app_migrations")
	})
	schema, err := d.Schema(t.Context(), "app_migrations")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "db", "schema.sql"), []byte(schema), 0o644)
	for _, tool := range []string{"sqlc", "templ"} {
		cmd := exec.Command(tool, "generate")
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s generate: %v\n%s", tool, err, out)
		}
	}
	if code, out, stderr := gantry(t, dir, "check", "--generated"); code != 0 {
		t.Fatalf("fresh: exit %d\n%s%s", code, out, stderr)
	}
	for _, file := range []string{"app/posts/index_templ.go", "app/models/posts.sql.go"} {
		b, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, file), append(b, "// by hand\n"...), 0o644)
	}
	code, out, _ := gantry(t, dir, "check", "--generated")
	for _, want := range []string{"edit app/posts/index.templ,", "edit app/models/posts.sql,", "2 stale"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in\n%s", want, out)
		}
	}
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}

	for file, stamp := range map[string]*regexp.Regexp{"app/posts/index_templ.go": templStamp, "app/models/posts.sql.go": sqlcStamp} {
		b, _ := os.ReadFile(filepath.Join(dir, file))
		m := stamp.FindSubmatchIndex(b)
		if m == nil {
			t.Fatalf("%s has no version stamp:\n%s", file, b)
		}
		os.WriteFile(filepath.Join(dir, file), append(append(append([]byte{}, b[:m[2]]...), "v0.0.1"...), b[m[3]:]...), 0o644)
	}
	os.Remove(filepath.Join(dir, "app/posts/form.templ"))
	_, out, _ = gantry(t, dir, "check", "--generated")
	for _, want := range []string{
		"app/posts/index_templ.go was made by templ v0.0.1, and the templ here is v",
		"app/models/posts.sql.go was made by sqlc v0.0.1, and the sqlc here is v",
		"app/posts/form_templ.go isn't made by templ generate any more: delete it, or restore app/posts/form.templ",
		"3 stale",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in\n%s", want, out)
		}
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
