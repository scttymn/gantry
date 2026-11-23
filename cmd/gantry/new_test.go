package main

import (
	"encoding/hex"
	"errors"
	"go/format"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/scttymn/gantry/testkit"
	"gopkg.in/yaml.v3"
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

// The JavaScript a new app vendors is MIT, which asks that every copy carry
// its copyright and permission notice: the app's copy does, in a /*! */
// comment the build's minifying keeps.
func TestNewVendorLicenses(t *testing.T) {
	root := t.TempDir()
	if code, _, stderr := gantry(t, root, "new", "shop", "--skip-houston"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for file, copyright := range map[string]string{
		"@hotwired--turbo.js":    "Copyright (c) 37signals",
		"@hotwired--stimulus.js": "Copyright © 2021 Basecamp, LLC.",
	} {
		got, err := os.ReadFile(filepath.Join(root, "shop", "assets", "js", "vendor", file))
		if err != nil {
			t.Fatal(err)
		}
		_, rest, opened := strings.Cut(string(got), "\n\n/*!\n")
		license, _, closed := strings.Cut(rest, "*/")
		if !opened || !closed {
			t.Errorf("%s has no license comment after its first line", file)
			continue
		}
		for _, want := range []string{copyright, "Permission is hereby granted, free of charge", "included in all copies or substantial portions of the Software"} {
			if !strings.Contains(license, want) {
				t.Errorf("%s's license is missing %q", file, want)
			}
		}
	}
}

// A new app's CI runs Strict, the generated-code check and the tests, each a
// command gantry has, with its actions pinned by commit and no secrets. An
// app in a module has its repository's CI instead.
func TestNewCI(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		root := t.TempDir()
		if code, _, stderr := gantry(t, root, "new", "shop", "--db", engine, "--skip-houston"); code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		ci, err := os.ReadFile(filepath.Join(root, "shop", ".github", "workflows", "ci.yml"))
		if err != nil {
			t.Fatal(err)
		}
		var runs []string
		for _, line := range strings.Split(string(ci), "\n") {
			line = strings.TrimSpace(line)
			if command, ok := strings.CutPrefix(line, "- run: bin/gantry "); ok {
				runs = append(runs, command)
				if msg := checkInvocation(command); msg != "" {
					t.Errorf("%s: bin/gantry %s: %s", engine, command, msg)
				}
			}
			if action, ok := strings.CutPrefix(strings.TrimPrefix(line, "- "), "uses: "); ok && !regexp.MustCompile(`@[0-9a-f]{40} # v`).MatchString(action) {
				t.Errorf("%s: %s isn't pinned by commit", engine, action)
			}
		}
		if want := []string{"check --strict", "check --generated", "test"}; !reflect.DeepEqual(runs, want) {
			t.Errorf("%s: runs bin/gantry %q, want %q", engine, runs, want)
		}
		if strings.Contains(string(ci), "secrets.") || !strings.Contains(string(ci), "GH_TOKEN: ${{ github.token }}") {
			t.Errorf("%s: its token isn't the job's own:\n%s", engine, ci)
		}
	}
	root := big(t, "")
	if code, _, stderr := gantry(t, filepath.Join(root, "apps"), "new", "control", "--in-module", "--skip-houston"); code != 0 {
		t.Fatal(stderr)
	}
	if _, err := os.Stat(filepath.Join(root, "apps", "control", ".github")); err == nil {
		t.Error("an app in a module has a .github of its own")
	}
}

// gantry secret is a key sign.Key takes: 32 random bytes, in hex.
func TestSecret(t *testing.T) {
	keys := map[string]bool{}
	for range 2 {
		code, out, stderr := gantry(t, t.TempDir(), "secret")
		key, ok := strings.CutSuffix(out, "\n")
		if raw, err := hex.DecodeString(key); code != 0 || !ok || err != nil || len(raw) != 32 || stderr != "" {
			t.Fatalf("exit %d: %q (%s)", code, out, stderr)
		}
		keys[key] = true
	}
	if len(keys) != 2 {
		t.Error("two runs printed the same key")
	}
}

// A new app's .env, which git ignores, has development's own SECRET_KEY,
// and on Postgres the dev database's password: each app its own.
func TestNewEnv(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			env := func() map[string]string {
				root := t.TempDir()
				if code, _, stderr := gantry(t, root, "new", "shop", "--db", engine, "--skip-houston"); code != 0 {
					t.Fatalf("exit %d: %s", code, stderr)
				}
				ignored, _ := os.ReadFile(filepath.Join(root, "shop", ".gitignore"))
				if !slices.Contains(strings.Split(string(ignored), "\n"), ".env") {
					t.Errorf(".gitignore doesn't ignore .env:\n%s", ignored)
				}
				data, _ := os.ReadFile(filepath.Join(root, "shop", ".env"))
				values := map[string]string{}
				for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
					if name, value, ok := strings.Cut(line, "="); ok && !strings.HasPrefix(line, "#") {
						values[name] = value
					}
				}
				return values
			}
			first, second := env(), env()
			want := map[string]int{"SECRET_KEY": 32}
			if engine == "postgres" {
				want["POSTGRES_PASSWORD"] = 16
			}
			if len(first) != len(want) {
				t.Errorf(".env = %q, want %v", first, want)
			}
			for name, size := range want {
				if raw, err := hex.DecodeString(first[name]); err != nil || len(raw) != size {
					t.Errorf("%s = %q, want %d random bytes in hex", name, first[name], size)
				}
				if first[name] == second[name] {
					t.Errorf("two apps got the same %s", name)
				}
			}
		})
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

// AGENTS.md sends agents to the docs of the gantry go.mod requires, and its
// layout map is the README's.
func TestNewAgents(t *testing.T) {
	was := releaseVersion
	t.Cleanup(func() { releaseVersion = was })
	releaseVersion = "v9.8.7"
	root := t.TempDir()
	if code, _, stderr := gantry(t, root, "new", "shop", "--skip-houston"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	agents, err := os.ReadFile(filepath.Join(root, "shop", "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"blob/v9.8.7/docs/agents.md", "blob/v9.8.7/docs/strict.md", "bin/gantry check --strict", "bin/gantry test"} {
		if !strings.Contains(string(agents), want) {
			t.Errorf("AGENTS.md has no %s:\n%s", want, agents)
		}
	}
	if strings.Contains(string(agents), "blob/main/") {
		t.Error("AGENTS.md links to docs on main, not the app's version")
	}
	readme, _ := os.ReadFile(filepath.Join(root, "shop", "README.md"))
	layout := func(doc []byte) string {
		_, after, _ := strings.Cut(string(doc), "## Where things go\n")
		section, _, _ := strings.Cut(after, "\n## ")
		return section
	}
	if layout(agents) == "" || layout(agents) != layout(readme) {
		t.Errorf("AGENTS.md's layout map isn't the README's:\n%s\n---\n%s", layout(agents), layout(readme))
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

// --gantry: go.work uses the checkout, and the dev container mounts the app
// and the checkout under /app, the same path apart as on the host, so
// go.work's use finds the checkout there too. Nothing is mounted over the
// container's own folders, however far up or away the checkout is.
func TestNewGantryCheckout(t *testing.T) {
	for _, tc := range []struct {
		in, checkout string // where the app is made and where the checkout is, under a temporary root
		use          string // go.work's use: the checkout, from the app
	}{
		{in: ".", checkout: "gantry", use: "../gantry"},
		{in: "gantry", checkout: "gantry", use: ".."},
		{in: "apps", checkout: ".", use: "../.."},
		{in: "apps", checkout: "x", use: "../../x"},
		{in: "home/me/apps", checkout: "src/gantry", use: "../../../../src/gantry"},
	} {
		t.Run(tc.use, func(t *testing.T) {
			root := t.TempDir()
			in, checkout := filepath.Join(root, tc.in), filepath.Join(root, tc.checkout)
			os.MkdirAll(in, 0o755)
			os.MkdirAll(checkout, 0o755)
			os.WriteFile(filepath.Join(checkout, "go.mod"), []byte("module github.com/scttymn/gantry\n"), 0o644)
			if code, _, stderr := gantry(t, in, "new", "shop", "--gantry", checkout, "--skip-houston"); code != 0 {
				t.Fatal(stderr)
			}
			dir := filepath.Join(in, "shop")
			work, _ := os.ReadFile(filepath.Join(dir, "go.work"))
			if !strings.Contains(string(work), "use (\n\t.\n\t"+tc.use+"\n)") {
				t.Fatalf("go.work doesn't use %s:\n%s", tc.use, work)
			}

			raw, _ := os.ReadFile(filepath.Join(dir, "compose.yml"))
			var compose struct {
				Services map[string]struct{ Volumes []string }
			}
			if err := yaml.Unmarshal(raw, &compose); err != nil {
				t.Fatal(err)
			}
			mounts := map[string]string{} // a folder on the host: where the container has it
			for _, v := range compose.Services["app"].Volumes {
				source, target, _ := strings.Cut(v, ":")
				if !strings.HasPrefix(source, ".") && !strings.HasPrefix(source, "/") {
					continue // a named volume
				}
				if target != "/app" && !strings.HasPrefix(target, "/app/") {
					t.Errorf("%s is mounted at %s, outside /app", source, target)
				}
				if !filepath.IsAbs(source) {
					source = filepath.Join(dir, source)
				}
				mounts[source] = target
			}
			app, gantryAt := mounts[dir], mounts[checkout]
			if app == "" || gantryAt == "" {
				t.Fatalf("the app and the checkout aren't both mounted: %q\n%s", mounts, raw)
			}
			if got := path.Join(app, tc.use); got != gantryAt {
				t.Errorf("in the container, go.work's use is %s, and the checkout is at %s", got, gantryAt)
			}
			docker, _ := os.ReadFile(filepath.Join(dir, "Dockerfile"))
			_, dev, _ := strings.Cut(string(docker), "AS dev\n")
			dev, _, _ = strings.Cut(dev, "\nFROM ")
			if !strings.Contains(dev, "WORKDIR "+app+"\n") || !strings.Contains(dev, `"-path", "`+app+`"`) {
				t.Errorf("the dev stage doesn't work in the app's mount, %s:\n%s", app, dev)
			}
			// The test and production builds copy the app to /app, and build
			// against go.mod's gantry, not the checkout.
			if others := strings.Replace(string(docker), dev, "", 1); strings.Contains(others, app+" ") || strings.Contains(others, app+"\n") {
				t.Errorf("a stage besides dev uses the dev container's %s", app)
			}
			ignored, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
			if !strings.Contains(string(ignored), "go.work\n") {
				t.Error("go.work isn't ignored by git")
			}
		})
	}
}

// gantry db, task and tasks are the app's own commands, in its container;
// anything else is Houston's.
func TestAppCommands(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "cmd", "shop"), 0o755)
	steps(t, "")
	if code, _, stderr := gantry(t, root, "verify"); code != 1 || !strings.Contains(stderr, "no test/features.yaml") {
		t.Errorf("verify without a feature map: exit %d: %s", code, stderr)
	}
	os.MkdirAll(filepath.Join(root, "test"), 0o755)
	os.WriteFile(filepath.Join(root, "test", "features.yaml"), []byte("features: []\n"), 0o644)
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{[]string{"db", "migrate"}, []string{"houston", "exec", "go", "run", "./cmd/shop", "db", "migrate"}},
		{[]string{"db", "rollback", "2"}, []string{"houston", "exec", "go", "run", "./cmd/shop", "db", "rollback", "2"}},
		{[]string{"task", "backfill", "--dry-run"}, []string{"houston", "exec", "go", "run", "./cmd/shop", "task", "backfill", "--dry-run"}},
		{[]string{"tasks"}, []string{"houston", "exec", "go", "run", "./cmd/shop", "tasks"}},
		{[]string{"db", "migrate", "--local"}, []string{"env", "GANTRY_ENV=development", "go", "run", "./cmd/shop", "db", "migrate"}},
		{[]string{"routes"}, []string{"houston", "exec", "go", "run", "./cmd/shop", "routes"}},
		{[]string{"routes", "--json", "--local"}, []string{"env", "GANTRY_ENV=development", "go", "run", "./cmd/shop", "routes", "--json"}},
		{[]string{"verify"}, []string{"houston", "exec", "go", "test", "-count=1", "-v", "-run", "^TestFeatures$", "./app/"}},
		{[]string{"verify", "--local"}, []string{"go", "test", "-count=1", "-v", "-run", "^TestFeatures$", "./app/"}},
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

// A new app's test stage compiles it as its production build does, with
// nodevcheck, so a call that only development builds have fails at gantry
// test, not at deploy.
func TestNewAppProductionBuild(t *testing.T) {
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
	mod, _ := os.ReadFile(filepath.Join(dir, "go.mod"))
	os.WriteFile(filepath.Join(dir, "go.mod"), append(mod, "\nreplace github.com/scttymn/gantry => "+repo+"\n"...), 0o644)
	run := func(args ...string) (string, error) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := run("templ", "generate"); err != nil {
		t.Fatalf("templ generate: %v\n%s", err, out)
	}
	if out, err := run("go", "build", "-tags", "nodynamic,nodevcheck", "./..."); err != nil {
		t.Fatalf("a new app's production build: %v\n%s", err, out)
	}
	os.MkdirAll(filepath.Join(dir, "internal", "devrules"), 0o755)
	os.WriteFile(filepath.Join(dir, "internal", "devrules", "devrules.go"), []byte(`package devrules

import (
	"net/http"

	"github.com/scttymn/gantry/web"
)

func Wrap(h http.Handler) http.Handler { return web.DevelopmentWithRules(h, ".", nil, nil) }
`), 0o644)
	if out, err := run("go", "vet", "./internal/devrules"); err != nil {
		t.Fatalf("the development build should compile the call: %v\n%s", err, out)
	}
	if out, err := run("go", "build", "-tags", "nodynamic,nodevcheck", "./..."); err == nil || !strings.Contains(out, "undefined: web.DevelopmentWithRules") {
		t.Fatalf("the production build should fail naming web.DevelopmentWithRules: %v\n%s", err, out)
	}
}

// A new app's feature map reads, as do the features g resource and g auth
// print for it once pasted. TestGeneratorMatrix runs TestFeatures in each
// app it builds, on its routes.
func TestFeatureMap(t *testing.T) {
	dir := shopApp(t, "sqlite")
	path := filepath.Join(dir, "test", "features.yaml")
	var names []string
	for _, f := range testkit.Features(t, path) {
		names = append(names, f.Name)
	}
	if want := []string{"home", "health check", "live updates"}; !reflect.DeepEqual(names, want) {
		t.Errorf("a new app's features: %q, want %q", names, want)
	}
	for _, args := range [][]string{{"g", "resource", "posts", "title:string"}, {"g", "resource", "admin/events", "name:string"}, {"g", "auth"}} {
		code, out, stderr := gantry(t, dir, args...)
		if code != 0 {
			t.Fatalf("%q: %s", args, stderr)
		}
		feature(t, dir, out)
	}
	features := testkit.Features(t, path)
	want := []testkit.Feature{
		{Name: "posts", Routes: []string{"/posts", "/posts/"}, Role: testkit.Public, Visit: []testkit.Visit{{Path: "/posts", Selector: "h1"}}},
		{Name: "admin/events", Routes: []string{"/admin/events", "/admin/events/"}, Role: testkit.Public, Visit: []testkit.Visit{{Path: "/admin/events", Selector: "h1"}}},
		{Name: "sign-in", Routes: []string{"/login", "/login/", "/logout", "/passwords", "/passwords/"}, Role: testkit.Public, Visit: []testkit.Visit{{Path: "/login", Selector: "form"}}},
	}
	if got := features[3:]; !reflect.DeepEqual(got, want) {
		t.Errorf("the generators' features:\n got %+v\nwant %+v", got, want)
	}
}
