package devcheck

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
)

func visitCalls(s *Source, visit func(*ast.CallExpr, []ast.Node, map[string]string, int)) error {
	set, file, err := s.GoAST()
	if err != nil {
		return err
	}
	aliases := map[string]string{}
	for _, imp := range file.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		alias := filepath.Base(path)
		if imp.Name != nil {
			alias = imp.Name.Name
		}
		aliases[alias] = path
	}
	var stack []ast.Node
	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)
		if call, ok := n.(*ast.CallExpr); ok {
			visit(call, stack, aliases, set.Position(call.Pos()).Offset)
		}
		return true
	})
	return nil
}
func packageCall(call *ast.CallExpr, aliases map[string]string) (string, string) {
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		if pkg, ok := sel.X.(*ast.Ident); ok {
			return aliases[pkg.Name], sel.Sel.Name
		}
	}
	return "", ""
}
func checkRawAssets(s *Source, report func(int, string)) error {
	return visitCalls(s, func(call *ast.CallExpr, _ []ast.Node, aliases map[string]string, offset int) {
		pkg, name := packageCall(call, aliases)
		if pkg == "net/http" && (name == "FileServer" || name == "FileServerFS") {
			report(offset, "")
		}
	})
}
func checkDevelopmentGuard(s *Source, report func(int, string)) error {
	return visitCalls(s, func(call *ast.CallExpr, stack []ast.Node, aliases map[string]string, offset int) {
		pkg, name := packageCall(call, aliases)
		if pkg != "github.com/scttymn/gantry/web" || (name != "Development" && name != "DevelopmentWithRules" && name != "Timing") {
			return
		}
		for _, parent := range stack {
			branch, ok := parent.(*ast.IfStmt)
			if !ok || call.Pos() < branch.Body.Pos() || call.End() > branch.Body.End() {
				continue
			}
			condition, ok := branch.Cond.(*ast.CallExpr)
			if !ok {
				continue
			}
			selector, ok := condition.Fun.(*ast.SelectorExpr)
			if ok && selector.Sel.Name == "Development" && len(condition.Args) == 0 {
				return
			}
		}
		report(offset, "")
	})
}
func checkEnvironment(s *Source, report func(int, string)) error {
	if !strings.HasPrefix(s.Path, "config/") {
		return nil
	}
	return visitCalls(s, func(call *ast.CallExpr, _ []ast.Node, _ map[string]string, offset int) {
		if len(call.Args) != 2 {
			return
		}
		key, k := call.Args[0].(*ast.BasicLit)
		fallback, f := call.Args[1].(*ast.BasicLit)
		if k && f && key.Kind == token.STRING && fallback.Kind == token.STRING {
			keyText, _ := strconv.Unquote(key.Value)
			value, _ := strconv.Unquote(fallback.Value)
			if keyText == "GANTRY_ENV" && value != "production" {
				report(offset, "")
			}
		}
	})
}
