package main

import (
	"errors"
	"go/format"
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
	was, wasQuiet, wasLook, wasVersion, wasPinned := runIn, runQuietlyIn, lookPath, houstonVersion, pinnedHouston
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
	houstonVersion = func() string { return houstonRelease }
	pinnedHouston = func(dir string) (string, error) {
		return filepath.Join(dir, ".gantry", "bin", "houston"), nil
	}
	t.Cleanup(func() {
		runIn, runQuietlyIn, lookPath, houstonVersion, pinnedHouston = was, wasQuiet, wasLook, wasVersion, wasPinned
	})
	return &got
}

func gantry(t *testing.T, root string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut strings.Builder
	code := run(args, root, &out, &errOut)
	return code, out.String(), errOut.String()
}

// big is a module to make an app in, with --in-module.
func big(t *testing.T, repo string) string {
	t.Helper()
	root := t.TempDir()
	mod := "module example.com/big\n\ngo 1.27.1\n"
	if repo != "" {
		mod += "\nreplace github.com/scttymn/gantry => " + repo + "\n"
	}
	os.WriteFile(filepath.Join(root, "go.mod"), []byte(mod), 0o644)
	os.MkdirAll(filepath.Join(root, "apps"), 0o755)
	return root
}

// An app in a folder of the module it's made in: no go.mod of its own, its
// imports under the module's path, built from the module's root.
func TestNewInModule(t *testing.T) {
	root := big(t, "")
	if code, _, stderr := gantry(t, filepath.Join(root, "apps"), "new", "control", "--in-module", "--skip-houston"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	dir := filepath.Join(root, "apps", "control")
	for _, gone := range []string{"go.mod", "go.work", ".dockerignore"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); err == nil {
			t.Errorf("%s written in a module's app", gone)
		}
	}
	read := func(name string) string { b, _ := os.ReadFile(filepath.Join(dir, name)); return string(b) }
	for file, wants := range map[string][]string{
		"cmd/control/main.go":     {`"example.com/big/apps/control/app"`},
		"compose.yml":             {"build: { context: ../.., dockerfile: apps/control/Dockerfile, target: dev }", "- ../..:/app "},
		"Dockerfile":              {"WORKDIR /app/apps/control", `"-path", "/app/apps/control"`, "GOMODCACHE=/app/apps/control/.cache/go/mod", "templ generate -path /app/apps/control && go run ./cmd/control assets /cache/assets && cp -a /cache/assets/. assets/built/"},
		"Dockerfile.dockerignore": {"**/.git", "**/.cache", "**/.gantry"},
	} {
		for _, want := range wants {
			if !strings.Contains(read(file), want) {
				t.Errorf("%s: no %q in\n%s", file, want, read(file))
			}
		}
	}
	// Its steps: no git init, the module's repo is there.
	got := steps(t, "")
	gantry(t, filepath.Join(root, "apps"), "new", "desk", "--in-module")
	for _, step := range *got {
		if step[1] == "git" {
			t.Errorf("ran %q in a module's repo", step)
		}
	}
	if len(*got) != 3 {
		t.Errorf("ran %q", *got)
	}
}

func TestNewInModuleRules(t *testing.T) {
	if code, _, stderr := gantry(t, t.TempDir(), "new", "control", "--in-module", "--skip-houston"); code != 1 || !strings.Contains(stderr, "no go.mod here or above") {
		t.Errorf("no module: exit %d, %s", code, stderr)
	}
	if code, _, stderr := gantry(t, big(t, ""), "new", "control", "--in-module", "--gantry", "..", "--skip-houston"); code != 1 || !strings.Contains(stderr, "--in-module and --gantry") {
		t.Errorf("with --gantry: exit %d, %s", code, stderr)
	}
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
				got, _ := os.ReadFile(path)
				if strings.HasPrefix(filepath.ToSlash(rel), "assets/js/vendor/") {
					// Vendored JavaScript is copied as it is, not kept twice.
					if want, err := fs.ReadFile(templates, "templates/new/"+filepath.ToSlash(rel)); err != nil || string(want) != string(got) {
						t.Errorf("%s isn't gantry's copy", rel)
					}
					return nil
				}
				seen[rel] = true
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
		{"shop", "houston", "exec", "templ", "generate"},
		{"shop", "houston", "exec", "go", "mod", "tidy"},
		{"shop", "git", "init", "-q"},
	}
	if code != 0 || !reflect.DeepEqual(*got, want) {
		t.Fatalf("exit %d, ran %q\n%s", code, *got, stderr)
	}
	if !strings.Contains(out, "cd shop && bin/gantry dev, and it's at http://shop.localhost") || strings.Contains(out+stderr, "said this") {
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
	got = steps(t, "houston exec templ generate")
	code, _, stderr = gantry(t, root, "new", "shop")
	if code != 1 || len(*got) != 2 || !strings.Contains(stderr, "run it, and the steps after it, in shop/") {
		t.Errorf("exit %d, ran %q, %s", code, *got, stderr)
	}
}

// The houston on PATH runs gantry new's steps when it's new enough for
// them; an older one, or none, and the app's pinned release does, which its
// bin/gantry downloads.
func TestNewHouston(t *testing.T) {
	pinned := [][]string{
		{"shop", "shop/.gantry/bin/houston", "init", "--name", "shop"},
		{"shop", "shop/.gantry/bin/houston", "exec", "templ", "generate"},
		{"shop", "shop/.gantry/bin/houston", "exec", "go", "mod", "tidy"},
		{"shop", "git", "init", "-q"},
	}
	onPath := [][]string{
		{"shop", "houston", "init", "--name", "shop"},
		{"shop", "houston", "exec", "templ", "generate"},
		{"shop", "houston", "exec", "go", "mod", "tidy"},
		{"shop", "git", "init", "-q"},
	}
	for _, c := range []struct {
		found string
		want  [][]string
		note  string
	}{
		{houstonRelease, onPath, ""},
		{"v9.0.0", onPath, ""},
		{"dev", onPath, ""}, // built from a checkout: trusted
		{"v0.4.9", pinned, "the houston on PATH is v0.4.9"},
		{"", pinned, "there's no houston on PATH"},
	} {
		root := t.TempDir()
		got := steps(t, "")
		houstonVersion = func() string { return c.found }
		pinnedHouston = func(dir string) (string, error) {
			return filepath.Join(filepath.Base(dir), ".gantry", "bin", "houston"), nil
		}
		code, out, stderr := gantry(t, root, "new", "shop")
		if code != 0 || !reflect.DeepEqual(*got, c.want) {
			t.Errorf("houston %q: exit %d, ran %q\n%s", c.found, code, *got, stderr)
		}
		if strings.Contains(stderr, "needs Houston") != (c.note != "") || !strings.Contains(stderr, c.note) {
			t.Errorf("houston %q: %s", c.found, stderr)
		}
		// Whichever it is, it's "houston" to people.
		if !strings.Contains(out, "  run houston init --name shop\n") {
			t.Errorf("houston %q: %s", c.found, out)
		}
	}

	// The pinned release can't be downloaded: what's left to do, by hand.
	root := t.TempDir()
	got := steps(t, "")
	houstonVersion = func() string { return "v0.4.9" }
	pinnedHouston = func(string) (string, error) { return "", errors.New("no network") }
	code, _, stderr := gantry(t, root, "new", "shop")
	if code != 1 || len(*got) != 0 || !strings.Contains(stderr, "no network") || !strings.Contains(stderr, "in shop/ run houston init --name shop") {
		t.Errorf("exit %d, ran %q: %s", code, *got, stderr)
	}
}

// A release's binary says which release it is (the Dockerfile stamps
// releaseVersion), and the app it makes requires that one. A build from a
// checkout requires the newest release, and says so.
func TestNewRequiresThisRelease(t *testing.T) {
	requires := func(t *testing.T) (string, string) {
		t.Helper()
		root := t.TempDir()
		code, _, stderr := gantry(t, root, "new", "shop", "--skip-houston")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		mod, _ := os.ReadFile(filepath.Join(root, "shop", "go.mod"))
		for _, line := range strings.Split(string(mod), "\n") {
			if f := strings.Fields(line); len(f) == 2 && f[0] == "github.com/scttymn/gantry" {
				return f[1], stderr
			}
		}
		t.Fatalf("go.mod requires no gantry:\n%s", mod)
		return "", ""
	}
	was := releaseVersion
	t.Cleanup(func() { releaseVersion = was })

	releaseVersion = "v9.8.7"
	if got, stderr := requires(t); got != "v9.8.7" || strings.Contains(stderr, "isn't a release") {
		t.Errorf("a release's binary: requires %s; %s", got, stderr)
	}
	for _, v := range []string{"", "dev", "v9.8.7-rc1"} {
		releaseVersion = v
		if got, stderr := requires(t); got != latestRelease || !strings.Contains(stderr, "isn't a release") {
			t.Errorf("version %q: requires %s; %s", v, got, stderr)
		}
	}
}

// A release is tagged with latestRelease raised to its tag: the release
// workflow gives the tag, so nothing is published with it behind, and an
// app made from a checkout never requires a release older than the newest.
func TestLatestReleaseIsTheTag(t *testing.T) {
	tag := os.Getenv("GANTRY_RELEASE_TAG")
	if tag == "" {
		t.Skip("not a release: GANTRY_RELEASE_TAG isn't set")
	}
	if latestRelease != tag {
		t.Fatalf("latestRelease is %s, and this is %s: raise it in cmd/gantry/new.go (and the golden go.mod files: go test ./cmd/gantry -update), then tag that commit", latestRelease, tag)
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

// An app made in a module builds and passes its tests there, against this
// checkout (the module's replace).
func TestNewInModuleBuilds(t *testing.T) {
	if testing.Short() {
		t.Skip("-short")
	}
	if _, err := exec.LookPath("templ"); err != nil {
		t.Skip("templ isn't installed: run it in the toolchain (bin/go)")
	}
	repo, _ := filepath.Abs("../..")
	root := big(t, repo)
	if code, _, stderr := gantry(t, filepath.Join(root, "apps"), "new", "control", "--in-module", "--skip-houston"); code != 0 {
		t.Fatal(stderr)
	}
	dir := filepath.Join(root, "apps", "control")
	for _, step := range [][]string{{"go", "mod", "tidy"}, {"templ", "generate"}, {"go", "vet", "./..."}, {"go", "test", "./..."}} {
		cmd := exec.Command(step[0], step[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", strings.Join(step, " "), err, out)
		}
	}
}

// A new app builds, vets clean and passes its own tests, against this
// checkout. It needs the toolchain (templ, and Go's
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
	// This checkout, not the release go.mod requires: go.work alone still
	// reads that release's go.mod, which isn't published until it's tagged.
	mod, _ := os.ReadFile(filepath.Join(dir, "go.mod"))
	os.WriteFile(filepath.Join(dir, "go.mod"), append(mod, "\nreplace github.com/scttymn/gantry => "+repo+"\n"...), 0o644)
	for _, step := range [][]string{{"templ", "generate"}, {"go", "run", "./cmd/shop", "assets"}, {"go", "vet", "./..."}, {"go", "test", "./..."}} {
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

// gofmted fails the test when a generated Go file isn't as gofmt writes it.
func gofmted(t *testing.T, rel string, src []byte) {
	t.Helper()
	if !strings.HasSuffix(rel, ".go") {
		return
	}
	if f, err := format.Source(src); err != nil || string(f) != string(src) {
		t.Errorf("%s isn't gofmt'd (%v)", rel, err)
	}
}
