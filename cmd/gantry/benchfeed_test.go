package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var benchFeedOwn = map[string]string{
	".env":                 "random keys, each app its own, ignored by git",
	"app/routes.go":        "the challenge's routes",
	"app/home_test.go":     "the API's 404 and the bench's routes",
	"app/features_test.go": "the API's bearer token signs a visitor in",
	"test/features.yaml":   "the challenge's routes",
	"config/config.go":     "JWT_SECRET",
	"cmd/feed/main.go":     "the app's JWT secret and start time",
	"db/schema.sql":        "the challenge's tables",
	"db/seeds/seeds.go":    "the challenge's users and posts",
	"sqlc.yaml":            "emit_json_tags: a row is the API's JSON",
}

var benchFeedPart = map[string]func(bench string) (before, generated, after string){
	"go.mod": func(b string) (string, string, string) {
		i := strings.Index(b, "\n)\n")
		if i < 0 {
			return "", b, ""
		}
		return "", b[:i+3], b[i+3:]
	},
	"README.md": func(b string) (string, string, string) {
		before, rest, ok := strings.Cut(b, "\n\n")
		if !ok {
			return "", b, ""
		}
		return before + "\n\n", rest, ""
	},
}

func TestBenchFeedIsANewApp(t *testing.T) {
	root := t.TempDir()
	if err := newApp(root, []string{"feed", "--skip-houston"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	generated, bench := filepath.Join(root, "feed"), filepath.Join("..", "..", "bench", "feed")
	seen := map[string]bool{}
	err := filepath.WalkDir(generated, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(generated, path)
		rel = filepath.ToSlash(rel)
		seen[rel] = true
		if _, ok := benchFeedOwn[rel]; ok {
			if strings.HasSuffix(rel, ".go") {
				have := declared(t, filepath.Join(bench, rel))
				for _, name := range declared(t, path) {
					if !slices.Contains(have, name) {
						t.Errorf("bench/feed/%s is its own, and lacks gantry new's %s: add it", rel, name)
					}
				}
			}
			return nil
		}
		want, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		target := filepath.Join(bench, rel)
		got, err := os.ReadFile(target)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		missing := err != nil
		before, part, after := "", string(got), ""
		if split, ok := benchFeedPart[rel]; ok && !missing {
			before, part, after = split(string(got))
		}
		if !missing && part == string(want) {
			return nil
		}
		if *update {
			info, _ := d.Info()
			os.MkdirAll(filepath.Dir(target), 0o755)
			return os.WriteFile(target, []byte(before+string(want)+after), info.Mode().Perm())
		}
		t.Errorf("bench/feed/%s isn't what gantry new writes: go test ./cmd/gantry -update rewrites it, then GANTRY_DIR=bench/feed bin/go sh -c 'templ generate && go mod tidy'", rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for rel := range benchFeedOwn {
		if !seen[rel] {
			t.Errorf("gantry new doesn't write %s any more: take it out of benchFeedOwn", rel)
		}
	}
}

func declared(t *testing.T, path string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			name := d.Name.Name
			if d.Recv != nil {
				recv := d.Recv.List[0].Type
				if star, ok := recv.(*ast.StarExpr); ok {
					recv = star.X
				}
				if id, ok := recv.(*ast.Ident); ok {
					name = id.Name + "." + name
				}
			}
			names = append(names, name)
		case *ast.GenDecl:
			for _, s := range d.Specs {
				switch s := s.(type) {
				case *ast.TypeSpec:
					names = append(names, s.Name.Name)
				case *ast.ValueSpec:
					for _, n := range s.Names {
						names = append(names, n.Name)
					}
				}
			}
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if c, ok := n.(*ast.CaseClause); ok {
			for _, e := range c.List {
				if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					names = append(names, "case "+lit.Value)
				}
			}
		}
		return true
	})
	return names
}
