// Package ciplan works out which of Gantry's CI checks a change needs: the Go
// packages whose tests it can break (a changed package, every package that
// imports it, and the generator when a generated app imports it), and the
// specs, fuzz targets, shell scripts, linters, modules and builds that read
// what changed.
//
// Every change also runs the checks of the whole repo, under a second
// together: the root package's, internal/hygiene's, internal/pins', this
// planner's, and TestGantryPassesStrict. A change to CI itself (ci.yml, bin/ci,
// bin/go, bin/shellcheck, this planner), go.mod, go.sum or the Dockerfile runs
// every check. Each spec/*.tla models a package's code, and a change to that
// code runs it (TestEverySpecHasAnOwner); a spec/ config runs the spec named
// by its name up to its first dot, as bin/tlc pairs them, and one with no
// spec runs every spec, so bin/tlc fails on it.
package ciplan

import (
	"cmp"
	"path"
	"slices"
	"strings"
)

// Package is one Go package of the module: `go list -json`'s fields, its
// imports from every build CI tests.
type Package struct {
	ImportPath   string
	Dir          string
	Imports      []string
	UnitImports  []string
	XUnitImports []string
}

// FuzzTarget identifies one Go fuzz test to smoke-run.
type FuzzTarget struct {
	Package string `json:"package"`
	Name    string `json:"name"`
}

// AllFuzzTargets lists every fuzz target smoke-tested by CI.
var AllFuzzTargets = []FuzzTarget{
	{Package: "./sign", Name: "^FuzzSignVerify$"},
	{Package: "./web", Name: "^FuzzSent$"},
	{Package: "./web", Name: "^FuzzProxies$"},
	{Package: "./web", Name: "^FuzzPageCache$"},
	{Package: "./images", Name: "^FuzzDimensionsAndResize$"},
	{Package: "./devcheck", Name: "^FuzzJSAndHTMLRules$"},
	{Package: "./internal/featuremap", Name: "^FuzzCovers$"},
}

// Run is some tests of a package the plan doesn't test whole: go test -run
// Pattern Package.
type Run struct {
	Package string `json:"package"`
	Pattern string `json:"pattern"`
}

var (
	strictRun    = Run{"./devcheck", "^TestGantryPassesStrict$"}
	docsRun      = Run{"./cmd/gantry", "^(TestDocsCommands|TestDocsCodeSpansCloseOnTheirLine)$"}
	benchFeedRun = Run{"./cmd/gantry", "^TestBenchFeedIsANewApp$"}
	releaseRun   = Run{"./cmd/gantry", "^TestReleasePublishesBinstubAssets$"}
)

var dirsTestedOnEveryChange = []string{"", "internal/hygiene", "internal/pins", "internal/ciplan", "internal/ciplan/cmd/ciplan"}

var specsModellingDir = map[string][]string{
	"auth": {"spec/Auth.tla"},
	"jobs": {"spec/Jobs.tla", "spec/Ticks.tla"},
	"web":  {"spec/PageCache.tla"},
}

func plansEveryCheck(file string) bool {
	switch file {
	case ".github/workflows/ci.yml", "bin/ci", "bin/go", "bin/shellcheck", "Dockerfile", "go.mod", "go.sum":
		return true
	}
	return strings.HasPrefix(file, "internal/ciplan/")
}

// Result is the targeted CI execution plan for a set of changed files.
type Result struct {
	Full         bool         `json:"full"`
	NeedTidy     bool         `json:"need_tidy"`
	NeedPostgres bool         `json:"need_postgres"`
	NeedCGO      bool         `json:"need_cgo"`
	NeedRace     bool         `json:"need_race"`
	NeedVet      bool         `json:"need_vet"`
	NeedFmt      bool         `json:"need_fmt"`
	NeedLint     bool         `json:"need_lint"`
	NeedLintCGO  bool         `json:"need_lint_cgo"`
	NeedVuln     bool         `json:"need_vuln"`
	NeedRelease  bool         `json:"need_release"`
	Packages     []string     `json:"packages"`
	Runs         []Run        `json:"runs"`
	LintPackages []string     `json:"lint_packages"`
	BenchModules []string     `json:"bench_modules"`
	ShellScripts []string     `json:"shell_scripts"`
	TLASpecs     []string     `json:"tla_specs"`
	FuzzTargets  []FuzzTarget `json:"fuzz_targets"`
}

// Any reports whether the plan includes any work to run.
func (r Result) Any() bool {
	return r.Full || r.NeedTidy || r.NeedCGO || r.NeedRace || r.NeedVet ||
		r.NeedFmt || r.NeedLint || r.NeedLintCGO || r.NeedVuln || r.NeedRelease ||
		len(r.Packages) > 0 || len(r.Runs) > 0 || len(r.BenchModules) > 0 ||
		len(r.ShellScripts) > 0 || len(r.TLASpecs) > 0 || len(r.FuzzTargets) > 0
}

// FullPlan returns a plan that runs every CI check across the repository.
func FullPlan(allSpecs, allScripts []string) Result {
	return Result{
		Full:         true,
		NeedTidy:     true,
		NeedPostgres: true,
		NeedCGO:      true,
		NeedRace:     true,
		NeedVet:      true,
		NeedFmt:      true,
		NeedLint:     true,
		NeedLintCGO:  true,
		NeedVuln:     true,
		NeedRelease:  true,
		Packages:     []string{"./..."},
		LintPackages: []string{"./..."},
		BenchModules: []string{"bench", "bench/feed"},
		ShellScripts: sorted(allScripts),
		TLASpecs:     sorted(allSpecs),
		FuzzTargets:  slices.Clone(AllFuzzTargets),
	}
}

// Plan computes the checks a change to the repository-relative paths changed
// needs.
func Plan(changed []string, in Inputs) Result {
	files := normalizePaths(changed)
	if len(files) == 0 {
		return Result{}
	}
	if slices.ContainsFunc(files, plansEveryCheck) {
		return FullPlan(in.Specs, in.Scripts)
	}

	g := newGraph(in)
	specSet, scriptSet, benchFeedSet := toSet(in.Specs), toSet(in.Scripts), toSet(in.BenchFeedImports)
	var (
		res         Result
		lintAll     bool
		changedPkgs = map[string]bool{}
		tested      = map[string]bool{}
		runs        = map[Run]bool{strictRun: true}
		bench       = map[string]bool{}
		specs       = map[string]bool{}
		scripts     = map[string]bool{}
	)
	allSpecs := func() {
		for _, s := range in.Specs {
			specs[s] = true
		}
	}
	for _, dir := range dirsTestedOnEveryChange {
		g.add(tested, dir)
	}

	for _, f := range files {
		if scriptSet[f] {
			scripts[f] = true
		}
		goFile := strings.HasSuffix(f, ".go")
		switch {
		case f == ".golangci.yml" || f == "staticcheck.conf":
			lintAll = true
			res.NeedLint = true
			res.NeedLintCGO = true
			bench["bench"] = true
			bench["bench/feed"] = true

		case f == "bin/tlc":
			allSpecs()

		case f == "bin/bench" || f == ".github/workflows/bench.yml":
			bench["bench"] = true

		case f == ".github/workflows/release.yml":
			runs[releaseRun] = true
			res.NeedRelease = true

		case f == ".dockerignore":
			res.NeedRelease = true

		case strings.HasPrefix(f, "spec/") && (strings.HasSuffix(f, ".tla") || strings.HasSuffix(f, ".cfg")):
			name := path.Base(f)
			if spec := "spec/" + name[:strings.IndexByte(name, '.')] + ".tla"; specSet[spec] {
				specs[spec] = true
			} else {
				allSpecs()
			}

		case f == "README.md" || f == "CONTRIBUTING.md" || f == "AGENTS.md" || f == "llms.txt" || strings.HasPrefix(f, "docs/"):
			if f == "docs/development.md" {
				g.add(tested, "devcheck")
			}
			if f != "CONTRIBUTING.md" && f != "AGENTS.md" && !strings.HasPrefix(f, "docs/plans/") {
				runs[docsRun] = true
			}

		case f == "bench/go.mod" || f == "bench/go.sum":
			res.NeedTidy = true
			bench["bench"] = true

		case f == "bench/feed/go.mod" || f == "bench/feed/go.sum":
			res.NeedTidy = true
			bench["bench/feed"] = true
			runs[benchFeedRun] = true

		case f == "bench/sqlite3way/go.mod" || f == "bench/sqlite3way/go.sum":
			res.NeedTidy = true

		case strings.HasPrefix(f, "bench/feed/"):
			bench["bench/feed"] = true
			runs[benchFeedRun] = true
			if goFile {
				res.NeedFmt, res.NeedLint, res.NeedTidy = true, true, true
			}

		case strings.HasPrefix(f, "bench/sqlite3way/"):
			if goFile {
				res.NeedFmt, res.NeedTidy = true, true
			}

		case strings.HasPrefix(f, "bench/"):
			bench["bench"] = true
			if goFile {
				res.NeedFmt, res.NeedLint, res.NeedTidy = true, true, true
			}

		default:
			imp, dir, ok := matchPackage(f, g.dirToImport)
			if inNoPackage := !ok || dir == "" && !goFile; inNoPackage {
				continue
			}
			changedPkgs[imp] = true
			res.NeedVet, res.NeedLint, res.NeedRace = true, true, true
			if goFile {
				res.NeedFmt, res.NeedTidy, res.NeedVuln = true, true, true
			}
			if dir == "db" || dir == "internal/pins" {
				res.NeedCGO, res.NeedLintCGO = true, true
			}
			for _, s := range specsModellingDir[dir] {
				specs[s] = true
			}
		}
	}

	closure := closeReverse(changedPkgs, g.revDeps)
	for imp := range closure {
		if benchFeedSet[imp] {
			bench["bench/feed"] = true
		}
		tested[imp] = true
	}
	if closure[in.Module+"/cmd/gantry"] {
		res.NeedRelease = true
	}
	for imp := range tested {
		if g.usesPostgres[imp] {
			res.NeedPostgres = true
		}
		res.Packages = append(res.Packages, relPkg(g.importToDir[imp]))
	}
	slices.Sort(res.Packages)
	for r := range runs {
		if !slices.Contains(res.Packages, r.Package) {
			res.Runs = append(res.Runs, r)
		}
	}
	slices.SortFunc(res.Runs, func(a, b Run) int {
		return cmp.Or(cmp.Compare(a.Package, b.Package), cmp.Compare(a.Pattern, b.Pattern))
	})

	if lintAll {
		res.LintPackages = []string{"./..."}
	} else if res.NeedLint {
		for imp := range closure {
			res.LintPackages = append(res.LintPackages, relPkg(g.importToDir[imp]))
		}
		slices.Sort(res.LintPackages)
	}
	for _, ft := range AllFuzzTargets {
		if imp, ok := g.dirToImport[normalizeDir(ft.Package)]; ok && closure[imp] {
			res.FuzzTargets = append(res.FuzzTargets, ft)
		}
	}
	res.BenchModules = keys(bench)
	res.TLASpecs = keys(specs)
	res.ShellScripts = keys(scripts)
	return res
}

type graph struct {
	dirToImport  map[string]string
	importToDir  map[string]string
	revDeps      map[string][]string
	usesPostgres map[string]bool
}

func newGraph(in Inputs) graph {
	g := graph{
		dirToImport:  map[string]string{},
		importToDir:  map[string]string{},
		revDeps:      map[string][]string{},
		usesPostgres: map[string]bool{},
	}
	testenv := in.Module + "/internal/testenv"
	for _, p := range in.Packages {
		d := normalizeDir(p.Dir)
		g.dirToImport[d] = p.ImportPath
		g.importToDir[p.ImportPath] = d
		for _, imp := range slices.Concat(p.Imports, p.UnitImports, p.XUnitImports) {
			if imp == testenv {
				g.usesPostgres[p.ImportPath] = true
			}
			g.revDeps[imp] = append(g.revDeps[imp], p.ImportPath)
		}
		if p.ImportPath == testenv {
			g.usesPostgres[p.ImportPath] = true
		}
	}
	for _, imp := range in.GeneratorImports {
		g.revDeps[imp] = append(g.revDeps[imp], in.Module+"/cmd/gantry")
	}
	return g
}

func (g graph) add(set map[string]bool, dir string) {
	if imp, ok := g.dirToImport[dir]; ok {
		set[imp] = true
	}
}

func normalizePaths(in []string) []string {
	seen := make(map[string]bool, len(in))
	var out []string
	for _, raw := range in {
		clean := strings.TrimPrefix(path.Clean(strings.TrimSpace(raw)), "./")
		if clean == "" || clean == "." || seen[clean] {
			continue
		}
		seen[clean] = true
		out = append(out, clean)
	}
	slices.Sort(out)
	return out
}

func normalizeDir(dir string) string {
	dir = strings.TrimPrefix(path.Clean(dir), "./")
	if dir == "." {
		return ""
	}
	return dir
}

func relPkg(dir string) string {
	if dir == "" {
		return "."
	}
	return "./" + dir
}

func toSet(items []string) map[string]bool {
	s := make(map[string]bool, len(items))
	for _, item := range items {
		s[item] = true
	}
	return s
}

func keys(set map[string]bool) []string {
	var out []string
	for k := range set {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func sorted(items []string) []string {
	out := slices.Clone(items)
	slices.Sort(out)
	return out
}

func matchPackage(file string, dirToImport map[string]string) (imp, dir string, ok bool) {
	for dir = normalizeDir(path.Dir(file)); ; dir = normalizeDir(path.Dir(dir)) {
		if imp, ok := dirToImport[dir]; ok {
			return imp, dir, true
		}
		if dir == "" {
			return "", "", false
		}
	}
}

func closeReverse(seeds map[string]bool, revDeps map[string][]string) map[string]bool {
	visited := make(map[string]bool, len(seeds))
	queue := make([]string, 0, len(seeds))
	for imp := range seeds {
		visited[imp] = true
		queue = append(queue, imp)
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, dep := range revDeps[cur] {
			if !visited[dep] {
				visited[dep] = true
				queue = append(queue, dep)
			}
		}
	}
	return visited
}
