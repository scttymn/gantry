// Command ciplan prints the targeted CI plan for a list of changed files.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/scttymn/gantry/internal/ciplan"
	"github.com/scttymn/gantry/internal/gomod"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "ciplan: %v\n", err)
		os.Exit(1)
	}
}

type listPkg struct {
	ImportPath   string   `json:"ImportPath"`
	Dir          string   `json:"Dir"`
	Imports      []string `json:"Imports"`
	TestImports  []string `json:"TestImports"`
	XTestImports []string `json:"XTestImports"`
}

func run(args []string, in io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("ciplan", flag.ContinueOnError)
	root := fs.String("root", ".", "repository root")
	format := fs.String("format", "json", "output format: json, sh, or github")
	full := fs.Bool("full", false, "emit full CI plan")
	scriptsFile := fs.String("scripts", "", "optional newline-delimited tracked shell scripts file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	absRoot, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	module, err := gomod.ImportPath(absRoot)
	if err != nil {
		return err
	}

	allSpecs, err := listSpecs(absRoot)
	if err != nil {
		return err
	}
	allScripts, err := loadScripts(absRoot, *scriptsFile)
	if err != nil {
		return err
	}

	var plan ciplan.Result
	if *full {
		plan = ciplan.FullPlan(allSpecs, allScripts)
	} else {
		changed, err := readLines(in)
		if err != nil {
			return err
		}
		pkgs, err := listPackages(absRoot)
		if err != nil {
			return err
		}
		benchFeedImports := listBenchFeedImports(absRoot, module)
		plan = ciplan.Plan(module, changed, pkgs, allSpecs, allScripts, benchFeedImports)
	}

	switch *format {
	case "json":
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(plan)
	case "sh":
		return writeShell(out, plan)
	case "github":
		return writeGitHub(out, plan)
	default:
		return fmt.Errorf("unknown format %q", *format)
	}
}

func readLines(r io.Reader) ([]string, error) {
	var lines []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			lines = append(lines, line)
		}
	}
	return lines, sc.Err()
}

func listSpecs(root string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(root, "spec", "*.tla"))
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		rel, err := filepath.Rel(root, m)
		if err != nil {
			return nil, err
		}
		out = append(out, filepath.ToSlash(rel))
	}
	return out, nil
}

func loadScripts(root, file string) ([]string, error) {
	if file != "" {
		f, err := os.Open(file)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return readLines(f)
	}
	cmd := exec.Command(filepath.Join(root, "bin", "shellcheck"), "--list")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=safe.directory", "GIT_CONFIG_VALUE_0=*")
	raw, err := cmd.Output()
	if err != nil {
		return nil, nil
	}
	return readLines(bytes.NewReader(raw))
}

func listPackages(root string) ([]ciplan.Package, error) {
	cmd := exec.Command("go", "list", "-json", "./...")
	cmd.Dir = root
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list ./...: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	var pkgs []ciplan.Package
	for {
		var lp listPkg
		if err := dec.Decode(&lp); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		rel, err := filepath.Rel(root, lp.Dir)
		if err != nil {
			return nil, err
		}
		pkgs = append(pkgs, ciplan.Package{
			ImportPath:   lp.ImportPath,
			Dir:          filepath.ToSlash(rel),
			Imports:      lp.Imports,
			TestImports:  lp.TestImports,
			XTestImports: lp.XTestImports,
		})
	}
	return pkgs, nil
}

func listBenchFeedImports(root, module string) []string {
	dir := filepath.Join(root, "bench", "feed")
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		return nil
	}
	cmd := exec.Command("go", "list", "-f", `{{join .Deps "\n"}}`, "./...")
	cmd.Dir = dir
	raw, err := cmd.Output()
	if err != nil {
		return nil
	}
	prefix := module + "/"
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		imp := strings.TrimSpace(line)
		if (imp == module || strings.HasPrefix(imp, prefix)) && !seen[imp] {
			seen[imp] = true
			out = append(out, imp)
		}
	}
	return out
}

func writeShell(out io.Writer, p ciplan.Result) error {
	b := func(v bool) string {
		if v {
			return "1"
		}
		return ""
	}
	quote := func(s string) string {
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	array := func(name string, items []string) {
		fmt.Fprintf(out, "%s=(", name)
		for i, item := range items {
			if i > 0 {
				fmt.Fprint(out, " ")
			}
			fmt.Fprint(out, quote(item))
		}
		fmt.Fprintln(out, ")")
	}
	fuzz := make([]string, 0, len(p.FuzzTargets))
	for _, ft := range p.FuzzTargets {
		fuzz = append(fuzz, ft.Package+"|"+ft.Name)
	}
	fmt.Fprintf(out, "PLAN_ANY=%s\n", b(p.Any()))
	fmt.Fprintf(out, "PLAN_FULL=%s\n", b(p.Full))
	fmt.Fprintf(out, "PLAN_NEED_TIDY=%s\n", b(p.NeedTidy))
	fmt.Fprintf(out, "PLAN_NEED_POSTGRES=%s\n", b(p.NeedPostgres))
	fmt.Fprintf(out, "PLAN_NEED_CGO=%s\n", b(p.NeedCGO))
	fmt.Fprintf(out, "PLAN_NEED_RACE=%s\n", b(p.NeedRace))
	fmt.Fprintf(out, "PLAN_NEED_VET=%s\n", b(p.NeedVet))
	fmt.Fprintf(out, "PLAN_NEED_FMT=%s\n", b(p.NeedFmt))
	fmt.Fprintf(out, "PLAN_NEED_LINT=%s\n", b(p.NeedLint))
	fmt.Fprintf(out, "PLAN_NEED_LINT_CGO=%s\n", b(p.NeedLintCGO))
	fmt.Fprintf(out, "PLAN_NEED_VULN=%s\n", b(p.NeedVuln))
	fmt.Fprintf(out, "PLAN_NEED_RELEASE=%s\n", b(p.NeedRelease))
	fmt.Fprintf(out, "PLAN_DOC_GANTRY_TEST=%s\n", b(p.DocGantryTest))
	fmt.Fprintf(out, "PLAN_STRICT_CHECK=%s\n", b(p.StrictCheck))
	array("PLAN_PACKAGES", p.Packages)
	array("PLAN_LINT_PACKAGES", p.LintPackages)
	array("PLAN_BENCH_MODULES", p.BenchModules)
	array("PLAN_SHELL_SCRIPTS", p.ShellScripts)
	array("PLAN_TLA_SPECS", p.TLASpecs)
	array("PLAN_FUZZ_TARGETS", fuzz)
	return nil
}

func writeGitHub(out io.Writer, p ciplan.Result) error {
	b := func(v bool) string {
		if v {
			return "true"
		}
		return "false"
	}
	fuzzParts := make([]string, 0, len(p.FuzzTargets))
	for _, ft := range p.FuzzTargets {
		fuzzParts = append(fuzzParts, fmt.Sprintf("go test -fuzz=%s -fuzztime=1s %s", ft.Name, ft.Package))
	}
	fmt.Fprintf(out, "any=%s\n", b(p.Any()))
	fmt.Fprintf(out, "full=%s\n", b(p.Full))
	fmt.Fprintf(out, "need_toolchain=%s\n", b(p.NeedsToolchain()))
	fmt.Fprintf(out, "need_tidy=%s\n", b(p.NeedTidy))
	fmt.Fprintf(out, "need_postgres=%s\n", b(p.NeedPostgres))
	fmt.Fprintf(out, "need_cgo=%s\n", b(p.NeedCGO))
	fmt.Fprintf(out, "need_race=%s\n", b(p.NeedRace))
	fmt.Fprintf(out, "need_vet=%s\n", b(p.NeedVet))
	fmt.Fprintf(out, "need_fmt=%s\n", b(p.NeedFmt))
	fmt.Fprintf(out, "need_lint=%s\n", b(p.NeedLint))
	fmt.Fprintf(out, "need_lint_cgo=%s\n", b(p.NeedLintCGO))
	fmt.Fprintf(out, "need_vuln=%s\n", b(p.NeedVuln))
	fmt.Fprintf(out, "need_release=%s\n", b(p.NeedRelease))
	fmt.Fprintf(out, "doc_gantry_test=%s\n", b(p.DocGantryTest))
	fmt.Fprintf(out, "strict_check=%s\n", b(p.StrictCheck))
	fmt.Fprintf(out, "packages=%s\n", strings.Join(p.Packages, " "))
	fmt.Fprintf(out, "lint_packages=%s\n", strings.Join(p.LintPackages, " "))
	fmt.Fprintf(out, "bench_modules=%s\n", strings.Join(p.BenchModules, " "))
	fmt.Fprintf(out, "shell_scripts=%s\n", strings.Join(p.ShellScripts, " "))
	fmt.Fprintf(out, "tla_specs=%s\n", strings.Join(p.TLASpecs, " "))
	fmt.Fprintf(out, "fuzz_cmd=%s\n", strings.Join(fuzzParts, " && "))
	return nil
}
