package ciplan

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
)

var real struct {
	once sync.Once
	in   Inputs
	err  error
}

func realInputs(t *testing.T) Inputs {
	t.Helper()
	real.once.Do(func() { real.in, real.err = Load(filepath.Join("..", "..")) })
	if real.err != nil {
		t.Fatal(real.err)
	}
	return real.in
}

func TestRealGraphGeneratedAppsImports(t *testing.T) {
	in := realInputs(t)
	for _, want := range []string{"auth", "jobs", "mail", "storage", "live", "images", "web", "db"} {
		if !slices.Contains(in.GeneratorImports, testMod+"/"+want) {
			t.Errorf("GeneratorImports %v, without %s, which a generated app imports", in.GeneratorImports, want)
		}
	}
	for _, imp := range in.GeneratorImports {
		dir := strings.TrimPrefix(imp, testMod+"/")
		got := Plan([]string{dir + "/x.go"}, in)
		if !slices.Contains(got.Packages, "./cmd/gantry") || !got.NeedRelease {
			t.Errorf("%s/x.go doesn't test ./cmd/gantry (TestGeneratorMatrix) and build the release, and a generated app imports %s: %+v", dir, imp, got)
		}
	}
	if got := Plan([]string{"auth/auth.go"}, in); !slices.Contains(got.Packages, "./cmd/gantry") {
		t.Errorf("auth/auth.go doesn't test ./cmd/gantry: %v", got.Packages)
	}
}

func TestRealGraphCGOImports(t *testing.T) {
	got := Plan([]string{"internal/pins/pins.go"}, realInputs(t))
	if !slices.Contains(got.Packages, "./db") || !got.NeedCGO {
		t.Errorf("db/sqlite_c.go, built with cgo, imports internal/pins, and its change doesn't test ./db with cgo: %+v", got)
	}
}

func TestRealGraphSpecs(t *testing.T) {
	in := realInputs(t)
	if got := Plan([]string{"jobs/recurring.go"}, in); !slices.Equal(got.TLASpecs, []string{"spec/Jobs.tla", "spec/Ticks.tla"}) {
		t.Errorf("jobs/recurring.go: TLASpecs %v", got.TLASpecs)
	}
	cfgs, err := filepath.Glob(filepath.Join("..", "..", "spec", "*.cfg"))
	if err != nil || len(cfgs) == 0 {
		t.Fatalf("no spec/*.cfg: %v", err)
	}
	for _, cfg := range cfgs {
		name := filepath.Base(cfg)
		want := "spec/" + name[:strings.IndexByte(name, '.')] + ".tla"
		if got := Plan([]string{"spec/" + name}, in); !slices.Equal(got.TLASpecs, []string{want}) {
			t.Errorf("spec/%s: TLASpecs %v, want [%s], the spec bin/tlc runs it with", name, got.TLASpecs, want)
		}
	}
}

func TestCIFilesPlanFull(t *testing.T) {
	workflow, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	ruled := map[string]func(Result) bool{
		"bin/tlc": func(r Result) bool { return slices.Equal(r.TLASpecs, realInputs(t).Specs) },
	}
	ran := regexp.MustCompile(`\bbin/[\w-]+|\./internal/[\w/]+/cmd/\w+`).FindAllString(string(workflow), -1)
	if len(ran) == 0 {
		t.Fatal("ci.yml runs no bin/ script or command")
	}
	for _, f := range ran {
		f = strings.TrimPrefix(f, "./")
		if _, err := os.Stat(filepath.Join("..", "..", f)); err != nil {
			continue
		}
		got := Plan([]string{f}, realInputs(t))
		if rule, ok := ruled[f]; ok && rule(got) || got.Full {
			continue
		}
		t.Errorf("ci.yml runs %s, and a change to it doesn't plan every check: add it to plansEveryCheck (internal/ciplan/plan.go), or a rule here for what does run it: %+v", f, got)
	}
}

func TestLoadFailsRatherThanPlanNothing(t *testing.T) {
	for name, c := range map[string]struct {
		files map[string]string
		says  string
	}{
		"scripts not listed": {map[string]string{"bin/shellcheck": "#!/bin/sh\nexit 1\n"}, "bin/shellcheck --list"},
		"go list fails":      {map[string]string{"go.mod": "module example.com/m\n\ngo 1.22\n\nrequire (\n"}, "go list"},
		"template imports a package that isn't": {
			map[string]string{"cmd/gantry/templates/x.go.tmpl": "import \"example.com/m/gone\"\n"},
			"cmd/gantry/templates/x.go.tmpl imports example.com/m/gone",
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := tempModule(t, c.files)
			if _, err := Load(root); err == nil || !strings.Contains(err.Error(), c.says) {
				t.Errorf("Load: %v, want an error saying %q", err, c.says)
			}
		})
	}
	t.Run("a template's import that is", func(t *testing.T) {
		root := tempModule(t, map[string]string{"cmd/gantry/templates/x.go.tmpl": "import \"example.com/m/a\"\n"})
		in, err := Load(root)
		if err != nil || !slices.Equal(in.GeneratorImports, []string{"example.com/m/a"}) || !slices.Equal(in.Scripts, []string{"bin/shellcheck"}) {
			t.Errorf("Load: %+v, %v", in, err)
		}
	})
}

func TestChanged(t *testing.T) {
	root := tempModule(t, nil)
	git(t, root, "checkout", "-q", "-b", "feature")
	git(t, root, "mv", "a/a.go", "b.go")
	git(t, root, "commit", "-q", "-m", "move")

	t.Run("a rename, as both paths", func(t *testing.T) {
		got, err := Changed(root, "main", false)
		if err != nil || !slices.Equal(got, []string{"a/a.go", "b.go"}) {
			t.Errorf("Changed: %q, %v; want the old path and the new", got, err)
		}
	})
	t.Run("the tree's changes, with worktree", func(t *testing.T) {
		write(t, root, map[string]string{"new.txt": "untracked\n", "go.mod": "module example.com/m\n\ngo 1.22\n// changed\n"})
		defer git(t, root, "checkout", "-q", "--", "go.mod")
		defer os.Remove(filepath.Join(root, "new.txt"))
		got, err := Changed(root, "main", true)
		if err != nil || !slices.Equal(got, []string{"a/a.go", "b.go", "go.mod", "new.txt"}) {
			t.Errorf("Changed: %q, %v", got, err)
		}
	})
	t.Run("a base that isn't", func(t *testing.T) {
		if got, err := Changed(root, "origin/nope", false); err == nil {
			t.Errorf("Changed: %q, want an error", got)
		}
	})
	t.Run("a base with no history in common", func(t *testing.T) {
		git(t, root, "checkout", "-q", "--orphan", "unrelated")
		git(t, root, "commit", "-q", "-m", "alone")
		defer git(t, root, "checkout", "-q", "feature")
		if got, err := Changed(root, "main", false); err == nil {
			t.Errorf("Changed: %q, want an error (no merge base)", got)
		}
	})
}

func tempModule(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	write(t, root, map[string]string{
		"go.mod":         "module example.com/m\n\ngo 1.22\n",
		"a/a.go":         "package a\n",
		"bin/shellcheck": "#!/bin/sh\necho bin/shellcheck\n",
	})
	write(t, root, files)
	git(t, root, "init", "-q", "-b", "main")
	git(t, root, "add", ".")
	git(t, root, "commit", "-q", "-m", "one")
	return root
}

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, text := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func git(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root, "-c", "safe.directory=*"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
