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

func sampleGraph() ([]Package, []string, []string, []string) {
	pkgs := []Package{
		{ImportPath: testMod, Dir: "."},
		{ImportPath: testMod + "/internal/env", Dir: "internal/env"},
		{ImportPath: testMod + "/internal/hygiene", Dir: "internal/hygiene"},
		{ImportPath: testMod + "/internal/pins", Dir: "internal/pins"},
		{ImportPath: testMod + "/internal/testenv", Dir: "internal/testenv"},
		{ImportPath: testMod + "/internal/featuremap", Dir: "internal/featuremap"},
		{ImportPath: testMod + "/db", Dir: "db", TestImports: []string{testMod + "/internal/testenv"}},
		{ImportPath: testMod + "/token", Dir: "token"},
		{ImportPath: testMod + "/sign", Dir: "sign", Imports: []string{testMod + "/db", testMod + "/internal/env"}},
		{ImportPath: testMod + "/web", Dir: "web", Imports: []string{testMod + "/sign", testMod + "/devcheck"}},
		{ImportPath: testMod + "/devcheck", Dir: "devcheck"},
		{ImportPath: testMod + "/images", Dir: "images", Imports: []string{testMod + "/web"}},
		{ImportPath: testMod + "/jobs", Dir: "jobs", Imports: []string{testMod + "/db"}, TestImports: []string{testMod + "/internal/testenv"}},
		{ImportPath: testMod + "/auth", Dir: "auth", Imports: []string{testMod + "/db", testMod + "/sign", testMod + "/token", testMod + "/web"}, TestImports: []string{testMod + "/internal/testenv"}},
		{ImportPath: testMod + "/testkit", Dir: "testkit", Imports: []string{testMod + "/db", testMod + "/web", testMod + "/internal/featuremap"}, TestImports: []string{testMod + "/internal/testenv"}},
		{ImportPath: testMod + "/cmd/gantry", Dir: "cmd/gantry", Imports: []string{testMod + "/devcheck", testMod + "/internal/pins", testMod + "/testkit"}, TestImports: []string{testMod + "/internal/testenv"}},
	}
	specs := []string{"spec/Auth.tla", "spec/Jobs.tla", "spec/PageCache.tla"}
	scripts := []string{"bin/ci", "bin/go", "bin/shellcheck", "bin/tlc", ".githooks/pre-push"}
	benchFeed := []string{testMod + "/db", testMod + "/web", testMod + "/sign"}
	return pkgs, specs, scripts, benchFeed
}

func TestPlanDocsOnly(t *testing.T) {
	pkgs, specs, scripts, benchFeed := sampleGraph()
	got := Plan(testMod, []string{"docs/development.md"}, pkgs, specs, scripts, benchFeed)
	if !got.Any() {
		t.Fatal("expected doc checks to run")
	}
	if !slices.Equal(got.Packages, []string{".", "./devcheck", "./internal/hygiene"}) {
		t.Errorf("Packages = %v", got.Packages)
	}
	if !got.DocGantryTest {
		t.Error("expected DocGantryTest for docs/development.md")
	}
	if got.NeedCGO || got.NeedPostgres || got.NeedRace || got.NeedLint || got.NeedVet || got.NeedFmt || got.NeedTidy || got.NeedVuln || got.NeedRelease {
		t.Errorf("unexpected heavy checks for doc edit: %+v", got)
	}
	if len(got.TLASpecs) != 0 || len(got.FuzzTargets) != 0 || len(got.ShellScripts) != 0 || len(got.BenchModules) != 0 {
		t.Errorf("unexpected extra targets for doc edit: %+v", got)
	}
}

func TestPlanSingleTLASpec(t *testing.T) {
	pkgs, specs, scripts, benchFeed := sampleGraph()
	got := Plan(testMod, []string{"spec/Auth.tla"}, pkgs, specs, scripts, benchFeed)
	if !slices.Equal(got.TLASpecs, []string{"spec/Auth.tla"}) {
		t.Errorf("TLASpecs = %v, want [spec/Auth.tla]", got.TLASpecs)
	}
	if got.NeedsToolchain() || got.NeedRelease || len(got.Packages) > 0 {
		t.Errorf("spec-only edit should not run Go toolchain: %+v", got)
	}
}

func TestPlanLeafPackageClosure(t *testing.T) {
	pkgs, specs, scripts, benchFeed := sampleGraph()
	got := Plan(testMod, []string{"token/token.go"}, pkgs, specs, scripts, benchFeed)
	if !slices.Equal(got.Packages, []string{"./auth", "./token"}) {
		t.Errorf("Packages = %v, want [./auth ./token]", got.Packages)
	}
	if !got.NeedPostgres || !got.NeedRace || !got.NeedVet || !got.NeedFmt || !got.NeedLint {
		t.Errorf("expected test/race/vet/fmt/lint on affected packages: %+v", got)
	}
	if got.NeedCGO || got.NeedLintCGO || got.NeedTidy || got.NeedVuln || got.NeedRelease {
		t.Errorf("leaf package edit should skip cgo/tidy/vuln/release: %+v", got)
	}
	if len(got.BenchModules) != 0 || len(got.FuzzTargets) != 0 || len(got.TLASpecs) != 0 {
		t.Errorf("unexpected extra checks for token edit: %+v", got)
	}
}

func TestPlanWebTargetsFuzzAndPageCacheSpec(t *testing.T) {
	pkgs, specs, scripts, benchFeed := sampleGraph()
	got := Plan(testMod, []string{"web/cache.go"}, pkgs, specs, scripts, benchFeed)
	if !slices.Equal(got.TLASpecs, []string{"spec/PageCache.tla"}) {
		t.Errorf("TLASpecs = %v, want [spec/PageCache.tla]", got.TLASpecs)
	}
	if got.NeedCGO || got.NeedRelease || got.NeedTidy || got.NeedVuln {
		t.Errorf("web edit should skip cgo/release/tidy/vuln: %+v", got)
	}
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

func TestPlanDBTriggersCGOAndPostgres(t *testing.T) {
	pkgs, specs, scripts, benchFeed := sampleGraph()
	got := Plan(testMod, []string{"db/sqlite_c.go"}, pkgs, specs, scripts, benchFeed)
	if !got.NeedCGO || !got.NeedLintCGO || !got.NeedPostgres || !got.NeedRace {
		t.Errorf("db edit must enable cgo/postgres/race: %+v", got)
	}
	if got.NeedTidy || got.NeedVuln || got.NeedRelease {
		t.Errorf("db edit should not run tidy/vuln/release: %+v", got)
	}
}

func TestPlanGoModTriggersFullChecks(t *testing.T) {
	pkgs, specs, scripts, benchFeed := sampleGraph()
	got := Plan(testMod, []string{"go.mod"}, pkgs, specs, scripts, benchFeed)
	if !got.NeedTidy || !got.NeedVuln || !got.NeedCGO || !got.NeedRace || !got.NeedRelease {
		t.Errorf("go.mod edit must run tidy/vuln/cgo/race/release: %+v", got)
	}
	if !slices.Equal(got.Packages, []string{"./..."}) {
		t.Errorf("Packages = %v, want [./...]", got.Packages)
	}
}

func TestPlanShellScriptTargeted(t *testing.T) {
	pkgs, specs, scripts, benchFeed := sampleGraph()
	got := Plan(testMod, []string{"bin/go"}, pkgs, specs, scripts, benchFeed)
	if !slices.Equal(got.ShellScripts, []string{"bin/go"}) {
		t.Errorf("ShellScripts = %v, want [bin/go]", got.ShellScripts)
	}
	if got.NeedCGO || got.NeedRace || got.NeedLint || got.NeedRelease {
		t.Errorf("bin/go edit should not run heavy Go checks: %+v", got)
	}
}

func TestPlanEmpty(t *testing.T) {
	pkgs, specs, scripts, benchFeed := sampleGraph()
	got := Plan(testMod, nil, pkgs, specs, scripts, benchFeed)
	if got.Any() {
		t.Errorf("empty change list should produce empty plan: %+v", got)
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
		pkg := relPkg(filepath.ToSlash(relDir))
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
