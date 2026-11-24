// Package ciplan computes the minimal set of Gantry CI checks, Go packages,
// TLA+ specifications, fuzz targets and shell scripts affected by a set of
// changed repository paths.
package ciplan

import (
	"path"
	"slices"
	"strings"
)

// Package describes one Go package from `go list -json ./...`.
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

// Result is the targeted CI execution plan for a set of changed files.
type Result struct {
	Full          bool         `json:"full"`
	NeedTidy      bool         `json:"need_tidy"`
	NeedPostgres  bool         `json:"need_postgres"`
	NeedCGO       bool         `json:"need_cgo"`
	NeedRace      bool         `json:"need_race"`
	NeedVet       bool         `json:"need_vet"`
	NeedFmt       bool         `json:"need_fmt"`
	NeedLint      bool         `json:"need_lint"`
	NeedLintCGO   bool         `json:"need_lint_cgo"`
	NeedVuln      bool         `json:"need_vuln"`
	NeedRelease   bool         `json:"need_release"`
	DocGantryTest bool         `json:"doc_gantry_test"`
	StrictCheck   bool         `json:"strict_check"`
	Packages      []string     `json:"packages"`
	LintPackages  []string     `json:"lint_packages"`
	BenchModules  []string     `json:"bench_modules"`
	ShellScripts  []string     `json:"shell_scripts"`
	TLASpecs      []string     `json:"tla_specs"`
	FuzzTargets   []FuzzTarget `json:"fuzz_targets"`
}

// Any reports whether the plan includes any work to run.
func (r Result) Any() bool {
	return r.Full || r.NeedTidy || r.NeedCGO || r.NeedRace || r.NeedVet ||
		r.NeedFmt || r.NeedLint || r.NeedLintCGO || r.NeedVuln ||
		r.NeedRelease || r.DocGantryTest || r.StrictCheck || len(r.Packages) > 0 ||
		len(r.BenchModules) > 0 || len(r.ShellScripts) > 0 ||
		len(r.TLASpecs) > 0 || len(r.FuzzTargets) > 0
}

// NeedsToolchain reports whether any planned step executes inside the Go toolchain container.
func (r Result) NeedsToolchain() bool {
	return r.Full || r.NeedTidy || r.NeedCGO || r.NeedRace || r.NeedVet ||
		r.NeedFmt || r.NeedLint || r.NeedLintCGO || r.NeedVuln ||
		r.DocGantryTest || r.StrictCheck || len(r.Packages) > 0 || len(r.BenchModules) > 0 ||
		len(r.FuzzTargets) > 0
}

// FullPlan returns a plan that runs every CI check across the repository.
func FullPlan(allSpecs, allScripts []string) Result {
	specs := slices.Clone(allSpecs)
	slices.Sort(specs)
	scripts := slices.Clone(allScripts)
	slices.Sort(scripts)
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
		ShellScripts: scripts,
		TLASpecs:     specs,
		FuzzTargets:  slices.Clone(AllFuzzTargets),
	}
}

// Plan computes the targeted checks needed for changed repository-relative file
// paths given the module's package graph, all TLA+ specs, all tracked shell
// scripts, and the gantry packages imported by bench/feed.
func Plan(module string, changed []string, pkgs []Package, allSpecs, allScripts, benchFeedImports []string) Result {
	files := normalizePaths(changed)
	if len(files) == 0 {
		return Result{}
	}

	specSet := toSet(allSpecs)
	scriptSet := toSet(allScripts)
	benchFeedSet := toSet(benchFeedImports)

	dirToImport := make(map[string]string, len(pkgs))
	importToDir := make(map[string]string, len(pkgs))
	revDeps := make(map[string][]string, len(pkgs))
	usesPostgres := make(map[string]bool, len(pkgs))
	testenvPkg := module + "/internal/testenv"

	for _, p := range pkgs {
		d := normalizeDir(p.Dir)
		dirToImport[d] = p.ImportPath
		importToDir[p.ImportPath] = d
		for _, imp := range allImports(p) {
			if imp == testenvPkg {
				usesPostgres[p.ImportPath] = true
			}
			revDeps[imp] = append(revDeps[imp], p.ImportPath)
		}
		if p.ImportPath == testenvPkg {
			usesPostgres[p.ImportPath] = true
		}
	}

	var (
		res            Result
		fullTests      bool
		fullLint       bool
		directCodePkgs = map[string]bool{}
		docOnlyPkgs    = map[string]bool{}
		benchMods      = map[string]bool{}
		specs          = map[string]bool{}
		scripts        = map[string]bool{}
	)

	addSpec := func(name string) {
		if specSet[name] {
			specs[name] = true
		}
	}
	addPkgByDir := func(target map[string]bool, dir string) {
		if imp, ok := dirToImport[normalizeDir(dir)]; ok {
			target[imp] = true
		}
	}

	for _, f := range files {
		if scriptSet[f] {
			scripts[f] = true
			addPkgByDir(docOnlyPkgs, "internal/hygiene")
		}
		switch path.Ext(f) {
		case ".go", ".templ", ".js", ".html", ".sql":
			res.StrictCheck = true
		}

		switch {
		case f == "Dockerfile":
			fullTests = true
			fullLint = true
			res.NeedTidy = true
			res.NeedCGO = true
			res.NeedRace = true
			res.NeedVet = true
			res.NeedFmt = true
			res.NeedLint = true
			res.NeedLintCGO = true
			res.NeedVuln = true
			res.NeedRelease = true
			benchMods["bench"] = true
			benchMods["bench/feed"] = true
			continue

		case f == "go.mod" || f == "go.sum":
			fullTests = true
			fullLint = true
			res.NeedTidy = true
			res.NeedCGO = true
			res.NeedRace = true
			res.NeedVet = true
			res.NeedFmt = true
			res.NeedLint = true
			res.NeedLintCGO = true
			res.NeedVuln = true
			res.NeedRelease = true
			benchMods["bench"] = true
			benchMods["bench/feed"] = true
			continue

		case f == "bench/go.mod" || f == "bench/go.sum":
			res.NeedTidy = true
			benchMods["bench"] = true
			continue

		case f == "bench/feed/go.mod" || f == "bench/feed/go.sum":
			res.NeedTidy = true
			benchMods["bench/feed"] = true
			addPkgByDir(directCodePkgs, "cmd/gantry")
			continue

		case f == "bench/sqlite3way/go.mod" || f == "bench/sqlite3way/go.sum":
			res.NeedTidy = true
			continue

		case f == ".golangci.yml" || f == "staticcheck.conf":
			fullLint = true
			res.NeedLint = true
			res.NeedLintCGO = true
			benchMods["bench"] = true
			benchMods["bench/feed"] = true
			continue

		case f == ".gantry-check.json":
			addPkgByDir(directCodePkgs, "devcheck")
			res.NeedRace = true
			res.NeedVet = true
			res.NeedLint = true
			continue

		case f == "bin/tlc":
			for _, s := range allSpecs {
				specs[s] = true
			}
			addPkgByDir(docOnlyPkgs, ".")
			addPkgByDir(docOnlyPkgs, "internal/pins")
			addPkgByDir(docOnlyPkgs, "internal/hygiene")
			continue

		case f == "bin/ci" || f == ".github/workflows/ci.yml" || f == ".github/workflows/release.yml" || f == ".githooks/pre-push" || f == "bin/go" || f == "bin/shellcheck":
			addPkgByDir(docOnlyPkgs, ".")
			addPkgByDir(docOnlyPkgs, "internal/pins")
			addPkgByDir(docOnlyPkgs, "internal/hygiene")
			continue

		case f == "bin/bench" || f == ".github/workflows/bench.yml":
			addPkgByDir(docOnlyPkgs, ".")
			addPkgByDir(docOnlyPkgs, "internal/pins")
			addPkgByDir(docOnlyPkgs, "internal/hygiene")
			benchMods["bench"] = true
			continue

		case strings.HasPrefix(f, "spec/") && (strings.HasSuffix(f, ".tla") || strings.HasSuffix(f, ".cfg")):
			base := strings.TrimSuffix(strings.TrimSuffix(path.Base(f), ".tla"), ".cfg")
			addSpec("spec/" + base + ".tla")
			continue

		case f == "README.md" || f == "CONTRIBUTING.md" || f == "AGENTS.md" || f == "llms.txt" || strings.HasPrefix(f, "docs/"):
			addPkgByDir(docOnlyPkgs, ".")
			addPkgByDir(docOnlyPkgs, "internal/hygiene")
			if f == "CONTRIBUTING.md" {
				addPkgByDir(docOnlyPkgs, "internal/pins")
			}
			if f == "docs/development.md" {
				addPkgByDir(docOnlyPkgs, "devcheck")
			}
			if f == "README.md" || f == "llms.txt" || (strings.HasPrefix(f, "docs/") && !strings.HasPrefix(f, "docs/plans/")) {
				res.DocGantryTest = true
			}
			continue

		case strings.HasPrefix(f, "bench/feed/"):
			benchMods["bench/feed"] = true
			addPkgByDir(directCodePkgs, "cmd/gantry")
			if strings.HasSuffix(f, ".go") {
				res.NeedFmt = true
				res.NeedLint = true
			}
			continue

		case strings.HasPrefix(f, "bench/sqlite3way/"):
			if strings.HasSuffix(f, ".go") {
				res.NeedFmt = true
			}
			continue

		case strings.HasPrefix(f, "bench/"):
			benchMods["bench"] = true
			if strings.HasSuffix(f, ".go") {
				res.NeedFmt = true
				res.NeedLint = true
			}
			continue
		}

		if imp, dir, ok := matchPackage(f, dirToImport); ok {
			directCodePkgs[imp] = true
			res.NeedVet = true
			res.NeedLint = true
			res.NeedRace = true
			if strings.HasSuffix(f, ".go") {
				res.NeedFmt = true
			}
			switch dir {
			case "db", "internal/pins":
				res.NeedCGO = true
				res.NeedLintCGO = true
			case "jobs":
				addSpec("spec/Jobs.tla")
			case "web":
				addSpec("spec/PageCache.tla")
			case "auth":
				addSpec("spec/Auth.tla")
			}
			if dir == "cmd/gantry" || dir == "internal/pins" {
				res.NeedRelease = true
			}
			if dir == "internal/ciplan" || strings.HasPrefix(dir, "internal/ciplan/") {
				addPkgByDir(docOnlyPkgs, ".")
			}
		}
	}

	codeClosure := closeReverse(directCodePkgs, revDeps)
	for imp := range codeClosure {
		if benchFeedSet[imp] {
			benchMods["bench/feed"] = true
		}
	}

	if fullTests {
		res.Packages = []string{"./..."}
		res.NeedPostgres = true
		res.DocGantryTest = false
		res.StrictCheck = false
	} else {
		combined := map[string]bool{}
		for imp := range codeClosure {
			combined[imp] = true
		}
		for imp := range docOnlyPkgs {
			combined[imp] = true
		}
		for imp := range combined {
			if usesPostgres[imp] {
				res.NeedPostgres = true
			}
			res.Packages = append(res.Packages, relPkg(importToDir[imp]))
		}
		slices.Sort(res.Packages)
		if slices.Contains(res.Packages, "./cmd/gantry") {
			res.DocGantryTest = false
		}
		if slices.Contains(res.Packages, "./devcheck") {
			res.StrictCheck = false
		}
	}

	if fullLint || fullTests {
		res.LintPackages = []string{"./..."}
	} else if res.NeedLint {
		for imp := range codeClosure {
			res.LintPackages = append(res.LintPackages, relPkg(importToDir[imp]))
		}
		slices.Sort(res.LintPackages)
	}

	if fullTests {
		res.FuzzTargets = slices.Clone(AllFuzzTargets)
	} else {
		for _, ft := range AllFuzzTargets {
			dir := normalizeDir(strings.TrimPrefix(ft.Package, "./"))
			if imp, ok := dirToImport[dir]; ok && codeClosure[imp] {
				res.FuzzTargets = append(res.FuzzTargets, ft)
			}
		}
	}

	for mod := range benchMods {
		res.BenchModules = append(res.BenchModules, mod)
	}
	slices.Sort(res.BenchModules)

	for s := range specs {
		res.TLASpecs = append(res.TLASpecs, s)
	}
	slices.Sort(res.TLASpecs)

	for sc := range scripts {
		res.ShellScripts = append(res.ShellScripts, sc)
	}
	slices.Sort(res.ShellScripts)

	return res
}

func normalizePaths(in []string) []string {
	seen := make(map[string]bool, len(in))
	var out []string
	for _, raw := range in {
		clean := path.Clean(strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/")))
		clean = strings.TrimPrefix(clean, "./")
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
	dir = path.Clean(strings.ReplaceAll(strings.TrimSpace(dir), "\\", "/"))
	dir = strings.TrimPrefix(dir, "./")
	if dir == "." {
		return ""
	}
	return dir
}

func relPkg(dir string) string {
	dir = normalizeDir(dir)
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

func allImports(p Package) []string {
	out := make([]string, 0, len(p.Imports)+len(p.UnitImports)+len(p.XUnitImports))
	out = append(out, p.Imports...)
	out = append(out, p.UnitImports...)
	out = append(out, p.XUnitImports...)
	return out
}

func matchPackage(file string, dirToImport map[string]string) (imp, dir string, ok bool) {
	cur := path.Dir(file)
	if cur == "." {
		cur = ""
	}
	for {
		if p, found := dirToImport[cur]; found {
			return p, cur, true
		}
		if cur == "" {
			return "", "", false
		}
		next := path.Dir(cur)
		if next == "." || next == cur {
			cur = ""
		} else {
			cur = next
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
