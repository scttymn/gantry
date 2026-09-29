package main

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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

// What the recipe writes is pinned, on each engine.
func TestAPITokensGolden(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			dir := shopApp(t, engine)
			at(t, "2026-09-29T12:00:00Z")
			before := map[string]bool{}
			filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
				before[p] = true
				return err
			})
			code, out, stderr := gantry(t, dir, "g", "api-tokens", "--prefix", "shop_")
			if code != 0 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			if !strings.Contains(out, "apitokens.Require(a.DB, time.Now)") {
				t.Errorf("no route to add:\n%s", out)
			}
			golden := filepath.Join("testdata", "golden", "api-tokens-"+engine)
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
			if written != 4 {
				t.Errorf("wrote %d files, want 4", written)
			}
		})
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
