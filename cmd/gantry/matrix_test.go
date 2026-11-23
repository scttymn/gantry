package main

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/devcheck"
	"github.com/scttymn/gantry/images"
	"github.com/scttymn/gantry/internal/testenv"
	"github.com/scttymn/gantry/testkit"
)

// matrixRow is a generator, run in an app: a row of the generator matrix.
type matrixRow struct {
	args []string // gantry's
	// then does what its next steps leave to a person, as one would, for
	// the app to use it; nil when the app builds without.
	then func(t *testing.T, dir, out string)
}

// matrixApps are the apps the matrix's rows run in, each row in the app the
// rows before it left: generators that compose share one, and those that
// can't (an app has one auth recipe) have their own. Each starts as a new
// app; the first one's is a row too.
var matrixApps = []struct {
	name string
	rows []matrixRow
}{
	{"shop", []matrixRow{
		{args: []string{"g", "resource", "posts", "title:string", "position:position", "published_on:date"}, then: routed(`
		{path: "/posts", create: map[string]string{"post[title]": "First", "post[published_on]": "2026-11-22"}, update: map[string]string{"post[title]": "Second"},
			listed: []string{"First", "2026-11-22"}, editing: map[string]string{"post[position]": "1", "post[published_on]": "2026-11-22"}},`)},
		{args: []string{"g", "resource", "admin/events", "name:string:required", "starts_on:date", "position:position"}, then: routed(`
		{path: "/admin/events", create: map[string]string{"event[name]": "Open day", "event[starts_on]": "2026-12-01"}, update: map[string]string{"event[name]": "Open house"},
			listed: []string{"Open day", "2026-12-01"}, editing: map[string]string{"event[position]": "1", "event[starts_on]": "2026-12-01"}, blank: map[string]string{"event[name]": " "}},`)},
		// Built, not routed: its Photos is App's Storage, which a new app hasn't.
		{args: []string{"g", "resource", "admin/galleries", "caption:string", "photo:photo"}},
		// Its package, resources, imported beside the helpers' resource.
		{args: []string{"g", "resource", "resources", "name:string", "position:position"}, then: routed(`
		{path: "/resources", create: map[string]string{"resource[name]": "Guide"}, update: map[string]string{"resource[name]": "Handbook"}, listed: []string{"Guide"}, editing: map[string]string{"resource[position]": "1"}},`)},
		{args: []string{"g", "migration", "create_notes", "body:text:required", "seen_at:datetime", "done:bool"}},
		{args: []string{"g", "api-tokens", "--prefix", "shop_"}},
		{args: []string{"g", "storage"}},
		{args: []string{"g", "error-pages", "--force"}},
		{args: []string{"g", "fonts", "Work Sans:400,600"}, then: func(t *testing.T, dir, _ string) {
			edit(t, filepath.Join(dir, "assets", "assets.go"),
				"//go:embed css js public all:built", "//go:embed css fonts js public all:built",
				`var Styles = All.Styles("application.css")`, `var Styles = All.Styles("fonts.css", "application.css").Preload(gantry.Face{Family: "Work Sans"})`)
		}},
		{args: []string{"g", "auth"}, then: func(t *testing.T, dir, _ string) {
			edit(t, filepath.Join(dir, "app", "routes.go"),
				"\t\"net/http\"\n", "\t\"net/http\"\n\t\"path/filepath\"\n",
				"\t\"example.com/shop/app/home\"\n", "\t\"example.com/shop/app/auth\"\n\t\"example.com/shop/app/home\"\n\t\"example.com/shop/config\"\n",
				"\thomePage :=", "\tlogin := auth.New(a.DB, a.Signer, a.Mail, a.Mail.From, filepath.Join(config.FromEnv().DataDir, \"initial-account-code\"))\n\tlogin.Routes(rt)\n\n\thomePage :=")
		}},
		{args: []string{"g", "auth", "--social"}},
	}},
	{"codes", []matrixRow{
		{args: []string{"g", "auth", "--email-code"}},
	}},
	{"signup", []matrixRow{
		{args: []string{"g", "auth", "--email-code", "--signup"}},
		{args: []string{"g", "auth", "--social"}},
	}},
}

// Every generator, on each engine, in a new app as people make one: each
// row's output, with its next steps done, passes Strict, generates its
// queries (sqlc) and views (templ), vets clean, passes the app's tests, and
// check --generated finds it fresh, against this checkout. The apps run in
// parallel, their rows in turn. Without templ and sqlc (bin/go has them) or
// with -short, the rows are generated and scanned only.
func TestGeneratorMatrix(t *testing.T) {
	repo, _ := filepath.Abs("../..")
	build := !testing.Short()
	for _, tool := range []string{"templ", "sqlc"} {
		if _, err := exec.LookPath(tool); err != nil {
			build = false
		}
	}
	if url := os.Getenv("GANTRY_TEST_POSTGRES_URL"); url != "" {
		t.Setenv("TEST_DATABASE_URL", url) // the apps' tests', testkit.Postgres's
	}
	fakeGoogle(t)

	// Generators read now and httpClient, which the apps share: one runs at
	// a time.
	var generating sync.Mutex
	gantryAt := func(t *testing.T, dir string, when time.Time, args ...string) string {
		t.Helper()
		generating.Lock()
		defer generating.Unlock()
		was := now
		now = func() time.Time { return when }
		defer func() { now = was }()
		code, out, stderr := gantry(t, dir, args...)
		if code != 0 {
			t.Fatalf("gantry %s: exit %d: %s", strings.Join(args, " "), code, stderr)
		}
		return out
	}
	start := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

	for _, engine := range []string{"sqlite", "postgres"} {
		for i, a := range matrixApps {
			t.Run(engine+"/"+a.name, func(t *testing.T) {
				t.Parallel()
				root := t.TempDir()
				gantryAt(t, root, start, "new", "shop", "--db", engine, "--module", "example.com/shop", "--skip-houston", "--gantry", repo)
				dir := filepath.Join(root, "shop")
				// This checkout, not the release go.mod requires: go.work alone
				// still reads that release's go.mod, which isn't published until
				// it's tagged.
				mod, _ := os.ReadFile(filepath.Join(dir, "go.mod"))
				os.WriteFile(filepath.Join(dir, "go.mod"), append(mod, "\nreplace github.com/scttymn/gantry => "+repo+"\n"...), 0o644)

				cell := func(name string, generate, after func(t *testing.T)) bool {
					return t.Run(name, func(t *testing.T) {
						generate(t)
						warnings, err := devcheck.Scan(dir)
						if err != nil {
							t.Fatal(err)
						}
						for _, w := range warnings {
							t.Errorf("%s (%s): Strict %s at %s:%d: %s; fix its template in cmd/gantry/templates", name, engine, w.Rule, w.File, w.Line, w.Message)
						}
						if !build {
							t.Skip("generated and scanned only: templ or sqlc isn't installed (bin/go has them), or -short")
						}
						if engine == "postgres" {
							testenv.Postgres(t)
						}
						buildApp(t, dir, engine)
						if after != nil {
							after(t)
						}
					})
				}
				if i == 0 && !cell("new", func(*testing.T) {}, func(t *testing.T) { pictureChild(t, dir) }) {
					return
				}
				for n, row := range a.rows {
					name := rowName(row.args)
					ok := cell(name, func(t *testing.T) {
						out := gantryAt(t, dir, start.Add(time.Duration(n+1)*time.Minute), row.args...)
						if row.then != nil {
							row.then(t, dir, out)
						}
					}, nil)
					if !ok {
						return // the rows after it build on it
					}
				}
			})
		}
	}
}

// rowName is a row's command, without its fields: "g resource posts".
func rowName(args []string) string {
	for i, a := range args {
		if strings.Contains(a, ":") {
			return strings.Join(args[:i], " ")
		}
	}
	return strings.Join(args, " ")
}

// buildApp is a cell's build, as gantry new's steps and the generators' next
// steps run it: the schema the migrations leave, sqlc's queries and templ's
// views, then go vet and the app's tests; and check --generated agrees.
func buildApp(t *testing.T, dir, engine string) {
	t.Helper()
	migrate := func(ctx context.Context, d *db.DB) error {
		return d.Migrate(ctx, os.DirFS(filepath.Join(dir, "db", "migrations")), "app_migrations")
	}
	var baseline *db.DB
	if engine == "postgres" {
		baseline = testkit.Postgres(t, migrate)
	} else {
		baseline = testkit.DB(t, migrate)
	}
	schema, err := baseline.Schema(t.Context(), "app_migrations")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "db", "schema.sql"), []byte(schema), 0o644); err != nil {
		t.Fatal(err)
	}
	var steps [][]string
	if hasQueries(dir) {
		steps = append(steps, []string{"sqlc", "generate"})
	}
	steps = append(steps, []string{"templ", "generate"}, []string{"go", "mod", "tidy"}, []string{"go", "vet", "./..."}, []string{"go", "test", "./..."})
	for _, step := range steps {
		cmd := exec.Command(step[0], step[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", strings.Join(step, " "), err, out)
		}
	}
	var out strings.Builder
	if err := generatedCheck(dir, &out); err != nil {
		t.Errorf("check --generated: %v\n%s", err, out.String())
	}
}

// pictureChild checks a new app's binary is its pictures' resizing child
// (images.Child), with the app's pipeline and so its pixel limit.
func pictureChild(t *testing.T, dir string) {
	t.Helper()
	for _, step := range [][]string{{"go", "run", "./cmd/shop", "assets"}, {"go", "build", "-o", "shop", "./cmd/shop"}} {
		cmd := exec.Command(step[0], step[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", strings.Join(step, " "), err, out)
		}
	}
	defer os.Remove(filepath.Join(dir, "shop"))
	var jpg bytes.Buffer
	jpeg.Encode(&jpg, image.NewRGBA(image.Rect(0, 0, 400, 200)), nil)
	scratch := t.TempDir()
	os.WriteFile(filepath.Join(scratch, "photo.jpg"), jpg.Bytes(), 0o644)
	os.WriteFile(filepath.Join(scratch, "bomb.gif"), []byte("GIF89a\xff\xff\xff\xff\x00\x00\x00"), 0o644)
	resize := func(src, dst string) (string, error) {
		cmd := exec.Command(filepath.Join(dir, "shop"), images.ChildCommand, src, dst, "100", "80")
		cmd.Dir = scratch
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := resize("photo.jpg", "copy.webp"); err != nil {
		t.Fatalf("resize: %v\n%s", err, out)
	}
	data, _ := os.ReadFile(filepath.Join(scratch, "copy.webp"))
	if w, h, ct, err := (&images.Pipeline{}).Dimensions(data); err != nil || w != 100 || h != 50 || ct != "image/webp" {
		t.Errorf("the child's copy: %d×%d %s %v", w, h, ct, err)
	}
	if out, err := resize("bomb.gif", "bomb.webp"); err == nil || !strings.Contains(out, "too many pixels") {
		t.Errorf("the child resized a bomb: %v\n%s", err, out)
	}
}

// edit replaces, in the file at path, each old with its new, as a person
// following next steps would; each old must be there.
func edit(t *testing.T, path string, oldNew ...string) {
	t.Helper()
	b, _ := os.ReadFile(path)
	src := string(b)
	for i := 0; i < len(oldNew); i += 2 {
		if !strings.Contains(src, oldNew[i]) {
			t.Fatalf("no %q in %s:\n%s", oldNew[i], path, src)
		}
		src = strings.Replace(src, oldNew[i], oldNew[i+1], 1)
	}
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

// routed is a resource's then: wired in as g resource's next steps say, and
// its pages used through their routes by the app's own test (resourcesTest,
// with testCase, as Go, among its cases), so its queries run on the engine.
func routed(testCase string) func(t *testing.T, dir, out string) {
	return func(t *testing.T, dir, out string) {
		t.Helper()
		wire(t, dir, out)
		path := filepath.Join(dir, "app", "resources_test.go")
		if _, err := os.Stat(path); err != nil {
			os.WriteFile(path, []byte(resourcesTest), 0o644)
		}
		edit(t, path, "\t\t// cases\n", testCase[1:]+"\n\t\t// cases\n")
	}
}

// wire does in app/routes.go what g resource's next steps say: their
// imports (each once, as a person adds one), and their code, in Handler.
func wire(t *testing.T, dir, out string) {
	t.Helper()
	_, next, ok := strings.Cut(out, "\nNext:\n")
	step, _, _ := strings.Cut(next, "\n  2. ")
	if !ok || !strings.HasPrefix(step, "  1. In app/routes.go") {
		t.Fatalf("no step 1 in\n%s", out)
	}
	path := filepath.Join(dir, "app", "routes.go")
	src, _ := os.ReadFile(path)
	routes := string(src)
	var code []string
	for _, line := range strings.Split(step, "\n") {
		if !strings.HasPrefix(line, "       ") {
			continue
		}
		if l := strings.TrimSpace(line); strings.HasPrefix(l, `"`) {
			if !strings.Contains(routes, "\t"+l+"\n") {
				routes = strings.Replace(routes, "\t\"example.com/shop/app/home\"\n", "\t\"example.com/shop/app/home\"\n\t"+l+"\n", 1)
			}
			continue
		}
		code = append(code, line)
	}
	if len(code) == 0 || !strings.Contains(routes, "\thomePage :=") {
		t.Fatalf("nothing to wire, or nowhere to wire it:\n%s", step)
	}
	routes = strings.Replace(routes, "\thomePage :=", strings.Join(code, "\n")+"\n\thomePage :=", 1)
	if err := os.WriteFile(path, []byte(routes), 0o644); err != nil {
		t.Fatal(err)
	}
}

const resourcesTest = `package app_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/scttymn/gantry/testkit"
)

// Each resource's pages, as a person uses them, so its queries run.
func TestResources(t *testing.T) {
	for _, tc := range []struct {
		path           string
		create, update map[string]string
		listed         []string          // the new record, in the list
		editing        map[string]string // its edit form's values
		blank          map[string]string // a form missing what's required
	}{
		// cases
	} {
		t.Run(tc.path, func(t *testing.T) {
			b := testkit.Browser(t, handler(t))
			if list := b.Get(tc.path); list.Code != http.StatusOK {
				t.Fatalf("GET %s = %d\n%s", tc.path, list.Code, list.Body)
			}
			if tc.blank != nil {
				sent := b.Get(tc.path+"/new").Submit("form", tc.blank)
				if sent.Code != http.StatusUnprocessableEntity || !strings.Contains(sent.Text("[role=alert]"), "can't be blank") {
					t.Fatalf("a blank form = %d\n%s", sent.Code, sent.Body)
				}
			}
			created := b.Get(tc.path+"/new").Submit("form", tc.create)
			if created.Code != http.StatusSeeOther {
				t.Fatalf("create = %d\n%s", created.Code, created.Body)
			}
			list := created.Follow()
			for _, want := range append([]string{"was successfully created."}, tc.listed...) {
				if !strings.Contains(list.Text("main"), want) {
					t.Errorf("no %q in the list after creating\n%s", want, list.Body)
				}
			}
			edit := b.Get(list.Find("a[href$='/edit']").AttrOr("href", ""))
			if edit.Code != http.StatusOK {
				t.Fatalf("edit = %d\n%s", edit.Code, edit.Body)
			}
			for name, want := range tc.editing {
				if got := edit.Find("[name='" + name + "']").AttrOr("value", ""); got != want {
					t.Errorf("%s is %q on the edit form, want %q", name, got, want)
				}
			}
			updated := edit.Submit("form:not(.delete)", tc.update)
			if updated.Code != http.StatusSeeOther {
				t.Fatalf("update = %d\n%s", updated.Code, updated.Body)
			}
			list = updated.Follow()
			if !strings.Contains(list.Text("main"), "was successfully updated.") {
				t.Errorf("no notice in the list after updating\n%s", list.Body)
			}
			for _, want := range tc.update {
				if !strings.Contains(list.Text("main"), want) {
					t.Errorf("no %q in the list after updating\n%s", want, list.Body)
				}
			}
			deleted := b.Get(edit.URL.Path).Submit("form.delete", nil)
			if deleted.Code != http.StatusSeeOther {
				t.Fatalf("delete = %d\n%s", deleted.Code, deleted.Body)
			}
			list = deleted.Follow()
			if !strings.Contains(list.Text("main"), "was successfully destroyed.") || list.Find("a[href$='/edit']").Length() != 0 {
				t.Errorf("the list after deleting:\n%s", list.Body)
			}
		})
	}
}
`
