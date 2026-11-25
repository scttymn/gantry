package gantry_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/scttymn/gantry/internal/pins"
)

func TestBinCIRunsTheCIWorkflow(t *testing.T) {
	read := func(file string) string {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	workflow, script := read(".github/workflows/ci.yml"), read("bin/ci")
	names := func(text, re string) (found []string) {
		for _, m := range regexp.MustCompile(re).FindAllStringSubmatch(text, -1) {
			found = append(found, m[1])
		}
		return found
	}
	steps := names(workflow, `(?m)^\s+- name: (.+)$`)
	ran := names(script, `(?m)^\s*step "(.+)"$`)
	if len(steps) == 0 || len(ran) == 0 {
		t.Fatalf("ci.yml has %d steps and bin/ci %d: neither should be none", len(steps), len(ran))
	}
	for _, s := range steps {
		if !slices.Contains(ran, s) {
			t.Errorf("ci.yml's step %q isn't in bin/ci: run it there too, as step %q", s, s)
		}
	}
	for _, s := range ran {
		if !slices.Contains(steps, s) {
			t.Errorf("bin/ci's step %q isn't one of ci.yml's", s)
		}
	}

	for what, re := range map[string]string{
		"tool version":           `go run ([\w.-]+(?:/[\w.-]+)+@v[\d.]+)`,
		"linker flags":           `-ldflags "[^"]+"`,
		"race detector":          `go test -race`,
		"bench module":           `GANTRY_DIR=\S+`,
		"modules tidied":         `for module in [^;]+; do`,
		"release tag":            `(?m)^\s+VERSION: (\S+)$`,
		"release target":         `--target release\b`,
		"new app probe":          `new probe --skip-houston`,
		"Houston's digests":      `houston-release --sha256`,
		"vulnerabilities' check": `jq -rs ('[^']+')`,
		"shellcheck's script":    `bin/shellcheck`,
		"TLA+ script":            `bin/tlc`,
		"fuzz target":            `-fuzz=(\^Fuzz\w+\$)`,
	} {
		found := regexp.MustCompile(re).FindAllStringSubmatch(workflow, -1)
		if len(found) == 0 {
			t.Errorf("ci.yml has no %s any more (%s): take it out of TestBinCIRunsTheCIWorkflow", what, re)
		}
		for _, m := range found {
			want := m[len(m)-1]
			if !regexp.MustCompile(regexp.QuoteMeta(want) + `([^\w./-]|$)`).MatchString(script) {
				t.Errorf("ci.yml's %s %s isn't in bin/ci", what, want)
			}
		}
	}
}

const fakeDocker = `#!/bin/sh
echo "$*" >>"$FAKE_DOCKER_LOG"
case "$*" in *"${FAKE_FAIL:-no such call}"*) exit 1 ;; esac
case "$*" in *tlc2.TLC*)
	while [ "$1" != tlc ]; do shift; done
	shift 3
	for cfg; do
		read -r expect <"spec/$cfg"
		[ -z "${FAKE_TLC_PASSES:-}" ] || expect=pass
		echo "@@ start $cfg 1000000000"
		[ -z "${FAKE_TLC_SILENT:-}" ] || { echo "@@ end $cfg 255 1500000000"; continue; }
		case "$expect" in
		*violates*) echo "Error: Invariant ${expect##* } is violated."; echo "9 states generated, 5 distinct states found, 2 states left on queue."; echo "@@ end $cfg 12 1500000000" ;;
		*) echo "7 states generated, 5 distinct states found, 0 states left on queue."; echo "@@ end $cfg 0 1500000000" ;;
		esac
	done
	exit 0 ;;
esac
case "$1 $2" in
"ps "*) exit "${FAKE_DOCKER_PS:-0}" ;;
"network create") exit "${FAKE_POSTGRES:-1}" ;;
"build "*) echo "built sha256:fake$$" >>"$FAKE_DOCKER_LOG"; echo "sha256:fake$$" ;;
esac
`

func binCI(t *testing.T, env []string, without ...string) (string, []string, error) {
	t.Helper()
	bin, home := t.TempDir(), t.TempDir()
	log := filepath.Join(home, "docker.log")
	stubs := map[string]string{
		"docker":    fakeDocker,
		"df":        "#!/bin/sh\nprintf 'Filesystem 1024-blocks Used Available Capacity Mounted\\nfake 1 1 %s 1%% /\\n' \"${FAKE_FREE_KB:-104857600}\"\n",
		"jq":        "#!/bin/sh\n",
		"du":        "#!/bin/sh\nprintf '%s\\t%s\\n' \"${FAKE_DU_KB:-0}\" \"$2\"\n",
		"git":       "#!/bin/sh\nprintf 'bin/ci\\0'\n",
		"curl":      "#!/bin/sh\nwhile [ $# -gt 0 ]; do case \"$1\" in -o) : >\"$2\"; shift ;; esac; shift; done\n",
		"sha256sum": "#!/bin/sh\nprintf '" + pins.TLASHA256 + "  %s\\n' \"$1\"\n",
	}
	for _, tool := range []string{"docker", "df", "git", "jq", "curl", "sha256sum", "bash", "sh", "awk", "basename", "cat", "cut", "dirname", "du", "mkdir", "mktemp", "mv", "rm", "seq", "sleep", "timeout", "tr", "uname"} {
		if slices.Contains(without, tool) {
			continue
		}
		dst := filepath.Join(bin, tool)
		if stub, ok := stubs[tool]; ok {
			if err := os.WriteFile(dst, []byte(stub), 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		src, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("no %s here to run bin/ci with", tool)
		}
		if err := os.Symlink(src, dst); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("bin/ci", "--fast")
	cmd.Env = append([]string{"PATH=" + bin, "HOME=" + home, "FAKE_DOCKER_LOG=" + log}, env...)
	out, err := cmd.CombinedOutput()
	calls, _ := os.ReadFile(log)
	return string(out), strings.Split(strings.TrimSpace(string(calls)), "\n"), err
}

func called(calls []string, part string) (found []string) {
	for _, c := range calls {
		if strings.Contains(c, part) {
			found = append(found, c)
		}
	}
	return found
}

func TestBinCIPostgres(t *testing.T) {
	t.Run("a throwaway one, removed after a failure too", func(t *testing.T) {
		out, calls, err := binCI(t, []string{"FAKE_POSTGRES=0", "FAKE_FAIL=go vet"})
		if err == nil {
			t.Fatalf("bin/ci passed with go vet failing:\n%s", out)
		}
		tests := called(calls, "go test")
		if len(tests) == 0 {
			t.Fatalf("bin/ci ran no tests:\n%s", out)
		}
		for _, c := range tests {
			if !strings.Contains(c, "GANTRY_TEST_POSTGRES_URL=postgres://") {
				t.Errorf("a test ran without the throwaway Postgres: docker %s", c)
			}
		}
		if len(called(calls, "golangci-lint")) > 0 {
			t.Errorf("bin/ci went on after go vet failed:\n%s", out)
		}
		if len(called(calls, "rm -f gantry-ci-pg-")) == 0 || len(called(calls, "network rm gantry-ci-")) == 0 {
			t.Errorf("bin/ci left its Postgres or its network: %q", calls)
		}
	})
	t.Run("none: the tests skip, even with CI set", func(t *testing.T) {
		out, calls, err := binCI(t, []string{"CI=true"})
		if err != nil {
			t.Fatalf("bin/ci failed without a Postgres: %v\n%s", err, out)
		}
		if !strings.Contains(out, "NO POSTGRES") {
			t.Errorf("bin/ci didn't say it has no Postgres:\n%s", out)
		}
		for _, c := range called(calls, "go test") {
			if !strings.Contains(c, " CI= ") {
				t.Errorf("a test without Postgres ran with CI set, so it fails rather than skips: docker %s", c)
			}
		}
	})
	t.Run("none, in GitHub Actions: a failure", func(t *testing.T) {
		out, calls, err := binCI(t, []string{"CI=true", "GITHUB_ACTIONS=true"})
		if err == nil || len(called(calls, "go test")) > 0 {
			t.Errorf("bin/ci in GitHub Actions without a Postgres: err %v, tests %q\n%s", err, called(calls, "go test"), out)
		}
	})
}

func TestBinCICleansTheCacheAlone(t *testing.T) {
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name   string
		other  int
		cleans bool
	}{
		{"alone", 0, true},
		{"beside a run that has gone", gone.Process.Pid, true},
		{"beside another running", os.Getpid(), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			cache := filepath.Join(home, ".cache/gantry")
			if runtime.GOOS == "darwin" {
				cache = filepath.Join(home, "Library/Caches/gantry")
			}
			for _, dir := range []string{"build", "runs"} {
				if err := os.MkdirAll(filepath.Join(cache, dir), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if c.other != 0 {
				if err := os.WriteFile(filepath.Join(cache, "runs", strconv.Itoa(c.other)), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			out, calls, err := binCI(t, []string{"HOME=" + home, "FAKE_DU_KB=6000000"})
			if err != nil {
				t.Fatalf("bin/ci: %v\n%s", err, out)
			}
			if cleaned := len(called(calls, "go clean -cache")) > 0; cleaned != c.cleans {
				t.Errorf("bin/ci cleaned the cache: %v, want %v\n%s", cleaned, c.cleans, out)
			}
			marks, _ := os.ReadDir(filepath.Join(cache, "runs"))
			if want := map[bool]int{true: 0, false: 1}[c.cleans]; len(marks) != want {
				t.Errorf("%d marks left in runs/, want %d (its own and a gone run's removed)", len(marks), want)
			}
			if _, err := os.Stat(filepath.Join(cache, "cleaning")); err == nil {
				t.Error("bin/ci left the cleaning lock")
			}
		})
	}
}

func TestBinCIStopsFirst(t *testing.T) {
	for name, c := range map[string]struct {
		env     []string
		without []string
		says    string
	}{
		"Docker down":      {env: []string{"FAKE_DOCKER_PS=1"}, says: "Docker"},
		"no jq":            {without: []string{"jq"}, says: "jq"},
		"under 20 GB free": {env: []string{"FAKE_FREE_KB=1048576"}, says: "20 GB"},
	} {
		t.Run(name, func(t *testing.T) {
			out, calls, err := binCI(t, c.env, c.without...)
			if err == nil || !strings.Contains(out, c.says) {
				t.Errorf("bin/ci: err %v, and it should have said %q:\n%s", err, c.says, out)
			}
			if b := called(calls, "build"); len(b) > 0 {
				t.Errorf("bin/ci built anyway: %q", b)
			}
		})
	}
}

func TestScriptsRunTheirOwnBuild(t *testing.T) {
	ownBuild := func(t *testing.T, out string, calls []string) {
		t.Helper()
		var built []string
		for _, c := range calls {
			if id, ok := strings.CutPrefix(c, "built "); ok {
				built = append(built, id)
			}
		}
		runs := 0
		for _, c := range calls {
			if !strings.HasPrefix(c, "run ") {
				continue
			}
			runs++
			if !slices.ContainsFunc(strings.Fields(c), func(w string) bool { return slices.Contains(built, w) || strings.Contains(w, "@sha256:") }) {
				t.Errorf("docker %s\nruns neither an image its build made (%q) nor one by its digest", c, built)
			}
		}
		if len(built) == 0 || runs == 0 {
			t.Fatalf("%d builds and %d runs, want both:\n%s", len(built), runs, out)
		}
	}
	t.Run("bin/ci", func(t *testing.T) {
		out, calls, err := binCI(t, []string{"FAKE_POSTGRES=0"})
		if err != nil {
			t.Fatalf("bin/ci: %v\n%s", err, out)
		}
		ownBuild(t, out, calls)
	})
	for script, args := range map[string][]string{"bin/go": {"go", "version"}, "bin/bench": {"-quick"}} {
		t.Run(script, func(t *testing.T) {
			root, bin := t.TempDir(), t.TempDir()
			body, err := os.ReadFile(script)
			if err != nil {
				t.Fatal(err)
			}
			for path, text := range map[string]string{filepath.Join(root, script): string(body), filepath.Join(bin, "docker"): fakeDocker} {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(text), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			log := filepath.Join(bin, "docker.log")
			cmd := exec.Command(filepath.Join(root, script), args...)
			cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "FAKE_DOCKER_LOG="+log, "FAKE_POSTGRES=0")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%s: %v\n%s", script, err, out)
			}
			calls, _ := os.ReadFile(log)
			ownBuild(t, string(out), strings.Split(strings.TrimSpace(string(calls)), "\n"))
		})
	}
}

var specExpect = regexp.MustCompile(`^\\\* expect: (pass|violates ([A-Za-z_][A-Za-z0-9_]*))$`)

func TestSpecConfigsSayWhatTheyExpect(t *testing.T) {
	cfgs, err := filepath.Glob("spec/*.cfg")
	if err != nil || len(cfgs) == 0 {
		t.Fatalf("no spec/*.cfg: %v", err)
	}
	for _, cfg := range cfgs {
		text, err := os.ReadFile(cfg)
		if err != nil {
			t.Fatal(err)
		}
		base, variant, _ := strings.Cut(strings.TrimSuffix(filepath.Base(cfg), ".cfg"), ".")
		spec, err := os.ReadFile(filepath.Join("spec", base+".tla"))
		if err != nil {
			t.Errorf("%s has no spec: bin/tlc runs spec/<Name>.cfg and spec/<Name>.*.cfg with spec/<Name>.tla, and there's no spec/%s.tla", cfg, base)
			continue
		}
		first, _, _ := strings.Cut(string(text), "\n")
		m := specExpect.FindStringSubmatch(first)
		if m == nil {
			t.Errorf("%s's first line is %q; bin/tlc wants \\* expect: pass or \\* expect: violates <Invariant>", cfg, first)
			continue
		}
		violated := m[2]
		switch {
		case strings.HasPrefix(variant, "Mut") && (violated == "" || !regexp.MustCompile(`(?m)^\s+`+variant+` = TRUE$`).Match(text)):
			t.Errorf("%s is a control: it sets %s = TRUE and expects the invariant that catches it violated", cfg, variant)
		case strings.HasPrefix(variant, "Reach") && violated != variant:
			t.Errorf("%s is a reachability witness: it expects %s violated", cfg, variant)
		case violated != "" && !regexp.MustCompile(`(?m)^INVARIANT `+violated+`$`).Match(text):
			t.Errorf("%s expects %s violated, so that's its one invariant (INVARIANT %s): TLC stops at the first it finds", cfg, violated, violated)
		case violated != "" && !regexp.MustCompile(`(?m)^`+violated+` ==`).Match(spec):
			t.Errorf("%s expects %s violated, and spec/%s.tla doesn't define it", cfg, violated, base)
		}
	}
}

func TestBinTLC(t *testing.T) {
	script, err := os.ReadFile("bin/tlc")
	if err != nil {
		t.Fatal(err)
	}
	spec := map[string]string{
		"M.tla":      "---- MODULE M ----\n====\n",
		"M.cfg":      "\\* expect: pass\nSPECIFICATION Spec\n",
		"M.MutX.cfg": "\\* expect: violates Safe\nSPECIFICATION Spec\nINVARIANT Safe\n",
		"N.tla":      "---- MODULE N ----\n====\n",
		"N.cfg":      "\\* expect: pass\nSPECIFICATION Spec\n",
	}
	tlc := func(t *testing.T, extra map[string]string, env []string, args ...string) (string, []string, error) {
		t.Helper()
		root, bin, home := t.TempDir(), t.TempDir(), t.TempDir()
		files := map[string]string{"bin/tlc": string(script)}
		for name, text := range spec {
			files["spec/"+name] = text
		}
		for name, text := range extra {
			files["spec/"+name] = text
		}
		for name, text := range files {
			path := filepath.Join(root, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(text), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		stubs := map[string]string{
			"docker":    fakeDocker,
			"curl":      "#!/bin/sh\nwhile [ $# -gt 0 ]; do case \"$1\" in -o) : >\"$2\"; shift ;; esac; shift; done\n",
			"sha256sum": "#!/bin/sh\nprintf '" + pins.TLASHA256 + "  %s\\n' \"$1\"\n",
		}
		for _, tool := range []string{"docker", "curl", "sha256sum", "bash", "sh", "awk", "basename", "dirname", "mkdir", "mktemp", "mv", "rm", "timeout", "uname"} {
			dst := filepath.Join(bin, tool)
			if stub, ok := stubs[tool]; ok {
				if err := os.WriteFile(dst, []byte(stub), 0o755); err != nil {
					t.Fatal(err)
				}
				continue
			}
			src, err := exec.LookPath(tool)
			if err != nil {
				t.Skipf("no %s here to run bin/tlc with", tool)
			}
			if err := os.Symlink(src, dst); err != nil {
				t.Fatal(err)
			}
		}
		log := filepath.Join(home, "docker.log")
		cmd := exec.Command(filepath.Join(root, "bin/tlc"), args...)
		cmd.Env = append([]string{"PATH=" + bin, "HOME=" + home, "FAKE_DOCKER_LOG=" + log}, env...)
		out, err := cmd.CombinedOutput()
		calls, _ := os.ReadFile(log)
		return string(out), strings.Split(strings.TrimSpace(string(calls)), "\n"), err
	}
	t.Run("each config as it expects", func(t *testing.T) {
		out, calls, err := tlc(t, nil, nil)
		if err != nil {
			t.Fatalf("bin/tlc: %v\n%s", err, out)
		}
		for _, cfg := range []string{"M.cfg", "M.MutX.cfg", "N.cfg"} {
			if !regexp.MustCompile(`(?m)^ok +` + regexp.QuoteMeta(cfg) + ` .* 5 states +0\.5s$`).MatchString(out) {
				t.Errorf("no line saying %s did as it expects, with its states and time:\n%s", cfg, out)
			}
		}
		if runs, m := called(calls, "--name gantry-tlc-"), called(calls, ".jar M.tla M.cfg M.MutX.cfg"); len(runs) != 2 || len(m) != 1 {
			t.Errorf("want one named container a spec, M's with both its configs: runs %q, M's %q", runs, m)
		}
	})
	t.Run("a control that passes fails", func(t *testing.T) {
		out, _, err := tlc(t, nil, []string{"FAKE_TLC_PASSES=1"})
		if err == nil || !regexp.MustCompile(`(?m)^MISMATCH +M\.MutX\.cfg +expect violates Safe +got pass`).MatchString(out) {
			t.Errorf("bin/tlc with a control that found nothing: err %v\n%s", err, out)
		}
	})
	t.Run("TLC that prints nothing", func(t *testing.T) {
		out, _, err := tlc(t, nil, []string{"FAKE_TLC_SILENT=1"})
		if err == nil || !regexp.MustCompile(`(?m)^MISMATCH +M\.cfg +expect pass +got exit 255`).MatchString(out) || !strings.Contains(out, "in TLC") {
			t.Errorf("bin/tlc whose TLC said nothing: err %v\n%s", err, out)
		}
	})
	t.Run("a config with no expect line", func(t *testing.T) {
		out, calls, err := tlc(t, map[string]string{"M.Bad.cfg": "SPECIFICATION Spec\n"}, nil)
		if err == nil || !strings.Contains(out, "M.Bad.cfg's first line") || len(called(calls, "tlc2.TLC")) > 0 {
			t.Errorf("bin/tlc with a config that says nothing: err %v, runs %q\n%s", err, called(calls, "tlc2.TLC"), out)
		}
	})
	t.Run("a config without its spec", func(t *testing.T) {
		out, _, err := tlc(t, map[string]string{"Gone.Reach.cfg": "\\* expect: pass\n"}, nil)
		if err == nil || !strings.Contains(out, "has no spec (spec/Gone.tla)") {
			t.Errorf("bin/tlc with a config whose spec isn't there: err %v\n%s", err, out)
		}
	})
	t.Run("a failed run's container is removed", func(t *testing.T) {
		out, calls, err := tlc(t, nil, []string{"FAKE_FAIL=tlc2.TLC"})
		if err == nil || len(called(calls, "rm -f gantry-tlc-")) == 0 {
			t.Errorf("bin/tlc whose TLC container failed: err %v, calls %q\n%s", err, calls, out)
		}
	})
	t.Run("SPEC... checks only those", func(t *testing.T) {
		out, calls, err := tlc(t, nil, nil, "spec/N.tla")
		if runs := called(calls, "--name gantry-tlc-"); err != nil || len(runs) != 1 || len(called(calls, ".jar N.tla N.cfg")) != 1 {
			t.Errorf("bin/tlc spec/N.tla: err %v, runs %q\n%s", err, runs, out)
		}
	})
	t.Run("--list", func(t *testing.T) {
		out, _, err := tlc(t, nil, nil, "--list")
		if err != nil || out != "spec/M.tla\nspec/N.tla\n" {
			t.Errorf("bin/tlc --list: %v\n%s", err, out)
		}
	})
}

func TestPrePushHook(t *testing.T) {
	hook, err := filepath.Abs(".githooks/pre-push")
	if err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	if err := os.MkdirAll(filepath.Join(repo, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	ran := filepath.Join(t.TempDir(), "ran")
	if err := os.WriteFile(filepath.Join(repo, "bin/ci"), []byte("#!/bin/sh\ntouch \""+ran+"\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "one")
	first := git("rev-parse", "HEAD")
	git("commit", "-q", "--allow-empty", "-m", "two")
	head := git("rev-parse", "HEAD")
	zero := strings.Repeat("0", len(head))

	push := func(t *testing.T, lines string) (string, bool, error) {
		t.Helper()
		os.Remove(ran)
		cmd := exec.Command(hook, "origin", "url")
		cmd.Dir = repo
		cmd.Stdin = strings.NewReader(lines)
		out, err := cmd.CombinedOutput()
		_, statErr := os.Stat(ran)
		return string(out), statErr == nil, err
	}
	for _, c := range []struct {
		name   string
		dirty  bool
		lines  string
		passes bool
		runs   bool
	}{
		{"HEAD, clean: bin/ci", false, "refs/heads/main " + head + " refs/heads/main " + zero + "\n", true, true},
		{"deletes only: nothing to check", true, "(delete) " + zero + " refs/heads/gone " + head + "\n", true, false},
		{"another commit: refused", false, "refs/heads/old " + first + " refs/heads/old " + zero + "\n", false, false},
		{"changes in the tree: refused", true, "refs/heads/main " + head + " refs/heads/main " + zero + "\n", false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			untracked := filepath.Join(repo, "stray.go")
			if c.dirty {
				if err := os.WriteFile(untracked, []byte("package stray\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				defer os.Remove(untracked)
			}
			out, runs, err := push(t, c.lines)
			if (err == nil) != c.passes || runs != c.runs {
				t.Errorf("hook: err %v, ran bin/ci %v; want passes %v, runs %v\n%s", err, runs, c.passes, c.runs, out)
			}
		})
	}
}
