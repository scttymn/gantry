// Package hygiene holds checks on the repository's files themselves, the
// patterns an agent copies from whatever it finds here.
package hygiene

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path"
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

// shellchecked is what bin/shellcheck --list names in root, a checkout with
// the script.
func shellchecked(t *testing.T, root string) []string {
	t.Helper()
	cmd := exec.Command(filepath.Join(root, "bin/shellcheck"), "--list")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=safe.directory", "GIT_CONFIG_VALUE_0=*")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("bin/shellcheck --list: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
}

// bin/shellcheck finds the scripts itself: every file git tracks whose first
// line runs sh, bash, dash or ksh, or that's named as a shell script, a
// generator's template or golden of one too.
func TestShellcheckListsScripts(t *testing.T) {
	root := t.TempDir()
	git(t, root, "init", "-q")
	script, err := os.ReadFile(filepath.Join(checkout, "bin/shellcheck"))
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"bin/shellcheck":                     string(script),
		"bin/tool":                           "#!/usr/bin/env bash\necho tool\n",
		"hooks/pre-commit":                   "#!/bin/sh -e\necho hook\n",
		"scripts/env-s":                      "#!/usr/bin/env -S bash -eu\necho env\n",
		"scripts/ksh":                        "#!/bin/ksh\necho ksh\n",
		"setup.sh":                           "echo no shebang\n",
		"cmd/gantry/templates/x/run.sh.tmpl": "#!/bin/sh\necho {{.Name}}\n",
		"testdata/golden/x/bin/run.golden":   "#!/bin/sh\necho x\n",
		"tools/gen.py":                       "#!/usr/bin/env python3\nprint('py')\n",
		"tools/fish":                         "#!/usr/bin/fish\necho fish\n",
		"README.md":                          "# Not a script\n#!/bin/sh, on its second line\n",
		"untracked.sh":                       "#!/bin/sh\n",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	git(t, root, "add", ".", ":!untracked.sh")
	want := []string{"bin/shellcheck", "bin/tool", "cmd/gantry/templates/x/run.sh.tmpl", "hooks/pre-commit", "scripts/env-s", "scripts/ksh", "setup.sh", "testdata/golden/x/bin/run.golden"}
	if got := shellchecked(t, root); !slices.Equal(got, want) {
		t.Errorf("bin/shellcheck --list: %q, want %q", got, want)
	}
}

// CI's shellcheck step (bin/shellcheck) reads every script here, each one
// executable or starting with #!: one added anywhere is checked, with no list
// to add it to. Every script here is a shell script; one that isn't fails
// here until this test knows it.
func TestEveryScriptIsShellchecked(t *testing.T) {
	executable := map[string]bool{}
	for _, entry := range strings.Split(strings.TrimSuffix(string(git(t, checkout, "ls-files", "-s", "-z")), "\x00"), "\x00") {
		mode, name, _ := strings.Cut(entry, "\t")
		executable[name] = strings.HasPrefix(mode, "100755")
	}
	listed := shellchecked(t, checkout)
	n := 0
	files(t, checkout, func(name, text string) {
		if !executable[name] && !strings.HasPrefix(text, "#!") {
			return
		}
		n++
		if !slices.Contains(listed, name) {
			t.Errorf("%s is a script bin/shellcheck doesn't read: make its first line a sh or bash #!, or name it .sh", name)
		}
	})
	if n == 0 {
		t.Fatal("found no scripts")
	}
}

var (
	gooseUp   = regexp.MustCompile(`(?m)^-- \+goose Up[ \t\r]*$`)
	gooseDown = regexp.MustCompile(`(?m)^-- \+goose Down[ \t\r]*$`)
)

// migrationProblem is whether name is a goose migration, and what's wrong
// with it ("" when nothing is). A SQL file is one by its place (a migrations
// directory, a generator's migration.sql.tmpl), so a mistyped Up line doesn't
// hide it, or by its Up line wherever it is.
func migrationProblem(name, text string) (bool, string) {
	if !strings.HasSuffix(name, ".sql") && !strings.HasSuffix(name, ".sql.tmpl") && !strings.HasSuffix(name, ".sql.golden") {
		return false, ""
	}
	placed := slices.Contains(strings.Split(path.Dir(name), "/"), "migrations") || path.Base(name) == "migration.sql.tmpl"
	switch {
	case !placed && !gooseUp.MatchString(text):
		return false, ""
	case !gooseUp.MatchString(text):
		return true, "no -- +goose Up line"
	case !gooseDown.MatchString(text):
		return true, "no -- +goose Down section"
	}
	return true, ""
}

func TestMigrationProblem(t *testing.T) {
	const up, down = "-- +goose Up\nCREATE TABLE x (id integer);\n", "\n-- +goose Down\nDROP TABLE x;\n"
	for _, c := range []struct {
		name, text string
		migration  bool
		problem    string
	}{
		{"jobs/migrations/sqlite/00002_x.sql", up + down, true, ""},
		{"db/migrations/00002_x.sql", "-- +goose Up\nUPDATE x SET id = id;\n\n-- +goose Down\n", true, ""},
		{"jobs/migrations/sqlite/00002_x.sql", up, true, "no -- +goose Down section"},
		{"cmd/gantry/templates/emailcodes/codes.sql.tmpl", up, true, "no -- +goose Down section"},
		{"jobs/migrations/sqlite/00002_x.sql", "-- goose Up\nCREATE TABLE x (id integer);\n" + down, true, "no -- +goose Up line"},
		{"cmd/gantry/templates/storage/migration.sql.tmpl", "CREATE TABLE x (id integer);\n" + down, true, "no -- +goose Up line"},
		{"cmd/gantry/testdata/golden/new-sqlite/db/migrations/00001_start.sql.golden", down, true, "no -- +goose Up line"},
		{"cmd/gantry/templates/resource/model.sql.tmpl", "-- name: Posts :many\nSELECT * FROM posts;\n", false, ""},
		{"db/schema.sql", "CREATE TABLE x (id integer);\n", false, ""},
	} {
		if migration, problem := migrationProblem(c.name, c.text); migration != c.migration || problem != c.problem {
			t.Errorf("%s %q: (%v, %q), want (%v, %q)", c.name, c.text, migration, problem, c.migration, c.problem)
		}
	}
}

// Every goose migration here (jobs', the generators' templates and their
// goldens, bench/feed's) has its up and down sections. Rolling back stops at
// one without a down, and testkit.Migrations checks no migration older than it.
func TestMigrationsHaveDown(t *testing.T) {
	n := 0
	files(t, checkout, func(name, text string) {
		migration, problem := migrationProblem(name, text)
		if migration {
			n++
		}
		if problem != "" {
			t.Errorf("%s: %s", name, problem)
		}
	})
	if n == 0 {
		t.Fatal("found no migrations")
	}
}

var (
	testName = regexp.MustCompile(`\bTest[A-Z][A-Za-z0-9_]*`)
	testFunc = regexp.MustCompile(`\bfunc (Test[A-Z][A-Za-z0-9_]*)\(`)
)

// staleTestNames is each test named in texts (file → text) that no text
// defines, with the files that name it. A definition counts wherever it is:
// a _test.go file, a template or golden of one, or a string a test writes
// out as a file.
func staleTestNames(texts map[string]string) map[string][]string {
	defined := map[string]bool{}
	for _, text := range texts {
		for _, m := range testFunc.FindAllStringSubmatch(text, -1) {
			defined[m[1]] = true
		}
	}
	stale := map[string][]string{}
	for name, text := range texts {
		for _, test := range testName.FindAllString(text, -1) {
			if !defined[test] && !slices.Contains(stale[test], name) {
				stale[test] = append(stale[test], name)
			}
		}
	}
	return stale
}

func TestStaleTestNames(t *testing.T) {
	// Built here, so this file's own text names none of them.
	a, b, gone := "Test"+"A", "Test"+"B", "Test"+"Gone"
	got := staleTestNames(map[string]string{
		"a_test.go":       "package a\n\n// Like " + b + ".\nfunc " + a + "(t *testing.T) {}\n",
		"b.go.tmpl":       "func " + b + "(t *testing.T) {}\n",
		".github/ci.yml":  "# " + gone + " holds it; " + a + " does too.\n",
		"CONTRIBUTING.md": "`" + gone + "` and `TestingT`.\n",
	})
	if len(got) != 1 || len(got[gone]) != 2 {
		t.Errorf("stale: %v, want %s in .github/ci.yml and CONTRIBUTING.md", got, gone)
	}
}

// Every test the repository names (in a comment, a workflow, a doc, a
// message) is one it has: a test renamed or replaced leaves no pointer to
// nothing behind. docs/plans records what was, and keeps its old names.
func TestNamedTestsExist(t *testing.T) {
	texts := map[string]string{}
	files(t, checkout, func(name, text string) {
		if !strings.HasPrefix(name, "docs/plans/") {
			texts[name] = text
		}
	})
	for test, names := range staleTestNames(texts) {
		slices.Sort(names)
		t.Errorf("%s names %s, which no file defines: name the test that does its work now", strings.Join(names, ", "), test)
	}
}

// email is an address (its first group) and its domain (its second). It
// doesn't start just after a backslash: in "\t@templ.Raw(x)", a templ call in
// a Go string, the t is the escape's.
var email = regexp.MustCompile(`(?:^|[^\\A-Za-z0-9._%+-])([A-Za-z0-9._%+-]+@([A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]+))`)

// exampleDomain is a domain reserved for examples (RFC 2606, RFC 6761, and
// .internal).
func exampleDomain(domain string) bool {
	domain = strings.ToLower(domain)
	for _, d := range []string{"example.com", "example.net", "example.org"} {
		if domain == d || strings.HasSuffix(domain, "."+d) {
			return true
		}
	}
	tld := domain[strings.LastIndexByte(domain, '.')+1:]
	return slices.Contains([]string{"example", "test", "invalid", "localhost", "internal"}, tld)
}

// providerDomains are the mail providers social login treats specially
// (auth/social_provider.go), allowed only in the tests of it.
var providerDomains = map[string][]string{
	"auth/social_test.go": {"gmail.com", "privaterelay.appleid.com"},
}

// addressProblems is the email addresses in name that aren't examples.
func addressProblems(name, text string) []string {
	if strings.HasPrefix(name, "docs/plans/") {
		return nil
	}
	var bad []string
	for _, m := range email.FindAllStringSubmatch(text, -1) {
		if !exampleDomain(m[2]) && !slices.Contains(providerDomains[name], strings.ToLower(m[2])) {
			bad = append(bad, m[1])
		}
	}
	return bad
}

func TestAddressProblems(t *testing.T) {
	// Built here, so this file's own text holds none of them.
	at := func(local, domain string) string { return local + "@" + domain }
	gmail, relay, real, short := at("someone", "gmail.com"), at("relay", "privaterelay.appleid.com"), at("person", "realcompany.co"), at("a", "b.c")
	for _, c := range []struct {
		name, text string
		bad        []string
	}{
		{"mail/mail_test.go", `"x@example.org", "a@mail.example.com", "dev@shop.localhost", "pw@host.docker.internal"`, nil},
		{"mail/mail_test.go", gmail, []string{gmail}},
		{"README.md", relay, []string{relay}},
		{"auth/social_test.go", gmail + ", " + relay, nil},
		{"auth/social_test.go", real, []string{real}},
		{"testkit/links_test.go", `<a href="mailto:` + short + `">`, []string{short}},
		{"docs/plans/gantry.md", real, nil},
		{"devcheck/rules_test.go", `"templ show() {\n\t@templ.Raw(body)\n}"`, nil},
		{"README.md", gmail + "," + real, []string{gmail, real}},
	} {
		if got := addressProblems(c.name, c.text); !slices.Equal(got, c.bad) {
			t.Errorf("%s %q: %q, want %q", c.name, c.text, got, c.bad)
		}
	}
}

// Email addresses in code, tests, goldens and docs are made up, at a domain
// reserved for examples: a real one is someone's, or an app's (d681bf7).
// docs/plans quotes the conversations gantry was planned in.
func TestEmailAddressesAreExamples(t *testing.T) {
	files(t, checkout, func(name, text string) {
		for _, bad := range addressProblems(name, text) {
			t.Errorf("%s: %s isn't at an example domain (example.com, *.test, *.localhost, ...)", name, bad)
		}
	})
}
