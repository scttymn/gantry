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
)

// bin/ci runs on the machine what the ci workflow runs on a pull request, and
// the pre-push hook runs bin/ci. A step added to ci.yml fails here until bin/ci
// runs it too, under the same name; bin/ci has no step ci.yml hasn't. The C
// flags, build tags and Postgres image are internal/pins' (TestCopiesMatchTheirPin).
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

	// What the steps run that a step's name doesn't say: each one ci.yml has
	// (the first group, or the whole match) must be in bin/ci as it is there.
	for what, re := range map[string]string{
		"tool version":           `go run ([\w.-]+(?:/[\w.-]+)+@v[\d.]+)`,
		"docker image":           `docker run [^\n]*?(\S+@sha256:[0-9a-f]{64})`,
		"linker flags":           `-ldflags "[^"]+"`,
		"race detector":          `go test -race`,
		"bench module":           `GANTRY_DIR=\S+`,
		"release tag":            `(?m)^\s+VERSION: (\S+)$`,
		"release target":         `--target release\b`,
		"new app probe":          `new probe --skip-houston`,
		"Houston's digests":      `houston-release --sha256`,
		"vulnerabilities' check": `jq -rs ('[^']+')`,
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

// fakeDocker logs each call to $FAKE_DOCKER_LOG and succeeds, but for docker
// ps under FAKE_DOCKER_PS, a network (the throwaway Postgres's) unless
// FAKE_POSTGRES=0, and a call with $FAKE_FAIL in it.
const fakeDocker = `#!/bin/sh
echo "$*" >>"$FAKE_DOCKER_LOG"
case "$*" in *"${FAKE_FAIL:-no such call}"*) exit 1 ;; esac
case "$1 $2" in
"ps "*) exit "${FAKE_DOCKER_PS:-0}" ;;
"network create") exit "${FAKE_POSTGRES:-1}" ;;
esac
`

// binCI runs bin/ci --fast on fakeDocker, a df with FAKE_FREE_KB free (100 GB
// unless set) and a jq that finds nothing, with env and nothing of the test's
// own environment. Of the rest of PATH it has the tools bin/ci uses, less
// without. It returns bin/ci's output, docker's calls, and its error.
func binCI(t *testing.T, env []string, without ...string) (string, []string, error) {
	t.Helper()
	bin, home := t.TempDir(), t.TempDir()
	log := filepath.Join(home, "docker.log")
	stubs := map[string]string{
		"docker": fakeDocker,
		"df":     "#!/bin/sh\nprintf 'Filesystem 1024-blocks Used Available Capacity Mounted\\nfake 1 1 %s 1%% /\\n' \"${FAKE_FREE_KB:-104857600}\"\n",
		"jq":     "#!/bin/sh\n",
		"du":     "#!/bin/sh\nprintf '%s\\t%s\\n' \"${FAKE_DU_KB:-0}\" \"$2\"\n",
	}
	for _, tool := range []string{"docker", "df", "jq", "bash", "sh", "awk", "cat", "cut", "dirname", "du", "mkdir", "mktemp", "rm", "seq", "sleep", "timeout", "tr", "uname"} {
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

// Every checkout's bin/ci shares Go's build cache: one over 5 GB is cleaned,
// but not under another bin/ci's build, whose files it would take.
func TestBinCICleansTheCacheAlone(t *testing.T) {
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name   string
		other  int // another run's mark, by pid
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

// bin/ci stops before it starts what it can't finish.
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

// The pre-push hook runs bin/ci on the commit being pushed: it refuses a push
// of any other, or from a tree with changes bin/ci would test too.
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
