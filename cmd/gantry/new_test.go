package main

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// steps records the commands gantry runs instead of running them; fail
// makes the one named fail.
func steps(t *testing.T, fail string) *[][]string {
	t.Helper()
	var got [][]string
	was, wasQuiet, wasLook := runIn, runQuietlyIn, lookPath
	runIn = func(dir, name string, args ...string) error {
		got = append(got, append([]string{filepath.Base(dir), name}, args...))
		if fail != "" && strings.Join(append([]string{name}, args...), " ") == fail {
			return errExit(3)
		}
		return nil
	}
	runQuietlyIn = func(dir, name string, args ...string) ([]byte, error) {
		return []byte(name + " said this\n"), runIn(dir, name, args...)
	}
	lookPath = func(string) (string, error) { return "/usr/local/bin/houston", nil }
	t.Cleanup(func() { runIn, runQuietlyIn, lookPath = was, wasQuiet, wasLook })
	return &got
}

func gantry(t *testing.T, root string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut strings.Builder
	code := run(args, root, &out, &errOut)
	return code, out.String(), errOut.String()
}

// What gantry new writes is pinned, on each engine: a change to it is a
// change to every app made next, so it shows up here, and in review.
func TestNewGolden(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			root := t.TempDir()
			if code, _, stderr := gantry(t, root, "new", "shop", "--db", engine, "--module", "example.com/shop", "--skip-houston"); code != 0 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			dir := filepath.Join(root, "shop")
			golden := filepath.Join("testdata", "golden", "new-"+engine)
			if *update {
				os.RemoveAll(golden)
			}
			seen := map[string]bool{}
			filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}
				rel, _ := filepath.Rel(dir, path)
				if rel == ".env" {
					// A random password, checked by TestNewPostgresEnv.
					return nil
				}
				seen[rel] = true
				got, _ := os.ReadFile(path)
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
			filepath.WalkDir(golden, func(path string, d fs.DirEntry, err error) error {
				if err == nil && !d.IsDir() {
					rel, _ := filepath.Rel(golden, path)
					if !seen[strings.TrimSuffix(rel, ".golden")] {
						t.Errorf("%s isn't written any more", rel)
					}
				}
				return err
			})
		})
	}
}

func TestNewPostgresEnv(t *testing.T) {
	root := t.TempDir()
	gantry(t, root, "new", "shop", "--db", "postgres", "--skip-houston")
	env, _ := os.ReadFile(filepath.Join(root, "shop", ".env"))
	if !strings.HasPrefix(string(env), "POSTGRES_PASSWORD=") || len(strings.TrimSpace(string(env))) != len("POSTGRES_PASSWORD=")+32 {
		t.Errorf(".env = %q", env)
	}
	root2 := t.TempDir()
	gantry(t, root2, "new", "shop", "--db", "postgres", "--skip-houston")
	if env2, _ := os.ReadFile(filepath.Join(root2, "shop", ".env")); string(env2) == string(env) {
		t.Error("two apps got the same password")
	}
}

// gantry new's steps: Houston's files, the module's requirements, templ's
// code, and git.
func TestNewSteps(t *testing.T) {
	root := t.TempDir()
	got := steps(t, "")
	code, out, stderr := gantry(t, root, "new", "shop")
	want := [][]string{
		{"shop", "houston", "init", "--name", "shop"},
		{"shop", "houston", "exec", "go", "mod", "tidy"},
		{"shop", "houston", "exec", "templ", "generate"},
		{"shop", "git", "init", "-q"},
	}
	if code != 0 || !reflect.DeepEqual(*got, want) {
		t.Fatalf("exit %d, ran %q\n%s", code, *got, stderr)
	}
	if !strings.Contains(out, "cd shop && gantry dev, and it's at http://shop.localhost") || strings.Contains(out+stderr, "said this") {
		t.Errorf("output: %s%s", out, stderr)
	}

	// houston init's output is shown when it fails.
	root = t.TempDir()
	steps(t, "houston init --name shop")
	if code, _, stderr := gantry(t, root, "new", "shop"); code != 1 || !strings.Contains(stderr, "houston said this") {
		t.Errorf("exit %d: %s", code, stderr)
	}

	// A step that fails stops it, and says what's left.
	root = t.TempDir()
	got = steps(t, "houston exec go mod tidy")
	code, _, stderr = gantry(t, root, "new", "shop")
	if code != 1 || len(*got) != 2 || !strings.Contains(stderr, "run it, and the steps after it, in shop/") {
		t.Errorf("exit %d, ran %q, %s", code, *got, stderr)
	}
}

func TestNewRules(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "taken"), 0o755)
	os.WriteFile(filepath.Join(root, "taken", "x"), nil, 0o644)
	for _, args := range [][]string{
		{"new"}, {"new", "--db", "sqlite"}, {"new", "My_App"}, {"new", "shop-"}, {"new", "1shop"},
		{"new", "shop", "--db", "mysql"}, {"new", "taken"},
		{"new", "shop", "--gantry", filepath.Join(root, "nowhere")},
	} {
		if code, _, stderr := gantry(t, root, append(args, "--skip-houston")...); code == 0 || stderr == "" {
			t.Errorf("%q: exit %d", args, code)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "shop")); err == nil {
		t.Error("a refused app left files behind")
	}
}

// --gantry: go.work uses the checkout, and the dev container mounts it at
// the same place relative to /app.
func TestNewGantryCheckout(t *testing.T) {
	root := t.TempDir()
	checkout := filepath.Join(root, "gantry")
	os.MkdirAll(checkout, 0o755)
	os.WriteFile(filepath.Join(checkout, "go.mod"), []byte("module github.com/scttymn/gantry\n"), 0o644)
	if code, _, stderr := gantry(t, root, "new", "shop", "--gantry", checkout, "--skip-houston"); code != 0 {
		t.Fatal(stderr)
	}
	work, _ := os.ReadFile(filepath.Join(root, "shop", "go.work"))
	compose, _ := os.ReadFile(filepath.Join(root, "shop", "compose.yml"))
	if !strings.Contains(string(work), "use (\n\t.\n\t../gantry\n)") || !strings.Contains(string(compose), "- ../gantry:/gantry") {
		t.Errorf("go.work:\n%s\ncompose.yml:\n%s", work, compose)
	}
	ignored, _ := os.ReadFile(filepath.Join(root, "shop", ".gitignore"))
	if !strings.Contains(string(ignored), "go.work\n") {
		t.Error("go.work isn't ignored by git")
	}
}

// gantry db, task and tasks are the app's own commands, in its container;
// anything else is Houston's.
func TestAppCommands(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "cmd", "shop"), 0o755)
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{[]string{"db", "migrate"}, []string{"houston", "exec", "go", "run", "./cmd/shop", "db", "migrate"}},
		{[]string{"db", "rollback", "2"}, []string{"houston", "exec", "go", "run", "./cmd/shop", "db", "rollback", "2"}},
		{[]string{"task", "backfill", "--dry-run"}, []string{"houston", "exec", "go", "run", "./cmd/shop", "task", "backfill", "--dry-run"}},
		{[]string{"tasks"}, []string{"houston", "exec", "go", "run", "./cmd/shop", "tasks"}},
		{[]string{"db", "migrate", "--local"}, []string{"env", "GANTRY_ENV=development", "go", "run", "./cmd/shop", "db", "migrate"}},
		{[]string{"dev", "--as", "login"}, []string{"houston", "dev", "--as", "login"}},
		{[]string{"test"}, []string{"houston", "test"}},
		{[]string{"deploy", "--server"}, []string{"houston", "deploy", "--server"}},
	} {
		got := steps(t, "")
		if code, _, stderr := gantry(t, root, tc.args...); code != 0 || len(*got) != 1 || !reflect.DeepEqual((*got)[0][1:], tc.want) {
			t.Errorf("%q: exit %d, ran %q (%s)", tc.args, code, *got, stderr)
		}
	}
	// The command's exit code is gantry's.
	steps(t, "houston test")
	if code, _, _ := gantry(t, root, "test"); code != 3 {
		t.Errorf("exit %d, want houston's 3", code)
	}
}

func TestHoustonMissing(t *testing.T) {
	got := steps(t, "")
	lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	code, _, stderr := gantry(t, t.TempDir(), "dev")
	if code != 1 || len(*got) != 0 || !strings.Contains(stderr, "Houston, which isn't installed") {
		t.Errorf("exit %d, ran %q: %s", code, *got, stderr)
	}
}

func TestAppCommandsTwoCommands(t *testing.T) {
	steps(t, "")
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "cmd", "shop"), 0o755)
	os.MkdirAll(filepath.Join(root, "cmd", "worker"), 0o755)
	if code, _, stderr := gantry(t, root, "db", "migrate"); code != 1 || !strings.Contains(stderr, "cmd/ has shop, worker") {
		t.Errorf("exit %d: %s", code, stderr)
	}
}

func TestAppCommandsOutsideAnApp(t *testing.T) {
	steps(t, "")
	code, _, stderr := gantry(t, t.TempDir(), "db", "migrate")
	if code != 1 || !strings.Contains(stderr, "run gantry at the app's root") {
		t.Errorf("exit %d: %s", code, stderr)
	}
}

// A new app builds, vets clean and passes its own tests, against this
// checkout (--gantry, go.work). It needs the toolchain (templ, and Go's
// module cache or the network): bin/go go test ./cmd/gantry runs it.
func TestNewAppBuilds(t *testing.T) {
	if testing.Short() {
		t.Skip("-short")
	}
	if _, err := exec.LookPath("templ"); err != nil {
		t.Skip("templ isn't installed: run it in the toolchain (bin/go)")
	}
	repo, _ := filepath.Abs("../..")
	root := t.TempDir()
	if code, _, stderr := gantry(t, root, "new", "shop", "--skip-houston", "--gantry", repo, "--module", "example.com/shop"); code != 0 {
		t.Fatal(stderr)
	}
	dir := filepath.Join(root, "shop")
	for _, step := range [][]string{{"templ", "generate"}, {"go", "vet", "./..."}, {"go", "test", "./..."}} {
		cmd := exec.Command(step[0], step[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", strings.Join(step, " "), err, out)
		}
	}
}

// A command's own exit code is gantry's: houston test's 3 is gantry test's.
func TestExitCodes(t *testing.T) {
	err := exitError(exec.Command("sh", "-c", "exit 5").Run())
	var code errExit
	if !errors.As(err, &code) || code != 5 {
		t.Errorf("%v, want exit 5", err)
	}
	if err := exitError(exec.Command("no-such-command-here").Run()); errors.As(err, &code) {
		t.Errorf("a command that didn't start is %v, not an exit code", err)
	}
}
