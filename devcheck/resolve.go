package devcheck

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// The cache rules follow a route to its handler, and the handler to what it
// calls, across the app's files and packages. Strict reads source without
// building it, so this follows syntax: a name is followed when where it's
// declared says what it is (a parameter's type, x := T{...}, a struct's
// field, a function's one result), and passed over otherwise, never guessed.

// program is the Go code a scan can follow names into: the packages under
// its root, each parsed when a rule first needs it and kept for the scan.
type program struct {
	root, module string
	set          *token.FileSet
	pkgs         map[string]*goPackage // by import path; nil: not under the root
}

func newProgram(root, module string) *program {
	return &program{root: root, module: module, set: token.NewFileSet(), pkgs: map[string]*goPackage{}}
}

type goPackage struct {
	path    string // import path
	files   []*goFile
	funcs   map[string]fn
	types   map[string]typeDecl
	methods map[string]map[string]fn // by receiver type, then name
}

type goFile struct {
	set     *token.FileSet
	path    string // project-relative
	file    *ast.File
	pkg     *goPackage
	aliases map[string]string
}

type typeDecl struct {
	file *goFile
	spec *ast.TypeSpec
}

// fn is a function the program can read: a declaration, or a literal in one.
type fn struct {
	file *goFile
	top  *ast.FuncDecl // the declaration, or the one the literal is in (nil at package level)
	lit  *ast.FuncLit
}

func (f fn) body() *ast.BlockStmt {
	if f.lit != nil {
		return f.lit.Body
	}
	return f.top.Body
}

// String is the function as the messages name it: home.Controller.Show, or
// where a literal is.
func (f fn) String() string {
	if f.lit != nil {
		return "the function at " + f.file.at(f.lit.Pos())
	}
	name := f.file.file.Name.Name + "."
	if recv := receiver(f.top); recv != "" {
		name += recv + "."
	}
	return name + f.top.Name.Name
}

// named is a named type: its package's import path and its name. The zero
// value is a type the program doesn't know.
type named struct{ pkg, name string }

// ref is a function, method or field by its package's import path, its
// type's name (none for a package's own function) and its name.
type ref struct{ pkg, recv, name string }

func (r ref) String() string {
	if r.recv == "" {
		return path.Base(r.pkg) + "." + r.name
	}
	return path.Base(r.pkg) + "." + r.recv + "." + r.name
}

// outside are the results and fields of gantry's packages that the program
// follows and a scan of an app doesn't read: the router NewRouter makes, and
// the page cache it holds.
var outside = map[ref]named{
	{gantryWeb, "", "NewRouter"}:   {gantryWeb, "Router"},
	{gantryWeb, "Router", "Cache"}: {gantryWeb, "PageCache"},
}

func (p *program) dir(importPath string) (string, bool) {
	if p.module == "" {
		return strings.CutPrefix(importPath, "./")
	}
	if importPath == p.module {
		return ".", true
	}
	return strings.CutPrefix(importPath, p.module+"/")
}

func (p *program) importPath(dir string) string {
	switch {
	case p.module == "":
		return "./" + dir
	case dir == ".":
		return p.module
	}
	return p.module + "/" + dir
}

// pkg is the package at importPath, nil when it isn't under the root. Its
// tests and templ's generated files aren't read, as the scan doesn't read
// them, nor a file that doesn't parse, which its own scan reports.
func (p *program) pkg(importPath string) *goPackage {
	if pkg, ok := p.pkgs[importPath]; ok {
		return pkg
	}
	p.pkgs[importPath] = nil
	dir, ok := p.dir(importPath)
	if !ok {
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(p.root, dir))
	if err != nil {
		return nil
	}
	pkg := &goPackage{path: importPath, funcs: map[string]fn{}, types: map[string]typeDecl{}, methods: map[string]map[string]fn{}}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, "_templ.go") {
			continue
		}
		rel := path.Join(filepath.ToSlash(dir), name)
		data, err := os.ReadFile(filepath.Join(p.root, rel))
		if err != nil {
			continue
		}
		file, err := parser.ParseFile(p.set, rel, data, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		f := &goFile{set: p.set, path: rel, file: file, pkg: pkg, aliases: importAliases(file)}
		pkg.files = append(pkg.files, f)
		for _, d := range file.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Body == nil {
					continue
				}
				if d.Recv == nil {
					pkg.funcs[d.Name.Name] = fn{file: f, top: d}
					continue
				}
				if recv := receiver(d); recv != "" {
					if pkg.methods[recv] == nil {
						pkg.methods[recv] = map[string]fn{}
					}
					pkg.methods[recv][d.Name.Name] = fn{file: f, top: d}
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					if t, ok := spec.(*ast.TypeSpec); ok && t.TypeParams == nil {
						pkg.types[t.Name.Name] = typeDecl{file: f, spec: t}
					}
				}
			}
		}
	}
	p.pkgs[importPath] = pkg
	return pkg
}

// receiver is the name of the type a method is declared on, its package's
// own by Go's rules, so it needs no lookup: the type may be declared after
// the method, or in another file.
func receiver(d *ast.FuncDecl) string {
	if d.Recv == nil || len(d.Recv.List) != 1 {
		return ""
	}
	t := d.Recv.List[0].Type
	for {
		switch e := t.(type) {
		case *ast.StarExpr:
			t = e.X
		case *ast.ParenExpr:
			t = e.X
		case *ast.Ident:
			return e.Name
		default:
			return "" // a generic type's, T[K]: not followed
		}
	}
}

// file is the scanned file at a project-relative path, as the program read it.
func (p *program) file(rel string) *goFile {
	pkg := p.pkg(p.importPath(path.Dir(rel)))
	if pkg == nil {
		return nil
	}
	for _, f := range pkg.files {
		if f.path == rel {
			return f
		}
	}
	return nil
}

func (f *goFile) at(pos token.Pos) string {
	return f.path + ":" + strconv.Itoa(f.set.Position(pos).Line)
}

// typeName is the named type a type expression is, pointers dropped: T
// declared in the file's package, or pkg.T.
func (f *goFile) typeName(e ast.Expr) named {
	switch e := e.(type) {
	case *ast.StarExpr:
		return f.typeName(e.X)
	case *ast.ParenExpr:
		return f.typeName(e.X)
	case *ast.Ident:
		if _, ok := f.pkg.types[e.Name]; ok {
			return named{f.pkg.path, e.Name}
		}
	case *ast.SelectorExpr:
		if x, ok := e.X.(*ast.Ident); ok && f.aliases[x.Name] != "" {
			return named{f.aliases[x.Name], e.Sel.Name}
		}
	}
	return named{}
}

// declared is what name is at pos in top, the last declaration of it before
// pos in a scope that holds pos: its declared type, or the value it was
// given (neither for one the program can't follow, as a range variable).
func declared(top *ast.FuncDecl, name string, pos token.Pos) (typ, value ast.Expr, found bool) {
	if top == nil {
		return nil, nil, false
	}
	fields := func(lists ...*ast.FieldList) {
		for _, list := range lists {
			if list == nil {
				continue
			}
			for _, f := range list.List {
				for _, n := range f.Names {
					if n.Name == name {
						typ, value, found = f.Type, nil, true
					}
				}
			}
		}
	}
	fields(top.Recv, top.Type.Params, top.Type.Results)
	if top.Body == nil {
		return
	}
	ast.Inspect(top.Body, func(n ast.Node) bool {
		if n == nil || n.Pos() >= pos {
			return false
		}
		switch n := n.(type) {
		case *ast.FuncLit:
			if pos >= n.End() {
				return false
			}
			fields(n.Type.Params, n.Type.Results)
		case *ast.BlockStmt, *ast.IfStmt, *ast.ForStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt, *ast.CaseClause, *ast.CommClause:
			if pos >= n.End() {
				return false
			}
		// A declaration's names are declared after it: in x := x, or in a
		// range's expression, x is still the one before. Inside it (a
		// function literal on its right), keep looking.
		case *ast.RangeStmt:
			if pos >= n.End() {
				return false
			}
			if pos < n.Body.Pos() {
				break
			}
			for _, e := range []ast.Expr{n.Key, n.Value} {
				if id, ok := e.(*ast.Ident); ok && n.Tok == token.DEFINE && id.Name == name {
					typ, value, found = nil, nil, true
				}
			}
		case *ast.AssignStmt:
			if n.Tok != token.DEFINE || pos < n.End() {
				break
			}
			for i, l := range n.Lhs {
				if id, ok := l.(*ast.Ident); ok && id.Name == name {
					typ, value, found = nil, nil, true
					if len(n.Lhs) == len(n.Rhs) {
						value = n.Rhs[i]
					}
				}
			}
		case *ast.ValueSpec:
			if pos < n.End() {
				break
			}
			for i, id := range n.Names {
				if id.Name == name {
					typ, value, found = n.Type, nil, true
					if typ == nil && len(n.Names) == len(n.Values) {
						value = n.Values[i]
					}
				}
			}
		}
		return true
	})
	return
}

// imported is the import path a name in f means at pos, "" when it's not an
// import's (or a declaration in the function hides the import).
func imported(f *goFile, top *ast.FuncDecl, e ast.Expr) string {
	id, ok := e.(*ast.Ident)
	if !ok {
		return ""
	}
	if _, _, local := declared(top, id.Name, id.Pos()); local {
		return ""
	}
	return f.aliases[id.Name]
}

// maxDepth bounds how far one name is followed, so a declaration that
// refers to itself can't loop.
const maxDepth = 16

// typeOf is the named type of e, in f's function top, an alias's the type it
// stands for.
func (p *program) typeOf(f *goFile, top *ast.FuncDecl, e ast.Expr, depth int) named {
	return p.unalias(p.exprType(f, top, e, depth), depth)
}

// unalias is the type an alias stands for, through aliases of aliases (g
// auth's type Auth = gantryauth.Auth is gantry's Auth), and t itself when
// it isn't one.
func (p *program) unalias(t named, depth int) named {
	for ; depth <= maxDepth; depth++ {
		pkg := p.pkg(t.pkg)
		if pkg == nil {
			return t
		}
		decl, ok := pkg.types[t.name]
		if !ok || !decl.spec.Assign.IsValid() {
			return t
		}
		t = decl.file.typeName(decl.spec.Type)
	}
	return named{}
}

func (p *program) exprType(f *goFile, top *ast.FuncDecl, e ast.Expr, depth int) named {
	if depth > maxDepth {
		return named{}
	}
	switch e := e.(type) {
	case *ast.ParenExpr:
		return p.typeOf(f, top, e.X, depth+1)
	case *ast.StarExpr:
		return p.typeOf(f, top, e.X, depth+1)
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			return p.typeOf(f, top, e.X, depth+1)
		}
	case *ast.CompositeLit:
		return f.typeName(e.Type)
	case *ast.Ident:
		typ, value, found := declared(top, e.Name, e.Pos())
		switch {
		case typ != nil:
			return f.typeName(typ)
		case value != nil:
			return p.typeOf(f, top, value, depth+1)
		case !found:
			return named{} // a package's variable: not followed
		}
	case *ast.SelectorExpr:
		if imported(f, top, e.X) != "" {
			return named{} // another package's variable
		}
		return p.field(p.typeOf(f, top, e.X, depth+1), e.Sel.Name, depth+1)
	case *ast.CallExpr:
		c := p.callee(f, top, e.Fun, depth+1)
		if c.fn.top != nil && c.fn.lit == nil {
			if results := c.fn.top.Type.Results; results != nil && len(results.List) == 1 && len(results.List[0].Names) <= 1 {
				return c.fn.file.typeName(results.List[0].Type)
			}
			return named{}
		}
		return outside[c.ref]
	}
	return named{}
}

// field is the type of t's field name, its own or one it embeds.
func (p *program) field(t named, name string, depth int) named {
	if t = p.unalias(t, depth); t.name == "" || depth > maxDepth {
		return named{}
	}
	pkg := p.pkg(t.pkg)
	if pkg == nil {
		return outside[ref{t.pkg, t.name, name}]
	}
	decl, ok := pkg.types[t.name]
	if !ok {
		return named{}
	}
	st, ok := decl.spec.Type.(*ast.StructType)
	if !ok {
		return named{}
	}
	var embedded []named
	for _, f := range st.Fields.List {
		ft := decl.file.typeName(f.Type)
		if len(f.Names) == 0 {
			if ft.name == name {
				return ft
			}
			embedded = append(embedded, ft)
		}
		for _, n := range f.Names {
			if n.Name == name {
				return ft
			}
		}
	}
	for _, e := range embedded {
		if ft := p.field(e, name, depth+1); ft.name != "" {
			return ft
		}
	}
	return named{}
}

// method is t's method name, its own or one a type it embeds has: the code,
// when the program reads t's package, and what it is either way.
func (p *program) method(t named, name string, depth int) (fn, ref) {
	if t = p.unalias(t, depth); t.name == "" || depth > maxDepth {
		return fn{}, ref{}
	}
	pkg := p.pkg(t.pkg)
	if pkg == nil {
		return fn{}, ref{t.pkg, t.name, name}
	}
	if m, ok := pkg.methods[t.name][name]; ok {
		return m, ref{t.pkg, t.name, name}
	}
	if decl, ok := pkg.types[t.name]; ok {
		if st, ok := decl.spec.Type.(*ast.StructType); ok {
			for _, f := range st.Fields.List {
				if len(f.Names) > 0 {
					continue
				}
				if m, r := p.method(decl.file.typeName(f.Type), name, depth+1); r.name != "" {
					return m, r
				}
			}
		}
	}
	return fn{}, ref{}
}

// callee is what a call to e runs, in f's function top: its code when the
// program can read it, and what it is.
type callee struct {
	fn  fn
	ref ref
}

func (p *program) callee(f *goFile, top *ast.FuncDecl, e ast.Expr, depth int) callee {
	if depth > maxDepth {
		return callee{}
	}
	switch e := e.(type) {
	case *ast.ParenExpr:
		return p.callee(f, top, e.X, depth+1)
	case *ast.FuncLit:
		return callee{fn: fn{file: f, top: top, lit: e}}
	case *ast.Ident:
		_, value, found := declared(top, e.Name, e.Pos())
		if lit, ok := value.(*ast.FuncLit); ok {
			return callee{fn: fn{file: f, top: top, lit: lit}}
		}
		if found {
			return callee{}
		}
		return callee{fn: f.pkg.funcs[e.Name], ref: ref{f.pkg.path, "", e.Name}}
	case *ast.SelectorExpr:
		if imp := imported(f, top, e.X); imp != "" {
			c := callee{ref: ref{imp, "", e.Sel.Name}}
			if pkg := p.pkg(imp); pkg != nil {
				c.fn = pkg.funcs[e.Sel.Name]
			}
			return c
		}
		m, r := p.method(p.typeOf(f, top, e.X, depth+1), e.Sel.Name, depth+1)
		return callee{fn: m, ref: r}
	}
	return callee{}
}

// reached is a call a function makes, itself or through the functions it
// calls: where it is, and the functions on the way, the first one first.
type reached struct {
	what, at string
	via      []fn
}

// reach is the first call in f, or in a function it calls that the program
// can read, that is names. A function already on the way isn't read again.
func (p *program) reach(f fn, is func(callee, *ast.CallExpr) string, seen map[*ast.BlockStmt]bool) (reached, bool) {
	body := f.body()
	if body == nil || seen[body] {
		return reached{}, false
	}
	seen[body] = true
	var found reached
	ok := false
	ast.Inspect(body, func(n ast.Node) bool {
		if ok {
			return false
		}
		call, isCall := n.(*ast.CallExpr)
		if !isCall {
			return true
		}
		c := p.callee(f.file, f.top, call.Fun, 0)
		if what := is(c, call); what != "" {
			found, ok = reached{what: what, at: f.file.at(call.Pos())}, true
			return false
		}
		if c.fn.file != nil {
			if r, hit := p.reach(c.fn, is, seen); hit {
				found, ok = r, true
				return false
			}
		}
		return true
	})
	if ok {
		found.via = append([]fn{f}, found.via...)
	}
	return found, ok
}
