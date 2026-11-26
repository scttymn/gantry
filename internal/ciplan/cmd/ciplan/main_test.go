package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	root := t.TempDir()
	for name, text := range map[string]string{
		"go.mod":         "module example.com/m\n\ngo 1.22\n",
		"a/a.go":         "package a\n",
		"bin/shellcheck": "#!/bin/sh\necho bin/shellcheck\n",
	} {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("add", ".")
	git("commit", "-q", "-m", "one")
	git("checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(root, "a/a.go"), []byte("package a\n\nconst X = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("commit", "-q", "-am", "two")

	plan := func(args ...string) (string, string, error) {
		var stdout, stderr bytes.Buffer
		err := run(append([]string{"-root", root, "-format", "github"}, args...), &stdout, &stderr)
		return stdout.String(), stderr.String(), err
	}
	t.Run("a change: its checks", func(t *testing.T) {
		out, _, err := plan("-base", "main")
		if err != nil || !strings.Contains(out, "any=true\nfull=false\n") || !strings.Contains(out, "packages=./a\n") {
			t.Errorf("ciplan -base main: %v\n%s", err, out)
		}
	})
	t.Run("no change: nothing", func(t *testing.T) {
		out, _, err := plan("-base", "feature")
		if err != nil || !strings.Contains(out, "any=false\n") {
			t.Errorf("ciplan -base feature: %v\n%s", err, out)
		}
	})
	t.Run("git can't say what changed: every check", func(t *testing.T) {
		out, stderr, err := plan("-base", "origin/nope")
		if err != nil || !strings.Contains(out, "any=true\nfull=true\n") || !strings.Contains(stderr, "planning every check") {
			t.Errorf("ciplan -base origin/nope: %v\n%s\n%s", err, out, stderr)
		}
	})
	t.Run("-full", func(t *testing.T) {
		out, _, err := plan("-full")
		if err != nil || !strings.Contains(out, "full=true\n") || !strings.Contains(out, "shell_scripts=bin/shellcheck\n") {
			t.Errorf("ciplan -full: %v\n%s", err, out)
		}
	})
	t.Run("neither -full nor -base: an error", func(t *testing.T) {
		if out, _, err := plan(); err == nil {
			t.Errorf("ciplan: no error\n%s", out)
		}
	})
	t.Run("a module go can't list: an error, not a plan", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/m\n\ngo 1.22\n\nrequire (\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		defer git("checkout", "-q", "--", "go.mod")
		if out, _, err := plan("-base", "main"); err == nil {
			t.Errorf("ciplan with a broken go.mod: no error\n%s", out)
		}
	})
}
