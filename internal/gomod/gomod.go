// Package gomod finds the Go module a directory is in, for the CLI and
// Strict alike: an app made with gantry new --in-module has no go.mod of
// its own, and its packages are under the enclosing module's path.
package gomod

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrNoModule is Enclosing's error when there's no go.mod at or above dir.
var ErrNoModule = errors.New("no go.mod here or above")

// Enclosing is the module dir is in, from the nearest go.mod at or above
// it: the module's root and its path.
func Enclosing(dir string) (root, path string, err error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", "", err
	}
	for d := abs; ; d = filepath.Dir(d) {
		if data, err := os.ReadFile(filepath.Join(d, "go.mod")); err == nil {
			if path := Path(data); path != "" {
				return d, path, nil
			}
			return "", "", fmt.Errorf("%s names no module", filepath.Join(d, "go.mod"))
		}
		if filepath.Dir(d) == d {
			return "", "", ErrNoModule
		}
	}
}

// ImportPath is dir's import path: its module's path, and dir's below the
// module's root.
func ImportPath(dir string) (string, error) {
	root, path, err := Enclosing(dir)
	if err != nil {
		return "", err
	}
	abs, _ := filepath.Abs(dir)
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == "." {
		return path, err
	}
	return path + "/" + filepath.ToSlash(rel), nil
}

// Path is a go.mod's module path, "" when it names none.
func Path(gomod []byte) string {
	for _, line := range strings.Split(string(gomod), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module"); ok && rest != "" && (rest[0] == ' ' || rest[0] == '\t') {
			rest, _, _ = strings.Cut(rest, "//")
			return strings.Trim(strings.TrimSpace(rest), `"`)
		}
	}
	return ""
}
