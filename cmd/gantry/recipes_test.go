package main

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/devcheck"
	"github.com/scttymn/gantry/storage"
	"github.com/scttymn/gantry/testkit"
)

// shopApp is a new app, written without Houston, on engine.
func shopApp(t *testing.T, engine string) string {
	t.Helper()
	root := t.TempDir()
	if code, _, stderr := gantry(t, root, "new", "shop", "--db", engine, "--module", "example.com/shop", "--skip-houston"); code != 0 {
		t.Fatalf("gantry new: %s", stderr)
	}
	return filepath.Join(root, "shop")
}

// What each recipe writes is pinned, on each engine.
func TestRecipesGolden(t *testing.T) {
	for _, tc := range []struct {
		recipe string
		args   []string
		files  int
	}{
		{"api-tokens", []string{"--prefix", "shop_"}, 4},
		{"auth", nil, 7},
		{"auth", []string{"--email-code"}, 5},
		{"auth", []string{"--email-code", "--signup"}, 5},
		{"storage", nil, 1},
	} {
		for _, engine := range []string{"sqlite", "postgres"} {
			t.Run(tc.recipe+strings.Join(tc.args, "")+"/"+engine, func(t *testing.T) {
				recipeGolden(t, tc.recipe, tc.args, tc.files, engine)
			})
		}
	}
}

func recipeGolden(t *testing.T, recipe string, args []string, files int, engine string) {
	t.Helper()
	dir := shopApp(t, engine)
	at(t, "2026-09-29T12:00:00Z")
	before := map[string]bool{}
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		before[p] = true
		return err
	})
	code, out, stderr := gantry(t, dir, append([]string{"g", recipe}, args...)...)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(out, "rt.Scope(") && !strings.Contains(out, "rt.Mount(") {
		t.Errorf("no route to add:\n%s", out)
	}
	label := recipe
	if recipe == "auth" && len(args) > 0 {
		label += "-email-code"
		if len(args) > 1 {
			label += "-signup"
		}
	}
	golden := filepath.Join("testdata", "golden", label+"-"+engine)
	if *update {
		os.RemoveAll(golden)
	}
	written := 0
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || before[p] {
			return err
		}
		written++
		rel, _ := filepath.Rel(dir, p)
		got, _ := os.ReadFile(p)
		gofmted(t, rel, got)
		want := filepath.Join(golden, rel+".golden")
		if *update {
			os.MkdirAll(filepath.Dir(want), 0o755)
			return os.WriteFile(want, got, 0o644)
		}
		if w, err := os.ReadFile(want); err != nil || string(w) != string(got) {
			t.Errorf("%s differs from %s (go test ./cmd/gantry -update rewrites it)", rel, want)
		}
		return nil
	})
	if written != files {
		t.Errorf("wrote %d files, want %d", written, files)
	}
}

func TestAPITokensRules(t *testing.T) {
	dir := shopApp(t, "sqlite")
	for _, prefix := range []string{"Shop_", "shop", "_shop_", "shop-_", "averyveryverylongone_"} {
		if code, _, stderr := gantry(t, dir, "g", "api-tokens", "--prefix", prefix); code != 1 || !strings.Contains(stderr, "--prefix") {
			t.Errorf("prefix %q: exit %d %s", prefix, code, stderr)
		}
	}
	gantry(t, dir, "g", "api-tokens")
	if code, _, stderr := gantry(t, dir, "g", "api-tokens"); code != 1 || !strings.Contains(stderr, "the recipe was applied") {
		t.Errorf("twice: exit %d %s", code, stderr)
	}
	if code, _, stderr := gantry(t, t.TempDir(), "g", "api-tokens"); code != 1 || !strings.Contains(stderr, "run gantry at the app's root") {
		t.Errorf("outside an app: exit %d %s", code, stderr)
	}
}

// An app in a folder of a module imports under the module's path.
func TestAPITokensInModule(t *testing.T) {
	root := big(t, "")
	gantry(t, filepath.Join(root, "apps"), "new", "control", "--in-module", "--skip-houston")
	dir := filepath.Join(root, "apps", "control")
	if code, _, stderr := gantry(t, dir, "g", "api-tokens"); code != 0 {
		t.Fatal(stderr)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "app", "apitokens", "apitokens.go"))
	if !strings.Contains(string(b), `"example.com/big/apps/control/app/models"`) {
		t.Errorf("imports:\n%s", b)
	}
}

// A new app with the recipe applied migrates, generates its queries,
// builds, and passes the recipe's own tests, against this checkout.
func TestAPITokensBuilds(t *testing.T) {
	if testing.Short() {
		t.Skip("-short")
	}
	for _, tool := range []string{"templ", "sqlc"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " isn't installed: run it in the toolchain (bin/go)")
		}
	}
	repo, _ := filepath.Abs("../..")
	dir := shopApp(t, "sqlite")
	mod, _ := os.ReadFile(filepath.Join(dir, "go.mod"))
	os.WriteFile(filepath.Join(dir, "go.mod"), append(mod, "\nreplace github.com/scttymn/gantry => "+repo+"\n"...), 0o644)
	if code, _, stderr := gantry(t, dir, "g", "api-tokens", "--prefix", "shop_"); code != 0 {
		t.Fatal(stderr)
	}
	env := append(os.Environ(), "GANTRY_ENV=development", "DATABASE_URL=sqlite://"+filepath.Join(t.TempDir(), "dev.sqlite3"))
	for _, step := range [][]string{
		{"templ", "generate"}, // as gantry new does: its code first, then tidy
		{"go", "mod", "tidy"},
		{"go", "run", "./cmd/shop", "db", "migrate"}, // writes db/schema.sql, which sqlc reads
		{"sqlc", "generate"},
		{"go", "vet", "./..."},
		{"go", "test", "./..."},
	} {
		cmd := exec.Command(step[0], step[1:]...)
		cmd.Dir, cmd.Env = dir, env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", strings.Join(step, " "), err, out)
		}
	}
}

// A new app with the auth recipe applied builds, and the recipe's own tests
// pass there, against this checkout.
func TestAuthBuilds(t *testing.T) {
	if testing.Short() {
		t.Skip("-short")
	}
	for _, tool := range []string{"templ", "sqlc"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " isn't installed: run it in the toolchain (bin/go)")
		}
	}
	repo, _ := filepath.Abs("../..")
	dir := shopApp(t, "sqlite")
	mod, _ := os.ReadFile(filepath.Join(dir, "go.mod"))
	os.WriteFile(filepath.Join(dir, "go.mod"), append(mod, "\nreplace github.com/scttymn/gantry => "+repo+"\n"...), 0o644)
	if code, _, stderr := gantry(t, dir, "g", "auth"); code != 0 {
		t.Fatal(stderr)
	}
	env := append(os.Environ(), "GANTRY_ENV=development", "DATABASE_URL=sqlite://"+filepath.Join(t.TempDir(), "dev.sqlite3"))
	for _, step := range [][]string{
		{"templ", "generate"},
		{"go", "mod", "tidy"},
		{"go", "run", "./cmd/shop", "db", "migrate"},
		{"sqlc", "generate"},
		{"go", "vet", "./..."},
		{"go", "test", "./..."},
	} {
		cmd := exec.Command(step[0], step[1:]...)
		cmd.Dir, cmd.Env = dir, env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", strings.Join(step, " "), err, out)
		}
	}
}

func TestAuthRules(t *testing.T) {
	dir := shopApp(t, "sqlite")
	if code, _, stderr := gantry(t, dir, "g", "auth", "extra"); code != 1 || !strings.Contains(stderr, "no arguments") {
		t.Errorf("an argument: %d %s", code, stderr)
	}
	gantry(t, dir, "g", "auth")
	if code, _, stderr := gantry(t, dir, "g", "auth"); code != 1 || !strings.Contains(stderr, "the recipe was applied") {
		t.Errorf("twice: %d %s", code, stderr)
	}
}

// The tables g storage writes migrate, on each engine, and the storage
// package works on them.
func TestStorageMigrates(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			url := "sqlite://" + filepath.Join(t.TempDir(), "app.sqlite3")
			if engine == "postgres" {
				if url = os.Getenv("GANTRY_TEST_POSTGRES_URL"); url == "" {
					t.Skip("GANTRY_TEST_POSTGRES_URL isn't set")
				}
			}
			dir := shopApp(t, engine)
			if code, _, stderr := gantry(t, dir, "g", "storage"); code != 0 {
				t.Fatal(stderr)
			}
			ctx := context.Background()
			d, err := db.Open(ctx, url)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			if engine == "postgres" {
				d.Write.Exec(`DROP TABLE IF EXISTS active_storage_attachments, active_storage_blobs, gantry_storage_test_migrations`)
			}
			migrations, _ := filepath.Glob(filepath.Join(dir, "db", "migrations", "*_create_active_storage_tables.sql"))
			if len(migrations) != 1 {
				t.Fatalf("migrations: %v", migrations)
			}
			if err := d.Migrate(ctx, os.DirFS(filepath.Dir(migrations[0])), "gantry_storage_test_migrations"); err != nil {
				t.Fatal(err)
			}
			st := &storage.Storage{DB: d, Root: t.TempDir(), NoAVIF: true, Log: slog.New(slog.DiscardHandler)}
			defer st.Wait()
			ref := storage.Ref{RecordType: "Clip", RecordID: 1, Name: "thumbnail"}
			b, err := st.Attach(ctx, ref, storage.File{Filename: "a.txt", Data: []byte("hello")})
			if err != nil {
				t.Fatal(err)
			}
			if found, ok, err := st.Find(ctx, ref); err != nil || !ok || found.Key != b.Key {
				t.Fatalf("Find: %+v %v %v", found, ok, err)
			}
			if all, err := st.All(ctx, "Clip", "thumbnail"); err != nil || all[1].Key != b.Key {
				t.Fatalf("All: %+v %v", all, err)
			}
			if _, ok, err := st.Detach(ctx, ref); err != nil || !ok {
				t.Fatalf("Detach: %v", err)
			}
			if loose, err := st.Unattached(ctx); err != nil || len(loose) != 1 {
				t.Fatalf("Unattached: %+v %v", loose, err)
			}
			if _, err := st.AttachBlob(ctx, ref, b.Key); err != nil {
				t.Fatal(err)
			}
			st.Detach(ctx, ref)
			if err := st.PurgeBlob(ctx, b.Key); err != nil {
				t.Fatal(err)
			}
			if _, err := st.Attach(ctx, ref, storage.File{Filename: "b.txt", Data: []byte("again")}); err != nil {
				t.Fatal(err)
			}
			if err := st.Purge(ctx, ref); err != nil {
				t.Fatal(err)
			}
			if loose, _ := st.Unattached(ctx); len(loose) != 0 {
				t.Errorf("left: %+v", loose)
			}
		})
	}
}

func TestStorageRules(t *testing.T) {
	dir := shopApp(t, "sqlite")
	if code, _, stderr := gantry(t, dir, "g", "storage", "extra"); code != 1 || !strings.Contains(stderr, "no arguments") {
		t.Errorf("an argument: %d %s", code, stderr)
	}
	gantry(t, dir, "g", "storage")
	if code, _, stderr := gantry(t, dir, "g", "storage"); code != 1 || !strings.Contains(stderr, "the recipe was applied") {
		t.Errorf("twice: %d %s", code, stderr)
	}
}

// Email-code recipes use the shared Auth implementation and preserve password
// recipes. Build the real generated apps, including their authentication tests.
func TestEmailCodeRecipe(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		for _, mode := range []string{"password", "email-code", "signup"} {
			signup := mode == "signup"
			name := engine + "-" + mode
			if signup {
				name += "-signup"
			}
			t.Run(name, func(t *testing.T) {
				dir := shopApp(t, engine)
				args := []string{"g", "auth"}
				if mode != "password" {
					args = append(args, "--email-code")
				}
				if signup {
					args = append(args, "--signup")
				}
				if code, _, stderr := gantry(t, dir, args...); code != 0 {
					t.Fatal(stderr)
				}
				if mode == "password" {
					path := filepath.Join(dir, "app", "routes.go")
					source, _ := os.ReadFile(path)
					wired := strings.Replace(string(source), `"net/http"`, `"net/http"
 "path/filepath"
 "example.com/shop/config"
 "example.com/shop/app/auth"`, 1)
					wired = strings.Replace(wired, "homePage :=", `login := auth.New(a.DB,a.Signer,a.Mail,a.Mail.From,filepath.Join(config.FromEnv().DataDir,"initial-account-code"))
                    login.Routes(rt)
                    homePage :=`, 1)
					if err := os.WriteFile(path, []byte(wired), 0644); err != nil {
						t.Fatal(err)
					}
				}
				warnings, err := devcheck.Scan(dir)
				if err != nil || len(warnings) != 0 {
					t.Fatalf("Strict: %+v %v", warnings, err)
				}
				for _, name := range []string{"auth.go", "auth_test.go"} {
					b, _ := os.ReadFile(filepath.Join(dir, "app", "auth", name))
					gofmted(t, name, b)
				}
				if code, _, stderr := gantry(t, dir, args...); code != 1 || !strings.Contains(stderr, "recipe was applied") {
					t.Fatalf("reapplied: %s", stderr)
				}
				if testing.Short() {
					return
				}
				for _, tool := range []string{"templ", "sqlc"} {
					if _, err := exec.LookPath(tool); err != nil {
						t.Skip("run in the toolchain")
					}
				}
				if engine == "postgres" && os.Getenv("GANTRY_TEST_POSTGRES_URL") == "" {
					t.Skip("GANTRY_TEST_POSTGRES_URL not set")
				}
				repo, _ := filepath.Abs("../..")
				mod, _ := os.ReadFile(filepath.Join(dir, "go.mod"))
				os.WriteFile(filepath.Join(dir, "go.mod"), append(mod, "\nreplace github.com/scttymn/gantry => "+repo+"\n"...), 0o644)
				migrate := func(ctx context.Context, d *db.DB) error {
					return d.Migrate(ctx, os.DirFS(filepath.Join(dir, "db", "migrations")), "app_migrations")
				}
				var baseline *db.DB
				if engine == "postgres" {
					t.Setenv("TEST_DATABASE_URL", os.Getenv("GANTRY_TEST_POSTGRES_URL"))
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
				env := append(os.Environ(), "TEST_DATABASE_URL="+os.Getenv("GANTRY_TEST_POSTGRES_URL"))
				for _, step := range [][]string{{"templ", "generate"}, {"go", "mod", "tidy"}, {"go", "vet", "./..."}, {"go", "test", "./..."}} {
					cmd := exec.Command(step[0], step[1:]...)
					cmd.Dir, cmd.Env = dir, env
					if out, err := cmd.CombinedOutput(); err != nil {
						t.Fatalf("%v: %v\n%s", step, err, out)
					}
				}
			})
		}
	}
	dir := shopApp(t, "sqlite")
	if code, _, stderr := gantry(t, dir, "g", "auth", "--signup"); code != 1 || !strings.Contains(stderr, "requires --email-code") {
		t.Fatalf("signup without email codes: %s", stderr)
	}
}

func TestSocialRecipe(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		for _, mode := range []string{"password", "email-code"} {
			t.Run(engine+"/"+mode, func(t *testing.T) {
				dir := shopApp(t, engine)
				at(t, "2026-10-12T12:00:00Z")
				args := []string{"g", "auth"}
				if mode == "email-code" {
					args = append(args, "--email-code", "--signup")
				}
				if code, _, stderr := gantry(t, dir, args...); code != 0 {
					t.Fatal(stderr)
				}
				original, _ := os.ReadFile(filepath.Join(dir, "app", "auth", "auth.go"))
				if code, out, stderr := gantry(t, dir, "g", "auth", "--social"); code != 0 || !strings.Contains(out, "ConfigureSocial") {
					t.Fatal(out, stderr)
				}
				after, _ := os.ReadFile(filepath.Join(dir, "app", "auth", "auth.go"))
				if string(original) != string(after) {
					t.Fatal("social recipe overwrote existing auth")
				}
				if code, _, _ := gantry(t, dir, "g", "auth", "--social"); code != 1 {
					t.Fatal("reapplied social recipe")
				}
				files, _ := filepath.Glob(filepath.Join(dir, "db", "migrations", "*_create_auth_identities.sql"))
				if len(files) != 1 {
					t.Fatal("no migration")
				}
				stamp := "20261012120001"
				if mode == "email-code" {
					stamp = "20261012120002"
				}
				if !strings.HasPrefix(filepath.Base(files[0]), stamp) {
					t.Fatal("migration does not follow auth", files)
				}
				for _, rel := range []string{"app/auth/social.go", "app/auth/social_pages.templ", "app/auth/social_test.go", "db/migrations/" + filepath.Base(files[0])} {
					got, err := os.ReadFile(filepath.Join(dir, rel))
					if err != nil {
						t.Fatal(err)
					}
					gofmted(t, rel, got)
					want := filepath.Join("testdata", "golden", "auth-social-"+mode+"-"+engine, rel+".golden")
					if *update {
						if err := os.MkdirAll(filepath.Dir(want), 0755); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(want, got, 0644); err != nil {
							t.Fatal(err)
						}
					} else if expected, err := os.ReadFile(want); err != nil || string(expected) != string(got) {
						t.Errorf("%s differs from %s (run with -update)", rel, want)
					}
				}
				warnings, err := devcheck.Scan(dir)
				if err != nil || len(warnings) != 0 {
					t.Fatalf("Strict: %+v %v", warnings, err)
				}
				if testing.Short() {
					return
				}
				if _, err := exec.LookPath("templ"); err != nil {
					t.Skip("toolchain required")
				}
				if engine == "postgres" && os.Getenv("GANTRY_TEST_POSTGRES_URL") == "" {
					t.Skip("Postgres required")
				}
				repo, _ := filepath.Abs("../..")
				mod, _ := os.ReadFile(filepath.Join(dir, "go.mod"))
				os.WriteFile(filepath.Join(dir, "go.mod"), append(mod, "\nreplace github.com/scttymn/gantry => "+repo+"\n"...), 0644)
				migrate := func(ctx context.Context, d *db.DB) error {
					return d.Migrate(ctx, os.DirFS(filepath.Join(dir, "db", "migrations")), "app_migrations")
				}
				var baseline *db.DB
				if engine == "postgres" {
					t.Setenv("TEST_DATABASE_URL", os.Getenv("GANTRY_TEST_POSTGRES_URL"))
					baseline = testkit.Postgres(t, migrate)
				} else {
					baseline = testkit.DB(t, migrate)
				}
				schema, err := baseline.Schema(t.Context(), "app_migrations")
				if err != nil {
					t.Fatal(err)
				}
				os.WriteFile(filepath.Join(dir, "db", "schema.sql"), []byte(schema), 0644)
				for _, step := range [][]string{{"templ", "generate"}, {"go", "mod", "tidy"}, {"go", "vet", "./..."}, {"go", "test", "./..."}} {
					cmd := exec.Command(step[0], step[1:]...)
					cmd.Dir = dir
					cmd.Env = append(os.Environ(), "TEST_DATABASE_URL="+os.Getenv("GANTRY_TEST_POSTGRES_URL"))
					if out, err := cmd.CombinedOutput(); err != nil {
						t.Fatalf("%v: %v\n%s", step, err, out)
					}
				}
			})
		}
	}
	dir := shopApp(t, "sqlite")
	if code, _, _ := gantry(t, dir, "g", "auth", "--social"); code != 1 {
		t.Fatal("social recipe needs existing auth")
	}
}
