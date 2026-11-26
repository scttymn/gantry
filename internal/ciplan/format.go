package ciplan

import (
	"fmt"
	"io"
	"strings"
)

// WriteShell writes p as bash assignments, for bin/ci to eval: PLAN_<FLAG>
// is 1 or empty, and each list an array; a fuzz target or run is
// "package|pattern".
func WriteShell(w io.Writer, p Result) error {
	var b strings.Builder
	flag := func(name string, v bool) {
		fmt.Fprintf(&b, "PLAN_%s=%s\n", name, map[bool]string{true: "1"}[v])
	}
	array := func(name string, items []string) {
		quoted := make([]string, len(items))
		for i, item := range items {
			quoted[i] = "'" + strings.ReplaceAll(item, "'", `'\''`) + "'"
		}
		fmt.Fprintf(&b, "PLAN_%s=(%s)\n", name, strings.Join(quoted, " "))
	}
	flag("ANY", p.Any())
	flag("FULL", p.Full)
	flag("NEED_TIDY", p.NeedTidy)
	flag("NEED_POSTGRES", p.NeedPostgres)
	flag("NEED_CGO", p.NeedCGO)
	flag("NEED_RACE", p.NeedRace)
	flag("NEED_VET", p.NeedVet)
	flag("NEED_FMT", p.NeedFmt)
	flag("NEED_LINT", p.NeedLint)
	flag("NEED_LINT_CGO", p.NeedLintCGO)
	flag("NEED_VULN", p.NeedVuln)
	flag("NEED_RELEASE", p.NeedRelease)
	array("PACKAGES", p.Packages)
	array("RUNS", runPairs(p.Runs))
	array("LINT_PACKAGES", p.LintPackages)
	array("BENCH_MODULES", p.BenchModules)
	array("SHELL_SCRIPTS", p.ShellScripts)
	array("TLA_SPECS", p.TLASpecs)
	array("FUZZ_TARGETS", fuzzPairs(p.FuzzTargets))
	_, err := io.WriteString(w, b.String())
	return err
}

// WriteGitHub writes p as a GitHub Actions step's outputs: each flag true or
// false, each list space-separated, and the runs and fuzz targets as one
// command each.
func WriteGitHub(w io.Writer, p Result) error {
	var b strings.Builder
	flag := func(name string, v bool) { fmt.Fprintf(&b, "%s=%t\n", name, v) }
	list := func(name string, items []string) { fmt.Fprintf(&b, "%s=%s\n", name, strings.Join(items, " ")) }
	flag("any", p.Any())
	flag("full", p.Full)
	flag("need_tidy", p.NeedTidy)
	flag("need_postgres", p.NeedPostgres)
	flag("need_cgo", p.NeedCGO)
	flag("need_race", p.NeedRace)
	flag("need_vet", p.NeedVet)
	flag("need_fmt", p.NeedFmt)
	flag("need_lint", p.NeedLint)
	flag("need_lint_cgo", p.NeedLintCGO)
	flag("need_vuln", p.NeedVuln)
	flag("need_release", p.NeedRelease)
	list("packages", p.Packages)
	list("lint_packages", p.LintPackages)
	list("bench_modules", p.BenchModules)
	list("shell_scripts", p.ShellScripts)
	list("tla_specs", p.TLASpecs)
	var runs, fuzz []string
	for _, r := range p.Runs {
		runs = append(runs, fmt.Sprintf("go test -run '%s' %s", r.Pattern, r.Package))
	}
	for _, ft := range p.FuzzTargets {
		fuzz = append(fuzz, fmt.Sprintf("go test -fuzz='%s' -fuzztime=1s %s", ft.Name, ft.Package))
	}
	fmt.Fprintf(&b, "runs_cmd=%s\n", strings.Join(runs, " && "))
	fmt.Fprintf(&b, "fuzz_cmd=%s\n", strings.Join(fuzz, " && "))
	_, err := io.WriteString(w, b.String())
	return err
}

func runPairs(runs []Run) []string {
	pairs := make([]string, len(runs))
	for i, r := range runs {
		pairs[i] = r.Package + "|" + r.Pattern
	}
	return pairs
}

func fuzzPairs(targets []FuzzTarget) []string {
	pairs := make([]string, len(targets))
	for i, ft := range targets {
		pairs[i] = ft.Package + "|" + ft.Name
	}
	return pairs
}
