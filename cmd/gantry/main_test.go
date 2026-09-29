package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scttymn/gantry/testkit"
)

var update = flag.Bool("update", false, "rewrite testdata/golden from the generator")

// app is an app's root to generate into: a go.mod, and a sqlc.yaml that
// renames a column, as the site's does.
func app(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/gym\n\ngo 1.27\n"), 0o644)
	os.WriteFile(filepath.Join(root, "sqlc.yaml"), []byte("version: \"2\"\nsql:\n  - gen:\n      go:\n        rename:\n          cta: CTA\n        emit_empty_slices: true\n"), 0o644)
	return root
}

func generate(t *testing.T, root string, args ...string) string {
	t.Helper()
	var out, errOut strings.Builder
	if code := run(append([]string{"g", "resource"}, args...), root, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	return out.String()
}

// The generator's output is pinned: a change to it is a change to every
// table an app generates next, so it shows up here, and in review.
func TestGolden(t *testing.T) {
	for name, args := range map[string][]string{
		"pillars":  {"admin/pillars", "title:string:required", "body:string", "position:position"},
		"programs": {"admin/programs", "name:string:required", "blurb:text", "photo:photo", "cta:string", "position:position"},
		"workouts": {"admin/workouts", "date:date:required", "name:string", "rx:text"},
	} {
		t.Run(name, func(t *testing.T) {
			root := app(t)
			generate(t, root, args...)
			golden := filepath.Join("testdata", "golden", name)
			filepath.WalkDir(filepath.Join(root, "app"), func(path string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}
				rel, _ := filepath.Rel(root, path)
				got, _ := os.ReadFile(path)
				want := filepath.Join(golden, rel)
				if *update {
					os.MkdirAll(filepath.Dir(want), 0o755)
					os.WriteFile(want, got, 0o644)
					return nil
				}
				if w, err := os.ReadFile(want); err != nil || string(w) != string(got) {
					t.Errorf("%s differs from %s (go test ./cmd/gantry -update, then review the diff)", rel, want)
				}
				return nil
			})
		})
	}
}

func TestResourceRules(t *testing.T) {
	t.Run("the next steps name the route and the package", func(t *testing.T) {
		out := generate(t, app(t), "admin/membership_options", "name:string:required", "position:position")
		for _, want := range []string{`rt.Resources("/admin/membership_options", membershipoptions.Controller{Controller: adm}, requireAdmin)`, "example.com/gym/app/admin/membershipoptions"} {
			if !strings.Contains(out, want) {
				t.Errorf("no %q in\n%s", want, out)
			}
		}
	})
	t.Run("names follow sqlc's, renames included", func(t *testing.T) {
		root := app(t)
		generate(t, root, "admin/categories", "cta:string", "hero_url:string", "owner_id:int")
		code, _ := os.ReadFile(filepath.Join(root, "app/admin/categories/controller.go"))
		for _, want := range []string{"models.Category{", "category.CTA", "category.HeroUrl", "category.OwnerID", "ListCategories", `param    = "category"`} {
			if !strings.Contains(string(code), want) {
				t.Errorf("no %q", want)
			}
		}
	})
	t.Run("a file that's there already is never overwritten", func(t *testing.T) {
		root := app(t)
		generate(t, root, "admin/pillars", "title:string")
		var out, errOut strings.Builder
		if code := run([]string{"g", "resource", "admin/pillars", "title:string"}, root, &out, &errOut); code == 0 || !strings.Contains(errOut.String(), "is there already") {
			t.Fatalf("%d %s", code, errOut.String())
		}
	})
	t.Run("what it can't make is said, and nothing is written", func(t *testing.T) {
		for _, args := range [][]string{
			{"admin/pillars"}, {"admin/Pillars", "title:string"}, {"admin/pillars", "title:blob"}, {"admin/pillars", "title"},
			{"admin/pillars", "title:string:unique"}, {"pillars", "title:string"},
		} {
			root := app(t)
			var out, errOut strings.Builder
			if code := run(append([]string{"g", "resource"}, args...), root, &out, &errOut); code == 0 {
				t.Errorf("%v: made", args)
			}
			if _, err := os.Stat(filepath.Join(root, "app")); err == nil {
				t.Errorf("%v: wrote files", args)
			}
		}
	})
	t.Run("outside an app it says so", func(t *testing.T) {
		var out, errOut strings.Builder
		if code := run([]string{"g", "resource", "admin/pillars", "title:string"}, t.TempDir(), &out, &errOut); code == 0 || !strings.Contains(errOut.String(), "go.mod") {
			t.Errorf("%d %s", code, errOut.String())
		}
	})
}

func TestSingularize(t *testing.T) {
	for plural, want := range map[string]string{"pillars": "pillar", "categories": "category", "addresses": "address", "boxes": "box", "staff": "staff", "faqs": "faq"} {
		if got := singularize(plural); got != want {
			t.Errorf("%s: %s, want %s", plural, got, want)
		}
	}
}

// g resource writes its table's migration too, and it applies and rolls back.
func TestResourceMigration(t *testing.T) {
	root := app(t)
	at(t, "2026-09-29T12:00:00Z")
	out := generate(t, root, "admin/programs", "name:string:required", "blurb:text", "photo:photo", "position:position", "starts_on:date")
	if !strings.Contains(out, "wrote db/migrations/20260929120000_create_programs.sql") {
		t.Fatalf("output:\n%s", out)
	}
	got, _ := os.ReadFile(filepath.Join(root, "db", "migrations", "20260929120000_create_programs.sql"))
	for _, want := range []string{`"name" text NOT NULL DEFAULT '',`, `"blurb" text NOT NULL DEFAULT '',`, `"position" integer,`, `"starts_on" date,`} {
		if !strings.Contains(string(got), want) {
			t.Errorf("no %s in\n%s", want, got)
		}
	}
	if strings.Contains(string(got), "photo") {
		t.Errorf("a photo isn't a column:\n%s", got)
	}
	testkit.Migrations(t, testkit.DB(t, nil), os.DirFS(filepath.Join(root, "db", "migrations")), "app_migrations")
}
