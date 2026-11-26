package ciplan

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/scttymn/gantry/internal/gomod"
	"github.com/scttymn/gantry/internal/pins"
)

// Inputs is what Plan reads of the checkout: the module's packages, with
// their imports in both builds CI tests (bin/go's, without cgo, and an app's,
// with cgo and SQLite's tags, whose db/sqlite_c.go imports internal/pins);
// spec/*.tla; the shell scripts bin/shellcheck checks; and the module's
// packages bench/feed builds and a generated app imports
// (cmd/gantry/templates), whose changes TestGeneratorMatrix tests.
type Inputs struct {
	Module           string
	Packages         []Package
	Specs            []string
	Scripts          []string
	BenchFeedImports []string
	GeneratorImports []string
}

// Load reads Plan's inputs from the checkout at root. Any it can't read is an
// error, not an empty list: an empty list plans nothing for it.
func Load(root string) (Inputs, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return Inputs{}, err
	}
	in := Inputs{}
	if in.Module, err = gomod.ImportPath(root); err != nil {
		return Inputs{}, err
	}
	if in.Specs, err = listSpecs(root); err != nil {
		return Inputs{}, err
	}
	if in.Scripts, err = listScripts(root); err != nil {
		return Inputs{}, err
	}
	if in.Packages, err = listPackages(root); err != nil {
		return Inputs{}, err
	}
	known := map[string]bool{}
	for _, p := range in.Packages {
		known[p.ImportPath] = true
	}
	for _, p := range in.Packages {
		for _, imp := range slices.Concat(p.UnitImports, p.XUnitImports) {
			if inModule(imp, in.Module) && !known[imp] {
				return Inputs{}, fmt.Errorf("%s's tests import %s, which isn't a package of %s", p.ImportPath, imp, in.Module)
			}
		}
	}
	if in.BenchFeedImports, err = listBenchFeedImports(root, in.Module, known); err != nil {
		return Inputs{}, err
	}
	if in.GeneratorImports, err = listGeneratorImports(root, in.Module, known); err != nil {
		return Inputs{}, err
	}
	return in, nil
}

// FullPlanAt is FullPlan for the checkout at root. Every check needs no
// package graph, so it lists none, and downloads no module.
func FullPlanAt(root string) (Result, error) {
	specs, err := listSpecs(root)
	if err != nil {
		return Result{}, err
	}
	scripts, err := listScripts(root)
	if err != nil {
		return Result{}, err
	}
	return FullPlan(specs, scripts), nil
}

// Changed lists the paths changed on HEAD since its merge base with base, as
// the old path and the new of a rename, and with worktree those staged,
// unstaged and untracked too. An error means it can't say what changed.
func Changed(root, base string, worktree bool) ([]string, error) {
	lists := [][]string{{"diff", "--no-renames", "--name-only", "-z", base + "...HEAD", "--"}}
	if worktree {
		lists = append(lists,
			[]string{"diff", "--no-renames", "--name-only", "-z", "--cached", "--"},
			[]string{"diff", "--no-renames", "--name-only", "-z", "--"},
			[]string{"ls-files", "-z", "--others", "--exclude-standard"},
		)
	}
	var files []string
	for _, args := range lists {
		out, err := command(root, nil, "git", append([]string{"-c", "safe.directory=*"}, args...)...)
		if err != nil {
			return nil, err
		}
		files = append(files, strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")...)
	}
	return normalizePaths(files), nil
}

func inModule(imp, module string) bool {
	return imp == module || strings.HasPrefix(imp, module+"/")
}

func command(dir string, env []string, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func lines(out []byte) []string {
	var found []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			found = append(found, line)
		}
	}
	return found
}

func listSpecs(root string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(root, "spec", "*.tla"))
	if err != nil {
		return nil, err
	}
	var specs []string
	for _, m := range matches {
		specs = append(specs, "spec/"+filepath.Base(m))
	}
	return specs, nil
}

func listScripts(root string) ([]string, error) {
	out, err := command(root, []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=safe.directory", "GIT_CONFIG_VALUE_0=*"}, filepath.Join(root, "bin", "shellcheck"), "--list")
	if err != nil {
		return nil, err
	}
	return lines(out), nil
}

func listPackages(root string) ([]Package, error) {
	byPath := map[string]*Package{}
	files := map[string]string{}
	var order []string
	for _, build := range []struct{ env, flags []string }{
		{[]string{"CGO_ENABLED=0"}, nil},
		{[]string{"CGO_ENABLED=1"}, []string{"-tags=" + pins.SQLiteTags}},
	} {
		out, err := command(root, build.env, "go", slices.Concat([]string{"list", "-buildvcs=false", "-json"}, build.flags, []string{"./..."})...)
		if err != nil {
			return nil, err
		}
		dec := json.NewDecoder(bytes.NewReader(out))
		for {
			var p struct {
				ImportPath, Dir string
				Imports         []string
				UnitImports     []string `json:"testimports"`
				XUnitImports    []string `json:"xtestimports"`
				GoFiles         []string
				CgoFiles        []string
				UnitFiles       []string `json:"testgofiles"`
				XUnitFiles      []string `json:"xtestgofiles"`
				Error           *struct{ Err string }
				DepsErrors      []struct{ Err string }
			}
			if err := dec.Decode(&p); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				return nil, err
			}
			if p.Error != nil {
				return nil, fmt.Errorf("%s: %s", p.ImportPath, p.Error.Err)
			}
			if len(p.DepsErrors) > 0 {
				return nil, fmt.Errorf("%s: %s", p.ImportPath, p.DepsErrors[0].Err)
			}
			rel, err := filepath.Rel(root, p.Dir)
			if err != nil {
				return nil, err
			}
			got, ok := byPath[p.ImportPath]
			if !ok {
				got = &Package{ImportPath: p.ImportPath, Dir: filepath.ToSlash(rel)}
				byPath[p.ImportPath] = got
				order = append(order, p.ImportPath)
			}
			got.Imports = union(got.Imports, p.Imports)
			got.UnitImports = union(got.UnitImports, p.UnitImports)
			got.XUnitImports = union(got.XUnitImports, p.XUnitImports)
			built := strings.Join(slices.Concat(p.GoFiles, p.CgoFiles, p.UnitFiles, p.XUnitFiles), " ")
			if was, ok := files[p.ImportPath]; ok && was != built {
				got.DiffersWithCGO = true
			}
			files[p.ImportPath] = built
		}
	}
	pkgs := make([]Package, 0, len(order))
	for _, imp := range order {
		pkgs = append(pkgs, *byPath[imp])
	}
	return pkgs, nil
}

func union(a, b []string) []string {
	for _, s := range b {
		if !slices.Contains(a, s) {
			a = append(a, s)
		}
	}
	return a
}

func listBenchFeedImports(root, module string, known map[string]bool) ([]string, error) {
	dir := filepath.Join(root, "bench", "feed")
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	out, err := command(dir, []string{"CGO_ENABLED=0"}, "go", "list", "-buildvcs=false", "-json", "./...")
	if err != nil {
		return nil, err
	}
	var imports []string
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var p struct {
			ImportPath   string
			Deps         []string
			UnitImports  []string `json:"testimports"`
			XUnitImports []string `json:"xtestimports"`
			Error        *struct{ Err string }
			DepsErrors   []struct{ Err string }
		}
		if err := dec.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, err
		}
		if p.Error != nil {
			return nil, fmt.Errorf("bench/feed: %s: %s", p.ImportPath, p.Error.Err)
		}
		if len(p.DepsErrors) > 0 {
			return nil, fmt.Errorf("bench/feed: %s: %s", p.ImportPath, p.DepsErrors[0].Err)
		}
		for _, imp := range slices.Concat(p.Deps, p.UnitImports, p.XUnitImports) {
			if !inModule(imp, module) || slices.Contains(imports, imp) {
				continue
			}
			if !known[imp] {
				return nil, fmt.Errorf("bench/feed: %s imports %s, which isn't a package of %s", p.ImportPath, imp, module)
			}
			imports = append(imports, imp)
		}
	}
	return imports, nil
}

func listGeneratorImports(root, module string, known map[string]bool) ([]string, error) {
	dir := filepath.Join(root, "cmd", "gantry", "templates")
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	quoted := regexp.MustCompile(`"(` + regexp.QuoteMeta(module) + `/[\w./-]+)"`)
	var imports []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		text, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, m := range quoted.FindAllSubmatch(text, -1) {
			imp := string(m[1])
			if !known[imp] {
				rel, _ := filepath.Rel(root, p)
				return fmt.Errorf("%s imports %s, which isn't a package of %s: an app generated with it doesn't build", filepath.ToSlash(rel), imp, module)
			}
			if !slices.Contains(imports, imp) {
				imports = append(imports, imp)
			}
		}
		return nil
	})
	slices.Sort(imports)
	return imports, err
}
