package gantry_test

import (
	"os"
	"regexp"
	"slices"
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
		"tool version":           `[\w.-]+(?:/[\w.-]+)+@v[\d.]+`,
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
