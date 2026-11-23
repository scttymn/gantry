package jobs

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// unfenced are the functions whose UPDATE of gantry_jobs needn't say which
// claim or state it changes, and why.
var unfenced = map[string]string{
	"claim": "it runs in the claim's own transaction, on rows that transaction just read (FOR UPDATE SKIP LOCKED on Postgres; SQLite's BEGIN IMMEDIATE holds the one writer)",
}

var fence = regexp.MustCompile(`(?is)\bWHERE\b.*\b(claimed_by|state\s*=)`)

// Every UPDATE of gantry_jobs says in its WHERE which claim (claimed_by) or
// state it changes a job from, so a worker that stalled past Dead, its job
// since gone back and claimed again, can't settle what's no longer its own
// (#32). settle fences on the claim; a function listed in unfenced says why
// it needn't.
func TestJobUpdatesAreFenced(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	set, updates := token.NewFileSet(), 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(set, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				query, _ := strconv.Unquote(lit.Value)
				if !regexp.MustCompile(`(?i)\bUPDATE\s+gantry_jobs\b`).MatchString(query) {
					return true
				}
				updates++
				if _, ok := unfenced[fn.Name.Name]; !ok && !fence.MatchString(query) {
					t.Errorf("%s: %s updates gantry_jobs with no claim or state in its WHERE; settle a job through settle, which fences on its claim, or say in unfenced why this one needn't:\n%s", set.Position(lit.Pos()), fn.Name.Name, query)
				}
				return true
			})
		}
	}
	if updates == 0 {
		t.Fatal("found no UPDATE of gantry_jobs to check")
	}
}
