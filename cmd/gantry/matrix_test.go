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
	"github.com/scttymn/gantry/internal/pins"
	"github.com/scttymn/gantry/internal/testenv"
	"github.com/scttymn/gantry/testkit"
)

type matrixRow struct {
	args []string
	then func(t *testing.T, dir, out string)
}

var matrixApps = []struct {
	name     string
	inModule bool
	rows     []matrixRow
}{
	{name: "shop", rows: []matrixRow{
		{args: []string{"g", "resource", "posts", "title:string", "position:position", "published_on:date"}, then: routed(`
		{path: "/posts", create: map[string]string{"post[title]": "First", "post[published_on]": "2026-11-22"}, update: map[string]string{"post[title]": "Second"},
			listed: []string{"First", "2026-11-22"}, editing: map[string]string{"post[position]": "1", "post[published_on]": "2026-11-22"}},`)},
		{args: []string{"g", "resource", "admin/events", "name:string:required", "starts_on:date", "position:position"}, then: routed(`
		{path: "/admin/events", create: map[string]string{"event[name]": "Open day", "event[starts_on]": "2026-12-01"}, update: map[string]string{"event[name]": "Open house"},
			listed: []string{"Open day", "2026-12-01"}, editing: map[string]string{"event[position]": "1", "event[starts_on]": "2026-12-01"}, blank: map[string]string{"event[name]": " "}},`)},
		{args: []string{"g", "resource", "admin/galleries", "caption:string", "photo:photo"}},
		{args: []string{"g", "resource", "resources", "name:string", "position:position"}, then: func(t *testing.T, dir, out string) {
			routed(`
		{path: "/resources", create: map[string]string{"resource[name]": "Guide"}, update: map[string]string{"resource[name]": "Handbook"}, listed: []string{"Guide"}, editing: map[string]string{"resource[position]": "1"}},`)(t, dir, out)
			sharedFormPageFromBeforeHiddenFields(t, dir)
		}},
		{args: []string{"g", "migration", "create_notes", "body:text:required", "seen_at:datetime", "done:bool"}},
		{args: []string{"g", "api-tokens", "--prefix", "shop_"}},
		{args: []string{"g", "storage"}},
		{args: []string{"g", "error-pages", "--force"}},
		{args: []string{"g", "fonts", "Work Sans:400,600"}, then: func(t *testing.T, dir, _ string) {
			edit(t, filepath.Join(dir, "assets", "assets.go"),
				"//go:embed css js public all:built", "//go:embed css fonts js public all:built",
				`var Styles = All.Styles("application.css")`, `var Styles = All.Styles("fonts.css", "application.css").Preload(gantry.Face{Family: "Work Sans"})`)
		}},
		{args: []string{"g", "auth"}, then: func(t *testing.T, dir, out string) {
			feature(t, dir, out)
			edit(t, filepath.Join(dir, "app", "routes.go"),
				"\t\"net/http\"\n", "\t\"net/http\"\n\t\"path/filepath\"\n",
				"\t\"example.com/shop/app/home\"\n", "\t\"example.com/shop/app/auth\"\n\t\"example.com/shop/app/home\"\n\t\"example.com/shop/config\"\n",
				"\thomePage :=", "\tlogin := auth.New(a.DB, a.Signer, a.Mail, a.Mail.From, filepath.Join(config.FromEnv().DataDir, \"initial-account-code\"))\n\tlogin.Routes(rt)\n\n\thomePage :=")
		}},
		{args: []string{"g", "auth", "--social"}},
	}},
	{name: "codes", rows: []matrixRow{
		{args: []string{"g", "auth", "--email-code"}},
	}},
	{name: "signup", rows: []matrixRow{
		{args: []string{"g", "auth", "--email-code", "--signup"}},
		{args: []string{"g", "auth", "--social"}},
	}},
	{name: "in-module", inModule: true, rows: []matrixRow{
		{args: []string{"g", "api-tokens", "--prefix", "control_"}},
		{args: []string{"g", "storage"}},
	}},
}

func TestGeneratorMatrix(t *testing.T) {
	repo, _ := filepath.Abs("../..")
	build := !testing.Short()
	for _, tool := range []string{"templ", "sqlc"} {
		if _, err := exec.LookPath(tool); err != nil {
			build = false
		}
	}
	if build {
		v, _ := exec.Command("templ", "version").Output()
		if have := strings.TrimSpace(string(v)); have != pins.Templ {
			t.Fatalf("the templ on PATH is %q, and gantry builds with %s (internal/pins): run the tests in bin/go, whose toolchain installs it", have, pins.Templ)
		}
	}
	if url := os.Getenv("GANTRY_TEST_POSTGRES_URL"); url != "" {
		t.Setenv("TEST_DATABASE_URL", url)
	}
	fakeGoogle(t)

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
				var dir string
				if a.inModule {
					root := big(t, repo)
					gantryAt(t, filepath.Join(root, "apps"), start, "new", "control", "--db", engine, "--in-module", "--skip-houston")
					dir = filepath.Join(root, "apps", "control")
				} else {
					root := t.TempDir()
					gantryAt(t, root, start, "new", "shop", "--db", engine, "--module", "example.com/shop", "--skip-houston", "--gantry", repo)
					dir = filepath.Join(root, "shop")
					mod, _ := os.ReadFile(filepath.Join(dir, "go.mod"))
					os.WriteFile(filepath.Join(dir, "go.mod"), append(mod, "\nreplace github.com/scttymn/gantry => "+repo+"\n"...), 0o644)
				}

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
						return
					}
				}
			})
		}
	}
}

func rowName(args []string) string {
	for i, a := range args {
		if strings.Contains(a, ":") {
			return strings.Join(args[:i], " ")
		}
	}
	return strings.Join(args, " ")
}

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

func sharedFormPageFromBeforeHiddenFields(t *testing.T, dir string) {
	t.Helper()
	edit(t, filepath.Join(dir, "app", "shared", "resource", "pages.templ"), `if field.Type == "hidden" {`, `if false {`)
}

func routed(testCase string) func(t *testing.T, dir, out string) {
	return func(t *testing.T, dir, out string) {
		t.Helper()
		wire(t, dir, out)
		path := filepath.Join(dir, "app", "resources_test.go")
		if _, err := os.Stat(path); err != nil {
			os.WriteFile(path, []byte(resourcesTest), 0o644)
		}
		edit(t, path, "\t}{\n", "\t}{\n"+testCase[1:]+"\n")
	}
}

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
	feature(t, dir, out)
}

func feature(t *testing.T, dir, out string) {
	t.Helper()
	_, step, ok := strings.Cut(out, "In test/features.yaml")
	_, yaml, found := strings.Cut(step, "\n       - name:")
	if !ok || !found {
		t.Fatalf("no feature in\n%s", out)
	}
	lines := strings.Split(yaml, "\n")
	add := "  - name:" + lines[0] + "\n"
	for _, line := range lines[1:] {
		if !strings.HasPrefix(line, "         ") {
			break
		}
		add += line[5:] + "\n"
	}
	path := filepath.Join(dir, "test", "features.yaml")
	features, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(features, add...), 0o644); err != nil {
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

func TestResources(t *testing.T) {
	for _, tc := range []struct {
		path           string
		create, update map[string]string
		listed         []string
		editing        map[string]string
		blank          map[string]string
	}{
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
			const version = "form:not(.delete) input[type=hidden][name$='[lock_version]']"
			if got := edit.Find(version).AttrOr("value", ""); got != "0" {
				t.Errorf("lock_version is %q on a new row's edit form, want 0\n%s", got, edit.Body)
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
			mine := map[string]string{}
			for name, value := range tc.update {
				mine[name] = "Stale " + value
			}
			stale := edit.Submit("form:not(.delete)", mine)
			if stale.Code != http.StatusConflict || !strings.Contains(stale.Text("[role=alert]"), "Someone else changed this record while you were editing. Review the current values and submit again.") {
				t.Fatalf("a form from before the update = %d\n%s", stale.Code, stale.Body)
			}
			for name, want := range mine {
				if got := stale.Find("[name='" + name + "']").AttrOr("value", ""); got != want {
					t.Errorf("%s is %q on the conflict's form, want what was sent, %q", name, got, want)
				}
			}
			if got := stale.Find(version).AttrOr("value", ""); got != "1" {
				t.Errorf("lock_version is %q on the conflict's form, want the row's, 1\n%s", got, stale.Body)
			}
			list = b.Get(tc.path)
			for name, want := range tc.update {
				if strings.Contains(list.Text("main"), mine[name]) || !strings.Contains(list.Text("main"), want) {
					t.Errorf("the stale form overwrote %q with %q\n%s", want, mine[name], list.Body)
				}
			}
			again := stale.Submit("form:not(.delete)", nil)
			if again.Code != http.StatusSeeOther {
				t.Fatalf("submitting the conflict's form again = %d\n%s", again.Code, again.Body)
			}
			list = again.Follow()
			for _, want := range mine {
				if !strings.Contains(list.Text("main"), want) {
					t.Errorf("no %q in the list after submitting again\n%s", want, list.Body)
				}
			}
			if got := b.Get(edit.URL.Path).Find(version).AttrOr("value", ""); got != "2" {
				t.Errorf("lock_version is %q after two saves, want 2", got)
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
