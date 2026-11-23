// Package hygiene holds checks on the repository's files themselves, the
// patterns an agent copies from whatever it finds here.
package hygiene

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// files calls each with every file in the checkout but Go's caches and Git's,
// by its slash-separated path from the root.
func files(t *testing.T, each func(name, text string)) {
	t.Helper()
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".cache", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil || bytes.IndexByte(b, 0) >= 0 {
			return err
		}
		name, _ := filepath.Rel(root, p)
		each(filepath.ToSlash(name), string(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
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
	files(t, func(name, text string) {
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
	files(t, func(name, text string) {
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
