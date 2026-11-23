package devcheck_test

import (
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func goCommand(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("go", args...).Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			t.Fatalf("go %s: %v\n%s%s", strings.Join(args, " "), err, out, exit.Stderr)
		}
		t.Fatal(err)
	}
	return string(out)
}

func TestProductionBuildLeavesOutDevcheck(t *testing.T) {
	if testing.Short() {
		t.Skip("-short")
	}
	const devcheck = "github.com/scttymn/gantry/devcheck"
	list := goCommand(t, "list", "-tags", "nodevcheck", "-f", `{{.ImportPath}} {{join .Deps " "}}`, "github.com/scttymn/gantry/...")
	var offenders []string
	for _, line := range strings.Split(strings.TrimSpace(list), "\n") {
		pkg, deps, _ := strings.Cut(line, " ")
		if pkg == devcheck || strings.HasPrefix(pkg, "github.com/scttymn/gantry/cmd/") {
			continue
		}
		if slices.Contains(strings.Fields(deps), devcheck) {
			offenders = append(offenders, pkg)
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("built with -tags nodevcheck, these still link devcheck: %s", strings.Join(offenders, ", "))
	}
}

func TestProductionBuildDevelopmentStub(t *testing.T) {
	if testing.Short() {
		t.Skip("-short")
	}
	out := goCommand(t, "test", "-count=1", "-v", "-tags", "nodevcheck", "-run", "^TestDevelopmentWithoutChecks$", "github.com/scttymn/gantry/web")
	if !strings.Contains(out, "--- PASS: TestDevelopmentWithoutChecks") {
		t.Fatalf("the nodevcheck build's development test didn't run:\n%s", out)
	}
}
