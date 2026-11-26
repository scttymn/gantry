package ciplan

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const testMod = "github.com/scttymn/gantry"

func sampleInputs() Inputs {
	pkg := func(dir string, imports ...string) Package {
		p := Package{ImportPath: testMod + "/" + dir, Dir: dir}
		if dir == "" {
			p.ImportPath = testMod
		}
		for _, imp := range imports {
			if after, ok := strings.CutPrefix(imp, "test:"); ok {
				p.UnitImports = append(p.UnitImports, testMod+"/"+after)
			} else {
				p.Imports = append(p.Imports, testMod+"/"+imp)
			}
		}
		return p
	}
	return Inputs{
		Module: testMod,
		Packages: []Package{
			pkg(""),
			pkg("internal/env"),
			pkg("internal/hygiene"),
			pkg("internal/pins"),
			pkg("internal/ciplan", "internal/pins"),
			pkg("internal/ciplan/cmd/ciplan", "internal/ciplan"),
			pkg("internal/testenv"),
			pkg("internal/featuremap"),
			pkg("db", "internal/pins", "test:internal/testenv"),
			pkg("token"),
			pkg("sign", "db", "internal/env"),
			pkg("devcheck"),
			pkg("web", "sign", "devcheck"),
			pkg("images", "web"),
			pkg("jobs", "db", "test:internal/testenv"),
			pkg("auth", "db", "sign", "token", "web", "test:internal/testenv"),
			pkg("testkit", "db", "web", "internal/featuremap", "test:internal/testenv"),
			pkg("cmd/gantry", "devcheck", "internal/pins", "testkit", "test:internal/testenv"),
		},
		Specs:            []string{"spec/Auth.tla", "spec/Jobs.tla", "spec/PageCache.tla", "spec/Ticks.tla"},
		Scripts:          []string{"bin/ci", "bin/go", "bin/shellcheck", "bin/tlc", ".githooks/pre-push"},
		BenchFeedImports: []string{testMod + "/db", testMod + "/web", testMod + "/sign"},
		GeneratorImports: []string{testMod + "/auth", testMod + "/jobs", testMod + "/web"},
	}
}

var always = []string{".", "./internal/ciplan", "./internal/ciplan/cmd/ciplan", "./internal/hygiene", "./internal/pins"}

func withAlways(pkgs ...string) []string {
	out := slices.Concat(always, pkgs)
	slices.Sort(out)
	return slices.Compact(out)
}

func TestPlanEveryChangeGetsTheRepoWideChecks(t *testing.T) {
	for _, f := range []string{"LICENSE", ".github/dependabot.yml", "auth/auth_test.go", "jobs/migrations/sqlite/00001_jobs.sql", ".gantry-check.json", "spec/Auth.tla", "cmd/gantry/templates/new/Dockerfile.tmpl"} {
		got := Plan([]string{f}, sampleInputs())
		for _, pkg := range always {
			if !slices.Contains(got.Packages, pkg) {
				t.Errorf("%s: Packages %v, without %s", f, got.Packages, pkg)
			}
		}
		if !slices.Contains(got.Runs, strictRun) && !slices.Contains(got.Packages, "./devcheck") {
			t.Errorf("%s: neither TestGantryPassesStrict nor ./devcheck runs: %+v", f, got)
		}
	}
}

func TestPlanNonCodeOutsidePackages(t *testing.T) {
	for _, f := range []string{"LICENSE", ".github/dependabot.yml", ".gantry-check.json"} {
		got := Plan([]string{f}, sampleInputs())
		if !slices.Equal(got.Packages, always) || got.NeedRace || got.NeedVet || got.NeedLint || got.NeedRelease || got.NeedPostgres {
			t.Errorf("%s plans more than the repo-wide checks: %+v", f, got)
		}
	}
}

func TestPlanDocs(t *testing.T) {
	got := Plan([]string{"docs/development.md"}, sampleInputs())
	if !slices.Equal(got.Packages, withAlways("./devcheck")) {
		t.Errorf("Packages = %v", got.Packages)
	}
	if !slices.Equal(got.Runs, []Run{docsRun}) {
		t.Errorf("Runs = %v, want the docs' commands", got.Runs)
	}
	if got.NeedCGO || got.NeedPostgres || got.NeedRace || got.NeedLint || got.NeedVet || got.NeedFmt || got.NeedTidy || got.NeedVuln || got.NeedRelease {
		t.Errorf("heavy checks for a doc: %+v", got)
	}
	if len(got.TLASpecs) != 0 || len(got.FuzzTargets) != 0 || len(got.ShellScripts) != 0 || len(got.BenchModules) != 0 {
		t.Errorf("extra targets for a doc: %+v", got)
	}
}

func TestPlanSpecs(t *testing.T) {
	for _, c := range []struct {
		file string
		want []string
	}{
		{"spec/Auth.tla", []string{"spec/Auth.tla"}},
		{"spec/Jobs.cfg", []string{"spec/Jobs.tla"}},
		{"spec/Jobs.Retry.cfg", []string{"spec/Jobs.tla"}},
		{"spec/Jobs.MutStaleFence.Retry.cfg", []string{"spec/Jobs.tla"}},
		{"spec/Ticks.Prompt.cfg", []string{"spec/Ticks.tla"}},
		{"spec/Orphan.X.cfg", sampleInputs().Specs},
		{"spec/Nope.tla", sampleInputs().Specs},
		{"jobs/recurring.go", []string{"spec/Jobs.tla", "spec/Ticks.tla"}},
		{"auth/auth.go", []string{"spec/Auth.tla"}},
		{"web/cache.go", []string{"spec/PageCache.tla"}},
		{"bin/tlc", sampleInputs().Specs},
	} {
		got := Plan([]string{c.file}, sampleInputs())
		if !slices.Equal(got.TLASpecs, c.want) || !got.Any() {
			t.Errorf("%s: TLASpecs %v, want %v", c.file, got.TLASpecs, c.want)
		}
	}
}

func TestPlanLeafPackageClosure(t *testing.T) {
	got := Plan([]string{"token/token.go"}, sampleInputs())
	if want := withAlways("./auth", "./cmd/gantry", "./token"); !slices.Equal(got.Packages, want) {
		t.Errorf("Packages = %v, want %v", got.Packages, want)
	}
	if !got.NeedPostgres || !got.NeedRace || !got.NeedVet || !got.NeedFmt || !got.NeedLint {
		t.Errorf("want test/race/vet/fmt/lint on the packages: %+v", got)
	}
	if !got.NeedTidy || !got.NeedVuln {
		t.Errorf("a .go change can leave go.mod untidy or reach a vulnerability: %+v", got)
	}
	if !got.NeedRelease {
		t.Errorf("a generated app imports auth, which imports token, so the release build runs: %+v", got)
	}
	if got.NeedCGO || got.NeedLintCGO {
		t.Errorf("token edit needs no cgo pass: %+v", got)
	}
}

func TestPlanGeneratedAppsImports(t *testing.T) {
	got := Plan([]string{"jobs/jobs.go"}, sampleInputs())
	if !slices.Contains(got.Packages, "./cmd/gantry") || !got.NeedRelease {
		t.Errorf("a generated app imports jobs: %+v", got)
	}
}

func TestPlanWebTargetsFuzzAndBenchFeed(t *testing.T) {
	got := Plan([]string{"web/cache.go"}, sampleInputs())
	if !slices.Equal(got.BenchModules, []string{"bench/feed"}) {
		t.Errorf("BenchModules = %v, want [bench/feed]", got.BenchModules)
	}
	var fuzzNames []string
	for _, ft := range got.FuzzTargets {
		fuzzNames = append(fuzzNames, ft.Name)
	}
	wantFuzz := []string{"^FuzzSent$", "^FuzzProxies$", "^FuzzPageCache$", "^FuzzDimensionsAndResize$"}
	if !slices.Equal(fuzzNames, wantFuzz) {
		t.Errorf("FuzzTargets = %v, want %v", fuzzNames, wantFuzz)
	}
}

func TestPlanCGO(t *testing.T) {
	for _, f := range []string{"db/sqlite_c.go", "internal/pins/pins.go"} {
		got := Plan([]string{f}, sampleInputs())
		if !got.NeedCGO || !got.NeedLintCGO || !got.NeedPostgres || !got.NeedRace || !slices.Contains(got.Packages, "./db") {
			t.Errorf("%s must test ./db with cgo, on Postgres, with the race detector: %+v", f, got)
		}
	}
}

func TestPlanReleaseBuild(t *testing.T) {
	for _, f := range []string{"devcheck/rules.go", "auth/auth.go", "internal/pins/pins.go", "cmd/gantry/new.go", ".dockerignore", ".github/workflows/release.yml"} {
		if got := Plan([]string{f}, sampleInputs()); !got.NeedRelease {
			t.Errorf("%s: no release build: %+v", f, got)
		}
	}
	if got := Plan([]string{".github/workflows/release.yml"}, sampleInputs()); !slices.Contains(got.Runs, releaseRun) {
		t.Errorf("release.yml: TestReleasePublishesBinstubAssets doesn't run: %+v", got)
	}
}

func TestPlanFull(t *testing.T) {
	for _, f := range []string{".github/workflows/ci.yml", "bin/ci", "bin/go", "bin/shellcheck", "Dockerfile", "go.mod", "go.sum", "internal/ciplan/plan.go", "internal/ciplan/cmd/ciplan/main.go"} {
		got := Plan([]string{"README.md", f}, sampleInputs())
		if !got.Full || !slices.Equal(got.Packages, []string{"./..."}) || !slices.Equal(got.TLASpecs, sampleInputs().Specs) || len(got.FuzzTargets) != len(AllFuzzTargets) {
			t.Errorf("%s: want the full plan, got %+v", f, got)
		}
	}
}

func TestPlanBench(t *testing.T) {
	got := Plan([]string{"bench/feed/main.go"}, sampleInputs())
	if slices.Contains(got.Packages, "./cmd/gantry") || !slices.Contains(got.Runs, benchFeedRun) || !slices.Equal(got.BenchModules, []string{"bench/feed"}) || !got.NeedTidy {
		t.Errorf("bench/feed: want its module, its tidy and TestBenchFeedIsANewApp, not all of ./cmd/gantry: %+v", got)
	}
	if got := Plan([]string{"bench/x.go"}, sampleInputs()); !got.NeedTidy || !slices.Equal(got.BenchModules, []string{"bench"}) {
		t.Errorf("bench/x.go: %+v", got)
	}
}

func TestPlanShellScript(t *testing.T) {
	got := Plan([]string{".githooks/pre-push"}, sampleInputs())
	if !slices.Equal(got.ShellScripts, []string{".githooks/pre-push"}) {
		t.Errorf("ShellScripts = %v", got.ShellScripts)
	}
	if got.Full || got.NeedCGO || got.NeedRace || got.NeedLint || got.NeedRelease {
		t.Errorf("the hook's change needs no Go checks but its test's: %+v", got)
	}
}

func TestPlanDropsARunOfAPackageTestedWhole(t *testing.T) {
	got := Plan([]string{"README.md", "devcheck/rules.go", "cmd/gantry/new.go"}, sampleInputs())
	if len(got.Runs) != 0 {
		t.Errorf("Runs = %v, of packages already tested whole", got.Runs)
	}
}

func TestPlanEmpty(t *testing.T) {
	if got := Plan(nil, sampleInputs()); got.Any() {
		t.Errorf("no change: %+v", got)
	}
}

func TestEverySpecHasAnOwner(t *testing.T) {
	owned := map[string]bool{}
	for _, specs := range specsModellingDir {
		for _, s := range specs {
			owned[s] = true
		}
	}
	for _, s := range realInputs(t).Specs {
		if !owned[s] {
			t.Errorf("%s models no package's code: add it to specsModellingDir (internal/ciplan/plan.go), so a change to that code runs it", s)
		}
	}
	for s := range owned {
		if !slices.Contains(realInputs(t).Specs, s) {
			t.Errorf("specsModellingDir names %s, which isn't in spec/", s)
		}
	}
}

func TestAllFuzzTargetsInRepoArePlanned(t *testing.T) {
	root := filepath.Join("..", "..")
	planned := map[string]bool{}
	for _, ft := range AllFuzzTargets {
		planned[ft.Package+"|"+ft.Name] = true
	}
	found := map[string]bool{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".cache", "bench", "testdata", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, 0)
		if err != nil {
			return err
		}
		relDir, err := filepath.Rel(root, filepath.Dir(p))
		if err != nil {
			return err
		}
		pkg := relPkg(normalizeDir(filepath.ToSlash(relDir)))
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Fuzz") {
				continue
			}
			key := pkg + "|^" + fn.Name.Name + "$"
			found[key] = true
			if !planned[key] {
				t.Errorf("fuzz target %s in %s is missing from ciplan.AllFuzzTargets", fn.Name.Name, pkg)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for key := range planned {
		if !found[key] {
			t.Errorf("ciplan.AllFuzzTargets entry %s has no matching Fuzz* test in repo", key)
		}
	}
}
