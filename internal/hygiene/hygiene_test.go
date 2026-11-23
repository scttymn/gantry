// Package hygiene holds checks on the repository's files themselves, the
// patterns an agent copies from whatever it finds here.
package hygiene

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// checkout is the repository's root, from this package's directory.
var checkout = filepath.Join("..", "..")

// git runs git in root. Without git, or a repository it can read, the test is
// skipped, or fails when CI is set: there a skip would pass unseen. bin/go
// can't read a worktree's repository, whose .git names a directory outside
// the container. safe.directory: CI's container runs as root on a checkout
// the runner's user owns.
func git(t *testing.T, root string, args ...string) []byte {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", root, "-c", "safe.directory=*"}, args...)...).Output()
	if err != nil {
		if ci, _ := strconv.ParseBool(os.Getenv("CI")); ci {
			t.Fatalf("git %s: %v", strings.Join(args, " "), err)
		}
		t.Skipf("git %s: %v", strings.Join(args, " "), err)
	}
	return out
}

// files calls each with every text file git tracks under root, by its
// slash-separated path from root.
func files(t *testing.T, root string, each func(name, text string)) {
	t.Helper()
	for _, name := range strings.Split(strings.TrimSuffix(string(git(t, root, "ls-files", "-z")), "\x00"), "\x00") {
		b, err := os.ReadFile(filepath.Join(root, name))
		if errors.Is(err, fs.ErrNotExist) || bytes.IndexByte(b, 0) >= 0 {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		each(name, string(b))
	}
}

// Only what git tracks is checked: a contributor's .env, scratch app or
// editor files never reach the repository.
func TestFilesAreTracked(t *testing.T) {
	root := t.TempDir()
	git(t, root, "init", "-q")
	for name, body := range map[string]string{"app/tracked.txt": "tracked", "untracked.txt": "untracked", "app/.env": "untracked"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, root, "add", "app/tracked.txt")
	got := map[string]string{}
	files(t, root, func(name, text string) { got[name] = text })
	if len(got) != 1 || got["app/tracked.txt"] != "tracked" {
		t.Fatalf("read %v, want only app/tracked.txt", got)
	}
}

var (
	gooseUp   = regexp.MustCompile(`(?m)^-- \+goose Up[ \t\r]*$`)
	gooseDown = regexp.MustCompile(`(?m)^-- \+goose Down[ \t\r]*$`)
)

// Every goose migration here (jobs', the generators' templates and their
// goldens, bench/feed's) has a down section. Rolling back stops at one that
// hasn't, and testkit.Migrations checks no migration older than it.
func TestMigrationsHaveDown(t *testing.T) {
	n := 0
	files(t, checkout, func(name, text string) {
		if !strings.HasSuffix(name, ".sql") && !strings.HasSuffix(name, ".sql.tmpl") && !strings.HasSuffix(name, ".sql.golden") || !gooseUp.MatchString(text) {
			return
		}
		n++
		if !gooseDown.MatchString(text) {
			t.Errorf("%s: no -- +goose Down section", name)
		}
	})
	if n == 0 {
		t.Fatal("found no migrations")
	}
}

var email = regexp.MustCompile(`[A-Za-z0-9._%+-]+@([A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,})`)

// exampleDomain is a domain reserved for examples (RFC 2606, RFC 6761, and
// .internal), or one of the mail providers gantry's code treats specially.
func exampleDomain(domain string) bool {
	domain = strings.ToLower(domain)
	for _, d := range []string{"example.com", "example.net", "example.org"} {
		if domain == d || strings.HasSuffix(domain, "."+d) {
			return true
		}
	}
	tld := domain[strings.LastIndexByte(domain, '.')+1:]
	return slices.Contains([]string{"example", "test", "invalid", "localhost", "internal"}, tld) ||
		slices.Contains([]string{"gmail.com", "googlemail.com", "privaterelay.appleid.com"}, domain)
}

// Email addresses in code, tests, goldens and docs are made up, at a domain
// reserved for examples: a real one is someone's, or an app's (d681bf7).
// docs/plans quotes the conversations gantry was planned in.
func TestEmailAddressesAreExamples(t *testing.T) {
	files(t, checkout, func(name, text string) {
		if strings.HasPrefix(name, "docs/plans/") {
			return
		}
		for _, m := range email.FindAllStringSubmatch(text, -1) {
			if !exampleDomain(m[1]) {
				t.Errorf("%s: %s isn't at an example domain (example.com, *.test, *.localhost, ...)", name, m[0])
			}
		}
	})
}
