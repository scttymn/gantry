package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

type goListPackage struct {
	ImportPath   string   `json:"importpath"`
	Dir          string   `json:"dir"`
	Imports      []string `json:"imports"`
	UnitImports  []string `json:"testimports"`
	XUnitImports []string `json:"xtestimports"`
}

var gitOutputIn = func(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd.Output()
}

var goListIn = func(dir string) ([]byte, error) {
	cmd := exec.Command("go", "list", "-json", "./...")
	cmd.Dir = dir
	return cmd.Output()
}

func gitChangedFiles(root, base string) ([]string, string, bool) {
	if _, err := gitOutputIn(root, "rev-parse", "--is-inside-work-tree"); err != nil {
		return nil, "", false
	}
	if base == "" {
		for _, cand := range []string{"origin/main", "main"} {
			if _, err := gitOutputIn(root, "rev-parse", "--verify", "-q", cand+"^{commit}"); err == nil {
				base = cand
				break
			}
		}
	} else if _, err := gitOutputIn(root, "rev-parse", "--verify", "-q", base+"^{commit}"); err != nil {
		return nil, "", false
	}
	seen := map[string]bool{}
	var files []string
	collect := func(args ...string) {
		raw, err := gitOutputIn(root, args...)
		if err != nil {
			return
		}
		for _, line := range strings.Split(string(raw), "\n") {
			clean := path.Clean(strings.TrimSpace(strings.ReplaceAll(line, "\\", "/")))
			clean = strings.TrimPrefix(clean, "./")
			if clean == "" || clean == "." || seen[clean] {
				continue
			}
			seen[clean] = true
			files = append(files, clean)
		}
	}
	if base != "" {
		collect("diff", "--name-only", base+"...HEAD")
	}
	collect("diff", "--name-only", "--cached")
	collect("diff", "--name-only")
	collect("ls-files", "--others", "--exclude-standard")
	slices.Sort(files)
	return files, base, true
}

func affectedGoPackages(root string, changed []string) ([]string, bool, error) {
	if len(changed) == 0 {
		return nil, false, nil
	}
	hasCode := false
	for _, f := range changed {
		if f == "go.mod" || f == "go.sum" || f == "Dockerfile" ||
			f == "db/schema.sql" || strings.HasPrefix(f, "db/migrations/") ||
			strings.HasPrefix(f, "config/") || strings.HasPrefix(f, "test/") {
			return nil, true, nil
		}
		switch path.Ext(f) {
		case ".go", ".templ", ".sql", ".html", ".css", ".js", ".yaml", ".yml", ".json":
			if !strings.HasPrefix(f, ".github/") {
				hasCode = true
			}
		}
	}
	if !hasCode {
		return nil, false, nil
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, true, err
	}
	raw, err := goListIn(absRoot)
	if err != nil {
		return nil, true, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dirToImport := map[string]string{}
	importToDir := map[string]string{}
	revDeps := map[string][]string{}
	for {
		var lp goListPackage
		if err := dec.Decode(&lp); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, true, nil
		}
		rel, err := filepath.Rel(absRoot, lp.Dir)
		if err != nil {
			continue
		}
		d := normalizeRelDir(filepath.ToSlash(rel))
		dirToImport[d] = lp.ImportPath
		importToDir[lp.ImportPath] = d
		for _, imp := range slices.Concat(lp.Imports, lp.UnitImports, lp.XUnitImports) {
			revDeps[imp] = append(revDeps[imp], lp.ImportPath)
		}
	}
	if len(dirToImport) == 0 {
		return nil, true, nil
	}
	seeds := map[string]bool{}
	for _, f := range changed {
		if imp, ok := matchPackageDir(f, dirToImport); ok {
			seeds[imp] = true
		} else if !isDocOrMetaFile(f) {
			return nil, true, nil
		}
	}
	if len(seeds) == 0 {
		return nil, false, nil
	}
	visited := map[string]bool{}
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
	pkgs := make([]string, 0, len(visited))
	for imp := range visited {
		d := importToDir[imp]
		if d == "" {
			pkgs = append(pkgs, ".")
		} else {
			pkgs = append(pkgs, "./"+d)
		}
	}
	slices.Sort(pkgs)
	return pkgs, false, nil
}

func normalizeRelDir(dir string) string {
	dir = path.Clean(strings.TrimSpace(strings.ReplaceAll(dir, "\\", "/")))
	dir = strings.TrimPrefix(dir, "./")
	if dir == "." {
		return ""
	}
	return dir
}

func matchPackageDir(file string, dirToImport map[string]string) (string, bool) {
	cur := path.Dir(file)
	if cur == "." {
		cur = ""
	}
	for {
		if imp, ok := dirToImport[cur]; ok {
			return imp, true
		}
		if cur == "" {
			return "", false
		}
		next := path.Dir(cur)
		if next == "." || next == cur {
			cur = ""
		} else {
			cur = next
		}
	}
}

func isDocOrMetaFile(f string) bool {
	if strings.HasPrefix(f, ".github/") || strings.HasPrefix(f, "docs/") {
		return true
	}
	switch path.Base(f) {
	case "README.md", "AGENTS.md", "CONTRIBUTING.md", "LICENSE", ".gitignore", ".dockerignore", ".gantry-check.json", ".golangci.yml":
		return true
	}
	return strings.HasSuffix(f, ".md")
}
