package devcheck

import (
	"go/ast"
	"go/token"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The rules here hold an app to its layout (docs/plans/gantry.md, "An app's
// layout"), so they read only what's under app/.

// appPackage is a file's package path under app/: "" for app itself (where
// routes.go is), "posts/comments" for app/posts/comments.
func appPackage(file string) (string, bool) {
	if !strings.HasPrefix(file, "app/") {
		return "", false
	}
	return strings.TrimPrefix(strings.TrimPrefix(path.Dir(file), "app"), "/"), true
}

// layer is where an app package sits in "routes.go → resources → services →
// models, and anything → shared", but for models: app/shared holds page
// shells and resource helpers, which import web and templ. mailers and jobs
// are none of these.
func layer(pkg string) string {
	first, _, _ := strings.Cut(pkg, "/")
	switch {
	case pkg == "":
		return "routes"
	case first == "models", first == "services", first == "shared":
		return first
	case first == "mailers", first == "jobs":
		return "support"
	}
	return "resource"
}

func within(imp, pkg string) bool { return imp == pkg || strings.HasPrefix(imp, pkg+"/") }

// boundary is why app/from mustn't import imp, "" when it may.
func boundary(module, from, imp string) string {
	importer := "app/" + from
	to, internal := "", false
	if module != "" {
		if rest, ok := strings.CutPrefix(imp, module+"/app"); ok && (rest == "" || rest[0] == '/') {
			to, internal = strings.TrimPrefix(rest, "/"), true
		}
	}
	switch layer(from) {
	case "models":
		if imp == "net/http" || within(imp, gantryWeb) || within(imp, templPkg) {
			return importer + " imports " + imp + ": models are tables, their queries and their rules, used and tested with no request or page; do the HTTP or drawing in the controller that calls the model"
		}
		if internal && layer(to) != "models" {
			return importer + " imports " + imp + ": models import no other app package, so every package can use them without an import cycle; put what needs both in a service (app/services/<name>) that imports models"
		}
	case "services":
		if within(imp, gantryWeb) {
			return importer + " imports " + imp + ": a service is work behind functions the controllers call, not pages; take values as arguments and return results, and keep requests and responses in the controller"
		}
		if internal && layer(to) == "resource" {
			return importer + " imports the resource " + imp + ": services sit under resources (routes.go → resources → services → models); move what both need into the service or app/models"
		}
	case "shared":
		if internal && layer(to) == "resource" {
			return importer + " imports the resource " + imp + ": every resource may use app/shared, so shared code can't depend on one; pass the values in as arguments, or move the part into app/shared"
		}
	case "resource":
		if internal && layer(to) == "resource" && strings.HasPrefix(from, to+"/") {
			return importer + " imports its parent " + imp + ": the parent imports a nested resource to draw its part, so a nested one loads its parent through models (models.New(c.DB.Read).Get...) instead"
		}
	}
	if internal && to == "" && from != "" {
		return importer + " imports the routes package " + imp + ": routes.go builds every controller, so a package it builds can't import it; give the controller what it needs as fields, set in routes.go"
	}
	return ""
}

func checkImportBoundary(s *Source, report func(int, string)) error {
	from, ok := appPackage(s.Path)
	if !ok {
		return nil
	}
	set, file, err := goHeader(s)
	if file == nil {
		return err
	}
	for _, spec := range file.Imports {
		imp, _ := strconv.Unquote(spec.Path.Value)
		if why := boundary(s.Module, from, imp); why != "" {
			report(set.Position(spec.Path.Pos()).Offset, why)
		}
	}
	return nil
}

// checkViewDB finds a view (a .templ file) that holds the database: the
// app's models.New or Queries, gantry's db.DB, Tx or Querier, or a DB's Read
// or Write pool.
func checkViewDB(s *Source, report func(int, string)) error {
	if _, ok := appPackage(s.Path); !ok {
		return nil
	}
	_, file, err := goHeader(s)
	if file == nil {
		return err
	}
	patterns := []string{`\bDB\s*\.\s*(?:Read|Write)\b`}
	for alias, imp := range importAliases(file) {
		if alias == "_" || alias == "." {
			continue
		}
		switch {
		case s.Module != "" && imp == s.Module+"/app/models":
			patterns = append(patterns, `\b`+regexp.QuoteMeta(alias)+`\s*\.\s*(?:New|Queries)\b`)
		case imp == gantryDB:
			patterns = append(patterns, `\b`+regexp.QuoteMeta(alias)+`\s*\.\s*(?:DB|Tx|Querier)\b`)
		}
	}
	offset, body, err := templBody(s)
	if err != nil {
		return err
	}
	for _, m := range regexp.MustCompile(strings.Join(patterns, "|")).FindAllStringIndex(body, -1) {
		report(offset+m[0], "views draw plain values: this one uses "+strings.Join(strings.Fields(body[m[0]:m[1]]), "")+". Load the data in the controller's action (Controller.Index) and pass it to the component, so the page can be cached and tested without a database")
	}
	return nil
}

func checkEnvOutsideConfig(s *Source, report func(int, string)) error {
	if _, ok := appPackage(s.Path); !ok {
		return nil
	}
	return visitCalls(s, func(call *ast.CallExpr, _ []ast.Node, aliases map[string]string, offset int) {
		pkg, name := packageCall(call, aliases)
		if (pkg == "os" && (name == "Getenv" || name == "LookupEnv" || name == "Environ")) || (pkg == "syscall" && name == "Getenv") {
			report(offset, path.Base(pkg)+"."+name+" in app code: the environment is read once, in config (config.Load), so it's checked at start and a test sets it as a value; add a field to config.Config and give it to what needs it")
		}
	})
}

func checkBackgroundWork(s *Source, report func(int, string)) error {
	if _, ok := appPackage(s.Path); !ok {
		return nil
	}
	const lost = " a deploy or a crash loses it, and nothing tries it again"
	return visitNodes(s, func(n ast.Node, _ []ast.Node, aliases map[string]string, offset func(token.Pos) int) {
		switch n := n.(type) {
		case *ast.GoStmt:
			report(offset(n.Pos()), "a go statement in app code outlives the request:"+lost+". Enqueue a job (a.Jobs) for work after the response, broadcast with live to update pages, or use sync.WaitGroup.Go for work the request waits for")
		case *ast.CallExpr:
			switch pkg, name := packageCall(n, aliases); {
			case pkg == "time" && name == "Sleep":
				report(offset(n.Pos()), "time.Sleep in app code holds a request, or a job's worker, doing nothing; to do something later, EnqueueIn or EnqueueAt a job")
			case pkg == "time" && name == "AfterFunc":
				report(offset(n.Pos()), "time.AfterFunc runs its function in a goroutine of its own:"+lost+". EnqueueIn or EnqueueAt a job instead")
			}
		}
	})
}

// rawSQL are the methods that run SQL on a *sql.DB or a *sql.Tx.
var rawSQL = []string{"Exec", "ExecContext", "Query", "QueryContext", "QueryRow", "QueryRowContext", "Prepare", "PrepareContext", "Begin", "BeginTx"}

func checkSQLOutsideModels(s *Source, report func(int, string)) error {
	pkg, ok := appPackage(s.Path)
	if !ok || layer(pkg) == "models" {
		return nil
	}
	const sqlc = "write the query in app/models/<table>.sql and call what sqlc makes of it, models.New(c.DB.Read).Name(ctx) (models.New(tx) in c.DB.Tx), so its columns are checked against the schema"
	return visitNodes(s, func(n ast.Node, stack []ast.Node, aliases map[string]string, offset func(token.Pos) int) {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if pkg, name := packageSelector(sel, aliases); pkg == "database/sql" && slices.Contains([]string{"Open", "OpenDB", "DB", "Conn", "Tx", "Stmt"}, name) {
				report(offset(sel.Pos()), "sql."+name+" outside app/models: the app's database is a.DB, gantry's db.DB, whose pools (Read, Write) and transactions (Tx) the models take; "+sqlc)
			}
			return
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !slices.Contains(rawSQL, sel.Sel.Name) {
			return
		}
		switch x := sel.X.(type) {
		case *ast.SelectorExpr:
			if x.Sel.Name == "Read" || x.Sel.Name == "Write" {
				report(offset(call.Pos()), "raw SQL on the "+x.Sel.Name+" pool outside app/models: "+sqlc)
			}
		case *ast.Ident:
			if isGantryTx(x.Name, stack, aliases) {
				report(offset(call.Pos()), "raw SQL on a transaction outside app/models: "+sqlc)
			}
		}
	})
}

func isGantryTx(name string, stack []ast.Node, aliases map[string]string) bool {
	for i := len(stack) - 1; i >= 0; i-- {
		var params *ast.FieldList
		switch f := stack[i].(type) {
		case *ast.FuncLit:
			params = f.Type.Params
		case *ast.FuncDecl:
			params = f.Type.Params
		default:
			continue
		}
		for _, field := range params.List {
			typ := field.Type
			if star, ok := typ.(*ast.StarExpr); ok {
				typ = star.X
			}
			sel, ok := typ.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			if pkg, t := packageSelector(sel, aliases); pkg != gantryDB || (t != "Tx" && t != "Querier") {
				continue
			}
			for _, n := range field.Names {
				if n.Name == name {
					return true
				}
			}
		}
	}
	return false
}

// pools tells which of a DB's pools a query runs on: models.New(c.DB.Read)
// or a name given it in the function.
type pools struct {
	models string
	named  map[string]string
}

func (p pools) of(e ast.Expr) string {
	if id, ok := e.(*ast.Ident); ok {
		return p.named[id.Name]
	}
	call, ok := e.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return ""
	}
	fn, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || fn.Sel.Name != "New" {
		return ""
	}
	if pkg, ok := fn.X.(*ast.Ident); !ok || pkg.Name != p.models {
		return ""
	}
	if arg, ok := call.Args[0].(*ast.SelectorExpr); ok && (arg.Sel.Name == "Read" || arg.Sel.Name == "Write") {
		return arg.Sel.Name
	}
	return ""
}

func (p pools) name(lhs []ast.Expr, rhs []ast.Expr) {
	if len(lhs) != len(rhs) {
		return
	}
	for i, l := range lhs {
		if id, ok := l.(*ast.Ident); ok {
			if pool := p.of(rhs[i]); pool != "" {
				p.named[id.Name] = pool
			} else {
				delete(p.named, id.Name)
			}
		}
	}
}

// txClosure is the function a db.Tx call runs, x.Tx(ctx, fn), nil for
// anything else.
func txClosure(n ast.Node) *ast.FuncLit {
	call, ok := n.(*ast.CallExpr)
	if !ok || len(call.Args) != 2 {
		return nil
	}
	if sel, ok := call.Fun.(*ast.SelectorExpr); !ok || sel.Sel.Name != "Tx" {
		return nil
	}
	fn, _ := call.Args[1].(*ast.FuncLit)
	return fn
}

// visitFunctions calls visit once for each function, with a walk of its own
// nodes: what a Tx closure in it does is the function's, and any other
// function literal (a handler routed inline) is a function of its own.
func visitFunctions(s *Source, visit func(walk func(func(ast.Node)), aliases map[string]string, offset func(token.Pos) int)) error {
	return visitNodes(s, func(n ast.Node, stack []ast.Node, aliases map[string]string, offset func(token.Pos) int) {
		var body *ast.BlockStmt
		switch fn := n.(type) {
		case *ast.FuncDecl:
			body = fn.Body
		case *ast.FuncLit:
			if txClosure(stack[len(stack)-2]) != fn {
				body = fn.Body
			}
		}
		if body == nil {
			return
		}
		visit(func(f func(ast.Node)) {
			own := map[*ast.FuncLit]bool{}
			ast.Inspect(body, func(n ast.Node) bool {
				if fn := txClosure(n); fn != nil {
					own[fn] = true
				}
				if fn, ok := n.(*ast.FuncLit); ok && !own[fn] {
					return false
				}
				if n != nil {
					f(n)
				}
				return true
			})
		}, aliases, offset)
	})
}

func appModels(s *Source, aliases map[string]string) string {
	for alias, imp := range aliases {
		if s.Module != "" && imp == s.Module+"/app/models" {
			return alias
		}
	}
	return ""
}

func checkReadThenWrite(s *Source, report func(int, string)) error {
	if _, ok := appPackage(s.Path); !ok {
		return nil
	}
	return visitFunctions(s, func(walk func(func(ast.Node)), aliases map[string]string, offset func(token.Pos) int) {
		p := pools{models: appModels(s, aliases), named: map[string]string{}}
		if p.models == "" {
			return
		}
		read := ""
		walk(func(n ast.Node) {
			switch n := n.(type) {
			case *ast.AssignStmt:
				p.name(n.Lhs, n.Rhs)
			case *ast.ValueSpec:
				names := make([]ast.Expr, len(n.Names))
				for i, id := range n.Names {
					names[i] = id
				}
				p.name(names, n.Values)
			case *ast.CallExpr:
				sel, ok := n.Fun.(*ast.SelectorExpr)
				if !ok {
					return
				}
				switch method := sel.Sel.Name; p.of(sel.X) {
				case "Read":
					if strings.HasPrefix(method, "Get") && read == "" {
						read = method
					}
				case "Write":
					if read != "" && (strings.HasPrefix(method, "Update") || strings.HasPrefix(method, "Delete")) {
						report(offset(n.Pos()), method+" writes on the Write pool what "+read+" read on the Read pool: an edit saved in between is overwritten (a lost update), and a read replica may lag. Read and write in one c.DB.Tx, with models.New(tx) and a Get...ForUpdate query")
					}
				}
			}
		})
	})
}

// getActions are the actions Resources routes GET to.
var getActions = []string{"Index", "Show", "New", "Edit"}

func checkGetMutates(s *Source, report func(int, string)) error {
	if _, ok := appPackage(s.Path); !ok {
		return nil
	}
	return visitNodes(s, func(n ast.Node, stack []ast.Node, aliases map[string]string, offset func(token.Pos) int) {
		var route string
		var body *ast.BlockStmt
		switch n := n.(type) {
		case *ast.FuncDecl:
			if n.Recv != nil && n.Body != nil && slices.Contains(getActions, n.Name.Name) && isHandler(n.Type, aliases) {
				route, body = n.Name.Name+" (Resources routes GET to Index, Show, New and Edit)", n.Body
			}
		case *ast.FuncLit:
			if pattern, ok := getRoute(n, stack); ok {
				route, body = pattern, n.Body
			}
		}
		if body == nil {
			return
		}
		p := pools{models: appModels(s, aliases), named: map[string]string{}}
		var write ast.Node
		ast.Inspect(body, func(n ast.Node) bool {
			if write != nil {
				return false
			}
			switch n := n.(type) {
			case *ast.AssignStmt:
				p.name(n.Lhs, n.Rhs)
			case *ast.CallExpr:
				if p.models != "" && p.of(n) == "Write" {
					write = n
				}
				if sel, ok := n.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Tx" && len(n.Args) == 2 {
					if _, ok := n.Args[1].(*ast.FuncLit); ok {
						write = n
					}
				}
			}
			return true
		})
		if write != nil {
			report(offset(write.Pos()), route+" writes to the database: a GET must be safe to repeat, and browsers prefetch links and crawlers follow them, so a link could change data or sign someone out. Route it as POST (PATCH or DELETE for a resource's Update and Delete) and send it from a button in a form")
		}
	})
}

func isHandler(fn *ast.FuncType, aliases map[string]string) bool {
	var types []ast.Expr
	for _, f := range fn.Params.List {
		for range max(1, len(f.Names)) {
			types = append(types, f.Type)
		}
	}
	if len(types) != 2 {
		return false
	}
	w, ok := types[0].(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, name := packageSelector(w, aliases)
	return pkg == "net/http" && name == "ResponseWriter"
}

// getRoute is the pattern a function is routed to, as Handle or Cached's
// handler, when that's a GET: its method GET or HEAD, or none.
func getRoute(fn *ast.FuncLit, stack []ast.Node) (string, bool) {
	if len(stack) < 2 {
		return "", false
	}
	call, ok := stack[len(stack)-2].(*ast.CallExpr)
	if !ok || len(call.Args) != 2 || call.Args[1] != fn {
		return "", false
	}
	if sel, ok := call.Fun.(*ast.SelectorExpr); !ok || (sel.Sel.Name != "Handle" && sel.Sel.Name != "Cached") {
		return "", false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	pattern, _ := strconv.Unquote(lit.Value)
	method, _, ok := strings.Cut(pattern, " ")
	if ok && method != "GET" && method != "HEAD" {
		return "", false
	}
	if !ok {
		return pattern + " (no method, so GET too)", true
	}
	return pattern, true
}

func checkInlineDownload(s *Source, report func(int, string)) error {
	if _, ok := appPackage(s.Path); !ok {
		return nil
	}
	return visitFunctions(s, func(walk func(func(ast.Node)), aliases map[string]string, offset func(token.Pos) int) {
		var serves []*ast.CallExpr
		disposition := false
		walk(func(n ast.Node) {
			switch n := n.(type) {
			case *ast.BasicLit:
				if n.Kind == token.STRING && strings.Contains(strings.ToLower(n.Value), "content-disposition") {
					disposition = true
				}
			case *ast.CallExpr:
				if pkg, name := packageCall(n, aliases); pkg == "net/http" && (name == "ServeContent" || name == "ServeFile" || name == "ServeFileFS") {
					serves = append(serves, n)
				}
			}
		})
		if disposition {
			return
		}
		for _, call := range serves {
			report(offset(call.Pos()), "")
		}
	})
}
