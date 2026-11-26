package jobs

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

func TestDeadlineLoopsSleep(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	set, loops := token.NewFileSet(), 0
	for _, name := range files {
		file, err := parser.ParseFile(set, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			loop, ok := n.(*ast.ForStmt)
			if !ok || !waitsFor(loop) {
				return true
			}
			loops++
			sleeps := false
			ast.Inspect(loop.Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Sleep" {
						if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "time" {
							sleeps = true
						}
					}
				}
				return !sleeps
			})
			if !sleeps {
				t.Errorf("%s: a loop that waits for a deadline spins without time.Sleep; sleep a few milliseconds a turn, as the other waits do", set.Position(loop.Pos()))
			}
			return true
		})
	}
	if loops == 0 {
		t.Fatal("found no deadline loop to check")
	}
}

func waitsFor(loop *ast.ForStmt) bool {
	init, ok := loop.Init.(*ast.AssignStmt)
	if !ok {
		return false
	}
	for _, lhs := range init.Lhs {
		if id, ok := lhs.(*ast.Ident); ok && id.Name == "deadline" {
			return true
		}
	}
	return false
}
