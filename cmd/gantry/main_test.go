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

func TestResourceRules(t *testing.T) {
	t.Run("the next steps name the route and the package", func(t *testing.T) {
		out := generate(t, app(t), "admin/membership_options", "name:string:required", "position:position")
		for _, want := range []string{`rt.Scope("/admin", web.Pipeline{}, func(s *web.Scope) {`, `s.Resources("/membership_options", membershipoptions.Controller{Controller: resource.Controller{`, `"example.com/gym/app/admin/membershipoptions"`, "a.Auth.Required"} {
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
	t.Run("an edit reads and writes its row in one transaction, locked on Postgres", func(t *testing.T) {
		for engine, lock := range map[string]string{"sqlite": "WHERE id = sqlc.arg(id);", "postgresql": "WHERE id = sqlc.arg(id) FOR UPDATE;"} {
			root := app(t)
			os.WriteFile(filepath.Join(root, "sqlc.yaml"), []byte("version: \"2\"\nsql:\n  - engine: "+engine+"\n"), 0o644)
			generate(t, root, "admin/pillars", "title:string:required")
			code, _ := os.ReadFile(filepath.Join(root, "app/admin/pillars/controller.go"))
			update := string(code[strings.Index(string(code), "func (c Controller) Update"):])
			update = update[:strings.Index(update, "\n}\n")]
			for _, want := range []string{"c.DB.Tx(r.Context(), func(tx *db.Tx) error {", "q := models.New(tx)", "q.GetPillarForUpdate(", "q.UpdatePillar("} {
				if !strings.Contains(update, want) {
					t.Errorf("%s: no %q in\n%s", engine, want, update)
				}
			}
			if strings.Contains(update, "c.DB.Read") || strings.Contains(update, "c.DB.Write") {
				t.Errorf("%s: Update reads or writes outside its transaction:\n%s", engine, update)
			}
			if form, tx := strings.Index(update, "resource.Overlay("), strings.Index(update, "c.DB.Tx("); form < 0 || form > tx {
				t.Errorf("%s: Update reads the form inside its transaction:\n%s", engine, update)
			}
			sql, _ := os.ReadFile(filepath.Join(root, "app/models/pillars.sql"))
			if !strings.Contains(string(sql), "-- name: GetPillarForUpdate :one\nSELECT * FROM pillars "+lock) {
				t.Errorf("%s: GetPillarForUpdate isn't %q:\n%s", engine, lock, sql)
			}
		}
	})
	t.Run("an edit form sends its row's lock_version, and Update saves only that version", func(t *testing.T) {
		const conflict = `"Someone else changed this record while you were editing. Review the current values and submit again."`
		for _, engine := range []string{"sqlite", "postgresql"} {
			root := app(t)
			os.WriteFile(filepath.Join(root, "sqlc.yaml"), []byte("version: \"2\"\nsql:\n  - engine: "+engine+"\n"), 0o644)
			generate(t, root, "admin/pillars", "title:string:required")
			b, _ := os.ReadFile(filepath.Join(root, "app/admin/pillars/controller.go"))
			code := string(b)
			body := func(sig string) string {
				i := strings.Index(code, sig)
				if i < 0 {
					t.Fatalf("%s: no %s in\n%s", engine, sig, code)
				}
				s := code[i:]
				return s[:strings.Index(s, "\n}\n")]
			}
			if fields, _, _ := strings.Cut(code[strings.Index(code, "var fields = "):], "\n"); strings.Contains(fields, "lock_version") {
				t.Errorf("%s: lock_version is one of the form's own fields, which New shows too:\n%s", engine, fields)
			}
			if !strings.Contains(code, `resource.F("lock_version", resource.Type("hidden"))`) {
				t.Errorf("%s: the edit form has no hidden lock_version:\n%s", engine, code)
			}
			if form := body("func (c Controller) form("); !strings.Contains(form, "editFields") {
				t.Errorf("%s: the edit form doesn't send lock_version:\n%s", engine, form)
			}
			if values := body("func formValues("); !strings.Contains(values, `"lock_version": strconv.FormatInt(`) {
				t.Errorf("%s: formValues has no lock_version:\n%s", engine, values)
			}
			update := body("func (c Controller) Update")
			check, save := strings.Index(update, `if sent["lock_version"] != version {`), strings.Index(update, "q.UpdatePillar(")
			if check < 0 || save < check || !strings.Contains(update, "http.StatusConflict") || !strings.Contains(update, conflict) {
				t.Errorf("%s: Update doesn't answer 409 when the form's lock_version isn't the row's, before it saves:\n%s", engine, update)
			}
			sql, _ := os.ReadFile(filepath.Join(root, "app/models/pillars.sql"))
			if !strings.Contains(string(sql), "lock_version = lock_version + 1") {
				t.Errorf("%s: UpdatePillar doesn't move lock_version on:\n%s", engine, sql)
			}
		}
	})
	t.Run("a new row's next position is read and written in one transaction", func(t *testing.T) {
		root := app(t)
		generate(t, root, "admin/pillars", "title:string:required", "photo:photo", "position:position")
		code, _ := os.ReadFile(filepath.Join(root, "app/admin/pillars/controller.go"))
		create := string(code[strings.Index(string(code), "func (c Controller) Create"):])
		create = create[:strings.Index(create, "\n}\n")]
		tx, next, save, photo := strings.Index(create, "c.DB.Tx("), strings.Index(create, "q.NextPillarPosition("), strings.Index(create, "q.CreatePillar("), strings.Index(create, "c.SavePhoto(")
		if tx < 0 || next < tx || save < next || photo < save || strings.Contains(create, "c.DB.Write") {
			t.Fatalf("Create doesn't read the position and write the row in one transaction, then save the photo:\n%s", create)
		}
		end := strings.Index(create[tx:], "\n\t})") + tx
		if photo < end {
			t.Errorf("Create saves the photo inside its transaction (storage opens its own):\n%s", create)
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
			{"admin/pillars", "title:string:unique"}, {"admin/pillars", "lock_version:int"},
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

func TestResourceMigration(t *testing.T) {
	root := app(t)
	at(t, "2026-09-29T12:00:00Z")
	out := generate(t, root, "admin/programs", "name:string:required", "blurb:text", "photo:photo", "position:position", "starts_on:date")
	if !strings.Contains(out, "wrote db/migrations/20260929120000_create_programs.sql") {
		t.Fatalf("output:\n%s", out)
	}
	got, _ := os.ReadFile(filepath.Join(root, "db", "migrations", "20260929120000_create_programs.sql"))
	for _, want := range []string{`"name" text NOT NULL DEFAULT '',`, `"blurb" text NOT NULL DEFAULT '',`, `"position" integer,`, `"starts_on" date,`, `"updated_at" datetime NOT NULL,
  "lock_version" integer NOT NULL DEFAULT 0
);`} {
		if !strings.Contains(string(got), want) {
			t.Errorf("no %s in\n%s", want, got)
		}
	}
	if strings.Contains(string(got), "photo") {
		t.Errorf("a photo isn't a column:\n%s", got)
	}
	testkit.Migrations(t, testkit.DB(t, nil), os.DirFS(filepath.Join(root, "db", "migrations")), "app_migrations")
}
