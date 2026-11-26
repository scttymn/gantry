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
	if in.BenchFeedImports, err = listBenchFeedImports(root, in.Module); err != nil {
		return Inputs{}, err
	}
	if in.GeneratorImports, err = listGeneratorImports(root, in.Module, in.Packages); err != nil {
		return Inputs{}, err
	}
	return in, nil
}

// Changed lists the paths changed on HEAD since its merge base with base, as
// the old path and the new of a rename, and with worktree those staged,
// unstaged and untracked too. An error means it can't say what changed.
func Changed(root, base string, worktree bool) ([]string, error) {
	lists := [][]string{{"diff", "--no-renames", "--name-only", base + "...HEAD", "--"}}
	if worktree {
		lists = append(lists,
			[]string{"diff", "--no-renames", "--name-only", "--cached", "--"},
			[]string{"diff", "--no-renames", "--name-only", "--"},
			[]string{"ls-files", "--others", "--exclude-standard"},
		)
	}
	var files []string
	for _, args := range lists {
		out, err := command(root, nil, "git", append([]string{"-c", "safe.directory=*", "-c", "core.quotePath=false"}, args...)...)
		if err != nil {
			return nil, err
		}
		files = append(files, lines(out)...)
	}
	return normalizePaths(files), nil
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
	var order []string
	for _, env := range [][]string{
		{"CGO_ENABLED=0", "GOFLAGS="},
		{"CGO_ENABLED=1", "GOFLAGS=-tags=" + pins.SQLiteTags},
	} {
		out, err := command(root, env, "go", "list", "-json", "./...")
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
			}
			if err := dec.Decode(&p); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				return nil, err
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

func listBenchFeedImports(root, module string) ([]string, error) {
	dir := filepath.Join(root, "bench", "feed")
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	out, err := command(dir, []string{"CGO_ENABLED=0", "GOFLAGS="}, "go", "list", "-f", `{{join .Deps "\n"}}`, "./...")
	if err != nil {
		return nil, err
	}
	var imports []string
	for _, imp := range lines(out) {
		if (imp == module || strings.HasPrefix(imp, module+"/")) && !slices.Contains(imports, imp) {
			imports = append(imports, imp)
		}
	}
	return imports, nil
}

func listGeneratorImports(root, module string, pkgs []Package) ([]string, error) {
	dir := filepath.Join(root, "cmd", "gantry", "templates")
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	known := map[string]bool{}
	for _, p := range pkgs {
		known[p.ImportPath] = true
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
