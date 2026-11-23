package gomod

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPath(t *testing.T) {
	for gomod, want := range map[string]string{
		"module example.com/shop\n\ngo 1.27\n":       "example.com/shop",
		"// a comment\nmodule \"example.com/q\"\n":   "example.com/q",
		"module example.com/c // trailing\n":         "example.com/c",
		"module\texample.com/tab\n":                  "example.com/tab",
		"modules example.com/x\n":                    "",
		"":                                           "",
		"go 1.27\nrequire (\n\tmodule.org/x v1\n)\n": "",
	} {
		if got := Path([]byte(gomod)); got != want {
			t.Errorf("Path(%q) = %q, want %q", gomod, got, want)
		}
	}
}

func TestImportPath(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "apps", "control"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportPath(root); !errors.Is(err, ErrNoModule) {
		t.Fatalf("no go.mod: %v", err)
	}
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/big\n"), 0o644)
	for dir, want := range map[string]string{
		root:                                   "example.com/big",
		filepath.Join(root, "apps", "control"): "example.com/big/apps/control",
	} {
		if got, err := ImportPath(dir); err != nil || got != want {
			t.Errorf("ImportPath(%s) = %q, %v; want %q", dir, got, err, want)
		}
	}
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("go 1.27\n"), 0o644)
	if _, err := ImportPath(root); err == nil {
		t.Fatal("a go.mod naming no module was accepted")
	}
}
