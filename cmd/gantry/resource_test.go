package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/devcheck"
	"github.com/scttymn/gantry/internal/testenv"
	"github.com/scttymn/gantry/testkit"
)

// A resource is top-level or under one namespace, any namespace: its
// folder, its address and the route its next steps give follow from that.
func TestResourcePaths(t *testing.T) {
	for _, tc := range []struct {
		name, controller, base, route string
	}{
		{"posts", "app/posts/controller.go", `path     = "/posts"`, `rt.Resources("/posts", posts.Controller{`},
		{"admin/posts", "app/admin/posts/controller.go", `path     = "/admin/posts"`, `s.Resources("/posts", posts.Controller{`},
		{"editor/blog_posts", "app/editor/blogposts/controller.go", `path     = "/editor/blog_posts"`, `rt.Scope("/editor", web.Pipeline{}, func(s *web.Scope) {`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := app(t)
			out := generate(t, root, tc.name, "title:string")
			code, err := os.ReadFile(filepath.Join(root, tc.controller))
			if err != nil || !strings.Contains(string(code), tc.base) {
				t.Errorf("%s: %v\n%s", tc.controller, err, code)
			}
			for _, want := range []string{tc.route, `"example.com/gym/` + filepath.Dir(tc.controller) + `"`, `"example.com/gym/app/shared/resource"`} {
				if !strings.Contains(out, want) {
					t.Errorf("no %q in\n%s", want, out)
				}
			}
		})
	}
	t.Run("the app's own folders aren't a resource's, and nothing is written", func(t *testing.T) {
		for _, name := range []string{"models", "shared", "mod_els", "models/posts", "shared/posts", "Admin/posts", "/posts", "admin/blog/posts"} {
			root := app(t)
			code, _, stderr := gantry(t, root, "g", "resource", name, "title:string")
			if code == 0 {
				t.Errorf("%s: made", name)
			}
			if strings.HasPrefix(name, "models") || strings.HasPrefix(name, "shared") || name == "mod_els" {
				if !strings.Contains(stderr, "the app's own") {
					t.Errorf("%s: %s", name, stderr)
				}
			}
			if _, err := os.Stat(filepath.Join(root, "app")); err == nil {
				t.Errorf("%s: wrote files", name)
			}
		}
	})
}

// A namespace with code of its own (an app/admin package, with the
// admin's layout) isn't what a resource there uses: the next steps say so,
// and how to give its pages that look. Resources the generator wrote there
// before aren't such code.
func TestResourceInNamespaceWithItsOwnCode(t *testing.T) {
	const note = "app/admin has code of its own"
	root := app(t)
	out := generate(t, root, "admin/events", "name:string")
	if strings.Contains(out, note) {
		t.Errorf("a namespace of generated resources only:\n%s", out)
	}
	out = generate(t, root, "admin/posts", "title:string")
	if strings.Contains(out, note) {
		t.Errorf("a namespace of generated resources only:\n%s", out)
	}
	os.WriteFile(filepath.Join(root, "app", "admin", "layout.go"), []byte("package admin\n"), 0o644)
	out = generate(t, root, "admin/staff", "name:string")
	for _, want := range []string{note, "app/shared/resource", "app/admin/staff/index.templ and form.templ"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in\n%s", want, out)
		}
	}
	if out := generate(t, root, "pages", "title:string"); strings.Contains(out, "code of its own") {
		t.Errorf("a top-level resource:\n%s", out)
	}
}

// The helpers every resource uses are the app's once written: the next
// resource keeps them as they are, changed or not.
func TestResourceHelpersKept(t *testing.T) {
	root := app(t)
	out := generate(t, root, "posts", "title:string")
	helpers := []string{"app/shared/resource/resource.go", "app/shared/resource/pages.templ"}
	for _, h := range helpers {
		if !strings.Contains(out, "wrote "+h) {
			t.Errorf("no wrote %s in\n%s", h, out)
		}
	}
	changed := filepath.Join(root, helpers[0])
	if err := os.WriteFile(changed, []byte("package resource // the app's now\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out = generate(t, root, "admin/events", "name:string")
	for _, h := range helpers {
		if !strings.Contains(out, "kept "+h) {
			t.Errorf("no kept %s in\n%s", h, out)
		}
	}
	if b, _ := os.ReadFile(changed); string(b) != "package resource // the app's now\n" {
		t.Errorf("written over:\n%s", b)
	}
}

// A new app's resources, wired in as g resource's next steps say, pass
// Strict, build, and work: the app's own test lists, creates, edits and
// deletes through their routes, so their queries run on its engine. A photo
// resource is built, not routed: its Photos is App's Storage, which a new
// app hasn't.
func TestResourceBuilds(t *testing.T) {
	for _, tc := range []struct {
		engine    string
		resources [][]string
		cases     string // the app test's, as Go
	}{
		{"sqlite", [][]string{
			{"posts", "title:string"},
			{"admin/events", "name:string:required", "position:position", "starts_on:date"},
			{"admin/galleries", "caption:string", "photo:photo"},
			{"resources", "name:string", "position:position"},
		}, `
		{path: "/posts", create: map[string]string{"post[title]": "First"}, update: map[string]string{"post[title]": "Second"}, listed: []string{"First"}},
		{path: "/admin/events", create: map[string]string{"event[name]": "Open day", "event[starts_on]": "2026-12-01"}, update: map[string]string{"event[name]": "Open house"},
			listed: []string{"Open day", "2026-12-01"}, editing: map[string]string{"event[position]": "1", "event[starts_on]": "2026-12-01"}, blank: map[string]string{"event[name]": " "}},
		{path: "/resources", create: map[string]string{"resource[name]": "Guide"}, update: map[string]string{"resource[name]": "Handbook"}, listed: []string{"Guide"}, editing: map[string]string{"resource[position]": "1"}},`},
		{"postgres", [][]string{
			{"posts", "title:string", "position:position", "published_on:date"},
			{"admin/events", "name:string:required", "starts_on:date"},
		}, `
		{path: "/posts", create: map[string]string{"post[title]": "First", "post[published_on]": "2026-11-22"}, update: map[string]string{"post[title]": "Second"},
			listed: []string{"First", "2026-11-22"}, editing: map[string]string{"post[position]": "1", "post[published_on]": "2026-11-22"}},
		{path: "/admin/events", create: map[string]string{"event[name]": "Open day", "event[starts_on]": "2026-12-01"}, update: map[string]string{"event[name]": "Open house"},
			listed: []string{"Open day", "2026-12-01"}, editing: map[string]string{"event[starts_on]": "2026-12-01"}, blank: map[string]string{"event[name]": ""}},`},
	} {
		t.Run(tc.engine, func(t *testing.T) {
			dir := shopApp(t, tc.engine)
			for i, args := range tc.resources {
				at(t, fmt.Sprintf("2026-11-22T12:00:%02dZ", i))
				code, out, stderr := gantry(t, dir, append([]string{"g", "resource"}, args...)...)
				if code != 0 {
					t.Fatalf("g resource %v: %s", args, stderr)
				}
				if !strings.Contains(strings.Join(args, " "), ":photo") {
					wire(t, dir, out)
				}
			}
			filepath.WalkDir(filepath.Join(dir, "app"), func(path string, d os.DirEntry, err error) error {
				if rel, _ := filepath.Rel(dir, path); err == nil && !d.IsDir() && rel != filepath.Join("app", "routes.go") {
					b, _ := os.ReadFile(path)
					gofmted(t, rel, b)
				}
				return err
			})
			os.WriteFile(filepath.Join(dir, "app", "resources_test.go"), []byte(strings.Replace(resourcesTest, "\t\t// cases\n", tc.cases[1:]+"\n", 1)), 0o644)
			warnings, err := devcheck.Scan(dir)
			if err != nil || len(warnings) != 0 {
				t.Fatalf("Strict: %+v %v", warnings, err)
			}
			if testing.Short() {
				t.Skip("-short")
			}
			for _, tool := range []string{"templ", "sqlc"} {
				if _, err := exec.LookPath(tool); err != nil {
					t.Skip(tool + " isn't installed: run it in the toolchain (bin/go)")
				}
			}
			if tc.engine == "postgres" {
				t.Setenv("TEST_DATABASE_URL", testenv.Postgres(t))
			}
			repo, _ := filepath.Abs("../..")
			mod, _ := os.ReadFile(filepath.Join(dir, "go.mod"))
			os.WriteFile(filepath.Join(dir, "go.mod"), append(mod, "\nreplace github.com/scttymn/gantry => "+repo+"\n"...), 0o644)
			migrate := func(ctx context.Context, d *db.DB) error {
				return d.Migrate(ctx, os.DirFS(filepath.Join(dir, "db", "migrations")), "app_migrations")
			}
			var baseline *db.DB
			if tc.engine == "postgres" {
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
			for _, step := range [][]string{
				{"sqlc", "generate"},
				{"templ", "generate"},
				{"go", "mod", "tidy"},
				{"go", "vet", "./..."},
				{"go", "test", "-count=1", "-run", "TestResources", "-v", "./app/"},
				{"go", "test", "./..."},
			} {
				cmd := exec.Command(step[0], step[1:]...)
				cmd.Dir = dir
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("%s: %v\n%s", strings.Join(step, " "), err, out)
				}
				if step[0] == "go" && step[1] == "test" && step[len(step)-1] == "./app/" {
					t.Logf("%s", out)
				}
			}
		})
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
