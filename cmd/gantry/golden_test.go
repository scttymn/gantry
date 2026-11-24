package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/scttymn/gantry/devcheck"
)

// goldenCase is a generator's output, pinned: what its command writes or
// changes, in an app the commands before it made.
type goldenCase struct {
	golden string                    // its folder in testdata/golden
	app    func(t *testing.T) string // the folder it runs in
	before [][]string                // gantry's arguments, run first and not pinned
	args   []string                  // gantry's arguments: the command pinned
	next   string                    // what its next steps must say
	// split is the folder a file is pinned in instead of golden, one that
	// cases share, and whether it is.
	split func(rel string) (string, bool)
}

// gymPostgres is app's bare module, on Postgres.
func gymPostgres(t *testing.T) string {
	root := app(t)
	os.WriteFile(filepath.Join(root, "sqlc.yaml"), []byte("version: \"2\"\nsql:\n  - engine: postgresql\n"), 0o644)
	return root
}

// resourceShared pins the helpers in app/shared, the same for every
// resource, once.
func resourceShared(rel string) (string, bool) {
	return "resource-shared", strings.HasPrefix(rel, "app/shared/")
}

func goldenCases() []goldenCase {
	resource := func(golden string, app func(*testing.T) string, args ...string) goldenCase {
		return goldenCase{golden: golden, app: app, args: append([]string{"g", "resource"}, args...), split: resourceShared}
	}
	cases := []goldenCase{
		resource("pillars", app, "admin/pillars", "title:string:required", "body:string", "position:position"),
		resource("programs", app, "admin/programs", "name:string:required", "blurb:text", "photo:photo", "cta:string", "position:position"),
		resource("workouts", app, "admin/workouts", "date:date:required", "name:string", "rx:text"),
		resource("posts", app, "posts", "title:string:required", "body:text", "published_on:date"),
		resource("events-postgres", gymPostgres, "editor/events", "name:string:required", "starts_on:date", "position:position"),
		{golden: "new-in-module", app: func(t *testing.T) string { return filepath.Join(big(t, ""), "apps") }, args: []string{"new", "control", "--in-module", "--skip-houston"}},
	}
	for _, engine := range []string{"sqlite", "postgres"} {
		shop := func(t *testing.T) string { return shopApp(t, engine) }
		cases = append(cases,
			goldenCase{golden: "new-" + engine, app: func(t *testing.T) string { return t.TempDir() }, args: []string{"new", "shop", "--db", engine, "--module", "example.com/shop", "--skip-houston"}},
			goldenCase{golden: "api-tokens-" + engine, app: shop, args: []string{"g", "api-tokens", "--prefix", "shop_"}, next: "rt.Scope("},
			goldenCase{golden: "auth-" + engine, app: shop, args: []string{"g", "auth"}, next: "rt.Scope("},
			goldenCase{golden: "auth-email-code-" + engine, app: shop, args: []string{"g", "auth", "--email-code"}, next: "rt.Scope("},
			goldenCase{golden: "auth-email-code-signup-" + engine, app: shop, args: []string{"g", "auth", "--email-code", "--signup"}, next: "rt.Scope("},
			goldenCase{golden: "auth-social-password-" + engine, app: shop, before: [][]string{{"g", "auth"}}, args: []string{"g", "auth", "--social"}},
			goldenCase{golden: "auth-social-email-code-" + engine, app: shop, before: [][]string{{"g", "auth", "--email-code", "--signup"}}, args: []string{"g", "auth", "--social"}},
			goldenCase{golden: "storage-" + engine, app: shop, args: []string{"g", "storage"}, next: "rt.Mount("},
		)
	}
	return cases
}

// What every generator writes is pinned, on each engine: a change to it is a
// change to every app made or generated into next, so it shows up here, and
// in review. Every template is rendered by a case, so none changes unseen,
// and what each case writes passes Strict.
func TestGolden(t *testing.T) {
	opened := &openedFS{FS: templates, names: map[string]bool{}}
	templates = opened
	t.Cleanup(func() { templates = opened.FS })

	cases := goldenCases()
	ran := 0
	for _, c := range cases {
		t.Run(c.golden, func(t *testing.T) {
			ran++
			at(t, "2026-09-29T12:00:00Z")
			dir := c.app(t)
			for _, args := range c.before {
				if code, _, stderr := gantry(t, dir, args...); code != 0 {
					t.Fatalf("gantry %s: exit %d: %s", strings.Join(args, " "), code, stderr)
				}
			}
			before := readTree(t, dir)
			code, out, stderr := gantry(t, dir, c.args...)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			if !strings.Contains(out, c.next) {
				t.Errorf("its next steps say no %s:\n%s", c.next, out)
			}
			if c.args[0] == "new" {
				dir = filepath.Join(dir, c.args[1])
			}
			got := map[string][]byte{}
			for rel, b := range readTree(t, dir) {
				if was, ok := before[rel]; !ok || !bytes.Equal(was, b) {
					got[rel] = b
				}
			}
			// Random keys, checked by TestNewEnv.
			delete(got, ".env")
			for rel, b := range got {
				if strings.HasPrefix(rel, "assets/js/vendor/") {
					// Vendored JavaScript is copied as it is, not kept twice.
					if want, err := fs.ReadFile(templates, "templates/new/"+rel); err != nil || !bytes.Equal(want, b) {
						t.Errorf("%s isn't gantry's copy", rel)
					}
					delete(got, rel)
				}
			}
			if c.split != nil {
				shared := map[string][]byte{}
				var golden string
				for rel, b := range got {
					if g, ok := c.split(rel); ok {
						shared[rel], golden = b, g
						delete(got, rel)
					}
				}
				if len(shared) > 0 {
					compareTree(t, shared, filepath.Join("testdata", "golden", golden))
				}
			}
			compareTree(t, got, filepath.Join("testdata", "golden", c.golden))
			// Scanned where it's written: devcheck skips testdata, and a
			// .golden file matches no rule's extension.
			warnings, err := devcheck.Scan(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range warnings {
				t.Errorf("Strict %s at %s:%d: %s; fix its template in cmd/gantry/templates", w.Rule, w.File, w.Line, w.Message)
			}
		})
	}
	if ran < len(cases) {
		return // -run chose some cases: the rest render the other templates
	}
	fs.WalkDir(opened.FS, "templates", func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && !opened.names[path] {
			t.Errorf("%s is never rendered by a golden case: add one to goldenCases (golden_test.go), or delete it", path)
		}
		return err
	})
}

// openedFS records the files read from FS.
type openedFS struct {
	fs.FS
	names map[string]bool
}

func (o *openedFS) Open(name string) (fs.File, error) {
	f, err := o.FS.Open(name)
	if err == nil {
		if info, err := f.Stat(); err == nil && !info.IsDir() {
			o.names[name] = true
		}
	}
	return f, err
}

// readTree is the files under dir, by their slash path.
func readTree(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		rel, _ := filepath.Rel(dir, path)
		files[filepath.ToSlash(rel)] = b
		return err
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return files
}

// compareTree checks got, generated files by their slash path, against
// golden, which pins each as <path>.golden: a file that differs, a file with
// no golden, and a golden no file has any more each fail. With -update it
// rewrites golden from got instead.
func compareTree(t *testing.T, got map[string][]byte, golden string) {
	t.Helper()
	var names []string
	for rel, b := range got {
		gofmted(t, rel, b)
		names = append(names, rel)
	}
	sort.Strings(names)
	if *update {
		os.RemoveAll(golden)
		for _, rel := range names {
			path := filepath.Join(golden, filepath.FromSlash(rel)+".golden")
			os.MkdirAll(filepath.Dir(path), 0o755)
			if err := os.WriteFile(path, got[rel], 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	want := readTree(t, golden)
	for _, rel := range names {
		path := filepath.Join(golden, filepath.FromSlash(rel)+".golden")
		w, ok := want[rel+".golden"]
		switch {
		case !ok:
			t.Errorf("%s is written, and there's no %s (go test ./cmd/gantry -update writes it; review it)", rel, path)
		case !bytes.Equal(w, got[rel]):
			t.Errorf("%s differs from %s (go test ./cmd/gantry -update rewrites it; review the diff)", rel, path)
		}
	}
	for rel := range want {
		if _, ok := got[strings.TrimSuffix(rel, ".golden")]; !ok || !strings.HasSuffix(rel, ".golden") {
			t.Errorf("%s pins nothing written: delete it (go test ./cmd/gantry -update does)", filepath.Join(golden, filepath.FromSlash(rel)))
		}
	}
}
