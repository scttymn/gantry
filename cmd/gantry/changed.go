package main

import (
	"bytes"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/scttymn/gantry/internal/gomod"
)

type affected struct {
	everythingBecause string
	pkgs              []string
}

func everything(format string, args ...any) affected {
	return affected{everythingBecause: fmt.Sprintf(format, args...)}
}

func affectedPackages(root, base string) affected {
	c, err := changedFiles(root, base)
	if err != nil {
		return everything("%v", err)
	}
	for _, f := range c.outside {
		if !strings.HasPrefix(f, ".github/") || !documentation(f) {
			return everything("%s changed, outside the app", f)
		}
	}
	graph, err := importGraph(root)
	if err != nil {
		return everything("%v", err)
	}
	changed, testsChanged := map[string]bool{}, map[string]bool{}
	for _, f := range c.inside {
		if runsEverything(f) {
			return everything("%s changed", f)
		}
		if skipped := graph.skippedDirOf(f); skipped == ".github" && documentation(f) {
			continue
		} else if skipped != "" {
			return everything("%s is under %s, which the import graph doesn't read", f, skipped)
		}
		dir, ok := graph.packageOf(root, f)
		switch {
		case ok && strings.HasSuffix(f, "_test.go") && parent(f) == dir:
			testsChanged[dir] = true
		case ok:
			changed[dir] = true
		case documentation(f):
		default:
			return everything("%s is in no package", f)
		}
	}
	return affected{pkgs: graph.toTest(changed, testsChanged)}
}

type changes struct {
	inside, outside []string
}

func changedFiles(root, base string) (changes, error) {
	git := func(args ...string) ([]byte, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("git %s: %v %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return out, nil
	}
	where, err := git("rev-parse", "--show-prefix")
	if err != nil {
		return changes{}, err
	}
	prefix := strings.TrimSpace(string(where))
	if base == "" {
		for _, candidate := range []string{"origin/main", "main"} {
			if _, err := git("rev-parse", "--verify", "--quiet", candidate+"^{commit}"); err == nil {
				base = candidate
				break
			}
		}
		if base == "" {
			return changes{}, errors.New("no origin/main or main to compare with: pass --base REF")
		}
	} else if _, err := git("rev-parse", "--verify", "--quiet", base+"^{commit}"); err != nil {
		return changes{}, fmt.Errorf("--base %s isn't a commit here", base)
	}
	since, err := git("merge-base", base, "HEAD")
	if err != nil {
		return changes{}, fmt.Errorf("HEAD shares no history with %s (a shallow clone needs fetch-depth: 0): %w", base, err)
	}
	var all []string
	diff := []string{"diff", "--raw", "-z", "--no-renames", "--no-relative", "--ignore-submodules=none"}
	for _, which := range [][]string{{strings.TrimSpace(string(since)), "HEAD"}, {"--cached"}, nil} {
		raw, err := git(slices.Concat(diff, which, []string{"--"})...)
		if err != nil {
			return changes{}, err
		}
		fields := strings.Split(string(raw), "\x00")
		for i := 0; i+1 < len(fields); i += 2 {
			if modes := strings.Fields(fields[i]); len(modes) > 1 && (modes[0] == ":160000" || modes[1] == "160000") {
				return changes{}, fmt.Errorf("%s is a submodule", fields[i+1])
			}
			all = append(all, fields[i+1])
		}
	}
	untracked, err := git("ls-files", "-z", "--others", "--exclude-standard", "--full-name", "--", ":/")
	if err != nil {
		return changes{}, err
	}
	all = append(all, strings.Split(string(untracked), "\x00")...)
	slices.Sort(all)
	var c changes
	for _, f := range slices.Compact(all) {
		if f == "" {
			continue
		}
		if rest, ok := strings.CutPrefix(f, prefix); ok {
			c.inside = append(c.inside, rest)
		} else {
			c.outside = append(c.outside, f)
		}
	}
	return c, nil
}

func runsEverything(f string) bool {
	return slices.Contains(moduleFiles, path.Base(f)) || slices.Contains(appFiles, f) ||
		slices.ContainsFunc(appDirs, func(dir string) bool { return strings.HasPrefix(f, dir) })
}

var (
	moduleFiles = []string{"go.mod", "go.sum", "go.work", "go.work.sum"}
	appFiles    = []string{"Dockerfile", "compose.yml", ".dockerignore", "Dockerfile.dockerignore"}
	appDirs     = []string{"config/", "db/", "test/"}
	unreadDirs  = []string{"vendor", "node_modules"}
)

func documentation(f string) bool {
	if strings.HasSuffix(f, ".go") || slices.Contains(moduleFiles, path.Base(f)) {
		return false
	}
	if strings.HasPrefix(f, "docs/") || strings.HasPrefix(f, ".github/") || strings.HasSuffix(f, ".md") {
		return true
	}
	switch path.Base(f) {
	case "LICENSE", ".gitignore", ".golangci.yml", ".gantry-check.json":
		return true
	}
	return false
}

type imports struct {
	dirs          map[string]string
	importers     map[string][]string
	testImporters map[string][]string
	skipped       []string
}

func (g imports) skippedDirOf(f string) string {
	segments := strings.Split(f, "/")
	for i, name := range segments[:len(segments)-1] {
		if name == "testdata" {
			break
		}
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || slices.Contains(unreadDirs, name) {
			return strings.Join(segments[:i+1], "/")
		}
	}
	for _, dir := range g.skipped {
		if strings.HasPrefix(f, dir+"/") {
			return dir
		}
	}
	return ""
}

func importGraph(root string) (imports, error) {
	module, err := gomod.ImportPath(root)
	if err != nil {
		return imports{}, fmt.Errorf("no go.mod for %s: %w", root, err)
	}
	g := imports{dirs: map[string]string{}, importers: map[string][]string{}, testImporters: map[string][]string{}}
	fset := token.NewFileSet()
	err = filepath.WalkDir(root, func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			name := entry.Name()
			switch {
			case rel == ".":
				return nil
			case name == "testdata":
				return filepath.SkipDir
			case strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || slices.Contains(unreadDirs, name):
				g.skipped = append(g.skipped, rel)
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(p, "go.mod")); err == nil {
				g.skipped = append(g.skipped, rel)
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") {
			return nil
		}
		file, err := parser.ParseFile(fset, p, nil, parser.ImportsOnly)
		if err != nil {
			return fmt.Errorf("reading the imports of %s: %w", rel, err)
		}
		dir := path.Dir(rel)
		importPath := module + "/" + dir
		if dir == "." {
			dir, importPath = "", module
		}
		g.dirs[dir] = importPath
		importers := g.importers
		if strings.HasSuffix(p, "_test.go") {
			importers = g.testImporters
		}
		for _, spec := range file.Imports {
			imported, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return fmt.Errorf("reading the imports of %s: %w", rel, err)
			}
			if !slices.Contains(importers[imported], importPath) {
				importers[imported] = append(importers[imported], importPath)
			}
		}
		return nil
	})
	if err != nil {
		return imports{}, err
	}
	return g, nil
}

func (g imports) packageOf(root, f string) (string, bool) {
	dir := parent(f)
	if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(f))); err != nil && strings.HasSuffix(f, ".go") {
		_, ok := g.dirs[dir]
		return dir, ok
	}
	for {
		if _, ok := g.dirs[dir]; ok {
			return dir, true
		}
		if dir == "" {
			return "", false
		}
		dir = parent(dir)
	}
}

func parent(p string) string {
	if dir := path.Dir(p); dir != "." {
		return dir
	}
	return ""
}

func (g imports) toTest(changed, testsChanged map[string]bool) []string {
	byPath := map[string]string{}
	for dir, importPath := range g.dirs {
		byPath[importPath] = dir
	}
	seen := map[string]bool{}
	var queue []string
	for dir := range changed {
		seen[g.dirs[dir]] = true
		queue = append(queue, g.dirs[dir])
	}
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]
		for _, importer := range g.importers[next] {
			if !seen[importer] {
				seen[importer] = true
				queue = append(queue, importer)
			}
		}
	}
	tested := maps.Clone(seen)
	for dir := range testsChanged {
		tested[g.dirs[dir]] = true
	}
	for importPath := range seen {
		for _, importer := range g.testImporters[importPath] {
			tested[importer] = true
		}
	}
	var pkgs []string
	for importPath := range tested {
		if dir := byPath[importPath]; dir == "" {
			pkgs = append(pkgs, ".")
		} else {
			pkgs = append(pkgs, "./"+dir)
		}
	}
	slices.Sort(pkgs)
	return pkgs
}
