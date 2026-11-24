package devcheck

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	gantryWeb = "github.com/scttymn/gantry/web"
	gantryDB  = "github.com/scttymn/gantry/db"
	templPkg  = "github.com/a-h/templ"
)

func importAliases(file *ast.File) map[string]string {
	aliases := map[string]string{}
	for _, imp := range file.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		alias := filepath.Base(path)
		if imp.Name != nil {
			alias = imp.Name.Name
		}
		aliases[alias] = path
	}
	return aliases
}

func visitNodes(s *Source, visit func(n ast.Node, stack []ast.Node, aliases map[string]string, offset func(token.Pos) int)) error {
	set, file, err := s.GoAST()
	if err != nil {
		return err
	}
	aliases := importAliases(file)
	offset := func(p token.Pos) int { return set.Position(p).Offset }
	var stack []ast.Node
	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)
		visit(n, stack, aliases, offset)
		return true
	})
	return nil
}

func visitCalls(s *Source, visit func(*ast.CallExpr, []ast.Node, map[string]string, int)) error {
	return visitNodes(s, func(n ast.Node, stack []ast.Node, aliases map[string]string, offset func(token.Pos) int) {
		if call, ok := n.(*ast.CallExpr); ok {
			visit(call, stack, aliases, offset(call.Pos()))
		}
	})
}

func packageCall(call *ast.CallExpr, aliases map[string]string) (string, string) {
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		return packageSelector(sel, aliases)
	}
	return "", ""
}

func packageSelector(sel *ast.SelectorExpr, aliases map[string]string) (string, string) {
	if pkg, ok := sel.X.(*ast.Ident); ok {
		return aliases[pkg.Name], sel.Sel.Name
	}
	return "", ""
}

func checkRawHTML(s *Source, report func(int, string)) error {
	unescaped := func(name string) string {
		return "templ." + name + " puts its argument in the page unescaped, so text a visitor wrote runs as the page's own HTML or script (stored XSS); write { value } and let templ escape it, attributes and URLs included (templ.URL), or document the escaping with an exception"
	}
	if strings.HasSuffix(s.Path, ".templ") {
		_, file, err := goHeader(s)
		if file == nil {
			return err
		}
		names := []string{"templ"}
		for alias, imp := range importAliases(file) {
			if imp == templPkg && alias != "_" && alias != "." {
				names = append(names, regexp.QuoteMeta(alias))
			}
		}
		raw := regexp.MustCompile(`\b(?:` + strings.Join(names, "|") + `)\s*\.\s*(Raw|JSUnsafeFuncCall)\s*\(`)
		offset, body, err := templBody(s)
		if err != nil {
			return err
		}
		for _, m := range raw.FindAllStringSubmatchIndex(body, -1) {
			report(offset+m[0], unescaped(body[m[2]:m[3]]))
		}
		return nil
	}
	return visitCalls(s, func(call *ast.CallExpr, _ []ast.Node, aliases map[string]string, offset int) {
		if pkg, name := packageCall(call, aliases); pkg == templPkg && (name == "Raw" || name == "JSUnsafeFuncCall") {
			report(offset, unescaped(name))
		}
	})
}

func goHeader(s *Source) (*token.FileSet, *ast.File, error) {
	set, file, err := s.GoAST()
	if err != nil && strings.HasSuffix(s.Path, ".templ") {
		return nil, nil, nil
	}
	return set, file, err
}

func templBody(s *Source) (int, string, error) {
	set, file, err := goHeader(s)
	if file == nil {
		return 0, "", err
	}
	end := file.Name.End()
	if len(file.Decls) > 0 {
		end = file.Decls[len(file.Decls)-1].End()
	}
	offset := set.Position(end).Offset
	lines := strings.SplitAfter(s.Text[offset:], "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			lines[i] = strings.Repeat(" ", len(line)-1) + "\n"
		}
	}
	return offset, strings.Join(lines, ""), nil
}

func checkHTTPClient(s *Source, report func(int, string)) error {
	const why = " has no timeout: a server that never answers holds the request, or the job, and its goroutine forever; make an &http.Client{Timeout: ...} of your own and send with it"
	return visitNodes(s, func(n ast.Node, _ []ast.Node, aliases map[string]string, offset func(token.Pos) int) {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return
		}
		pkg, name := packageSelector(sel, aliases)
		if pkg != "net/http" {
			return
		}
		switch name {
		case "DefaultClient":
			report(offset(sel.Pos()), "http.DefaultClient"+why)
		case "Get", "Head", "Post", "PostForm":
			report(offset(sel.Pos()), "http."+name+" uses http.DefaultClient, which"+why)
		}
	})
}

func checkCrossOriginBypass(s *Source, report func(int, string)) error {
	return visitCalls(s, func(call *ast.CallExpr, _ []ast.Node, aliases map[string]string, offset int) {
		if pkg, name := packageCall(call, aliases); pkg == gantryWeb && name == "CrossOrigin" && (len(call.Args) > 2 || call.Ellipsis.IsValid()) {
			report(offset, "")
		}
	})
}

func checkTxDefer(s *Source, report func(int, string)) error {
	return visitNodes(s, func(n ast.Node, _ []ast.Node, _ map[string]string, offset func(token.Pos) int) {
		var body *ast.BlockStmt
		switch f := n.(type) {
		case *ast.FuncDecl:
			body = f.Body
		case *ast.FuncLit:
			body = f.Body
		}
		if body == nil {
			return
		}
		begun := map[string]*ast.CallExpr{}
		committed, deferred := map[string]bool{}, map[string]bool{}
		ast.Inspect(body, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.FuncLit:
				return false
			case *ast.DeferStmt:
				ast.Inspect(n.Call, func(n ast.Node) bool {
					if tx, method := methodCall(n); method == "Rollback" {
						deferred[tx] = true
					}
					return true
				})
				return false
			case *ast.AssignStmt:
				if tx, ok := n.Lhs[0].(*ast.Ident); ok && len(n.Rhs) == 1 {
					if call, ok := n.Rhs[0].(*ast.CallExpr); ok {
						if _, method := methodCall(call); method == "Begin" || method == "BeginTx" {
							begun[tx.Name] = call
						}
					}
				}
			case *ast.CallExpr:
				if tx, method := methodCall(n); method == "Commit" {
					committed[tx] = true
				}
			}
			return true
		})
		for tx, call := range begun {
			if committed[tx] && !deferred[tx] {
				report(offset(call.Pos()), "")
			}
		}
	})
}

func methodCall(n ast.Node) (string, string) {
	if call, ok := n.(*ast.CallExpr); ok {
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			if x, ok := sel.X.(*ast.Ident); ok {
				return x.Name, sel.Sel.Name
			}
			return "", sel.Sel.Name
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

var developmentOnly = []string{"Development", "DevelopmentWithRules", "Timing", "Reload"}

func developmentGuardGuidance() string {
	names := make([]string, len(developmentOnly))
	for i, name := range developmentOnly {
		names[i] = "web." + name
	}
	last := len(names) - 1
	return strings.Join(names[:last], ", ") + " and " + names[last] + " are development-only: call them inside a positive cfg.Development() guard, or document a custom equivalent guard with an exception"
}

func checkDevelopmentGuard(s *Source, report func(int, string)) error {
	return visitCalls(s, func(call *ast.CallExpr, stack []ast.Node, aliases map[string]string, offset int) {
		pkg, name := packageCall(call, aliases)
		if pkg != "github.com/scttymn/gantry/web" || !slices.Contains(developmentOnly, name) {
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
		report(offset, "web."+name+" is development-only: call it inside a positive cfg.Development() guard, or document a custom equivalent guard with an exception")
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
