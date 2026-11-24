package devcheck

import (
	"go/ast"
	"go/token"
	"slices"
	"strconv"
	"strings"
)

const gantryAuth = "github.com/scttymn/gantry/auth"

var visitorReads = []ref{
	{gantryAuth, "", "Current"},
	{gantryAuth, "Auth", "SignedIn"},
	{gantryAuth, "Auth", "HasSessionCookie"},
	{gantryWeb, "", "SignedCookie"},
	{gantryWeb, "Flash", "Take"},
	{"net/http", "Request", "Cookie"},
	{"net/http", "Request", "Cookies"},
	{"net/http", "Request", "CookiesNamed"},
}

var cookieWrites = []ref{
	{"net/http", "", "SetCookie"},
	{gantryWeb, "", "SetCookie"},
	{gantryWeb, "", "ClearCookie"},
	{gantryWeb, "Flash", "Set"},
	{gantryWeb, "Flash", "Redirect"},
	{gantryWeb, "Flash", "Take"},
	{gantryAuth, "Auth", "StartSession"},
}

type cachedRoute struct {
	pos      token.Pos
	route    string
	handlers []fn
}

func (p *program) cachedRoutes(f *goFile) []cachedRoute {
	var routes []cachedRoute
	for _, d := range f.file.Decls {
		top, ok := d.(*ast.FuncDecl)
		if !ok || top.Body == nil {
			continue
		}
		isWeb := func(e ast.Expr, types ...string) bool {
			t := p.typeOf(f, top, e, 0)
			return t.pkg == gantryWeb && slices.Contains(types, t.name)
		}
		ast.Inspect(top.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			r := cachedRoute{pos: call.Pos()}
			switch {
			case sel.Sel.Name == "Cached" && len(call.Args) == 2 && isWeb(sel.X, "Router", "Scope"):
				r.route = quoted(call.Args[0], "the route") + " is Cached"
				r.handlers = readable(p.callee(f, top, call.Args[1], 0).fn)
			case sel.Sel.Name == "Handler" && len(call.Args) == 1 && isWeb(sel.X, "PageCache"):
				r.route = "PageCache.Handler caches what it wraps"
				r.handlers = p.served(f, top, call.Args[0], isWeb)
			case sel.Sel.Name == "Resources" && len(call.Args) == 3 && isWeb(sel.X, "Router", "Scope"):
				wrap, ok := call.Args[2].(*ast.SelectorExpr)
				if !ok || wrap.Sel.Name != "Handler" || !isWeb(wrap.X, "PageCache") {
					return true
				}
				r.route = "Resources(" + strconv.Quote(quoted(call.Args[0], "...")) + ") puts its pages behind the page cache"
				controller := p.typeOf(f, top, call.Args[1], 0)
				for _, action := range getActions {
					m, _ := p.method(controller, action, 0)
					r.handlers = append(r.handlers, readable(m)...)
				}
			default:
				return true
			}
			routes = append(routes, r)
			return true
		})
	}
	return routes
}

func (p *program) served(f *goFile, top *ast.FuncDecl, e ast.Expr, isWeb func(ast.Expr, ...string) bool) []fn {
	if call, ok := e.(*ast.CallExpr); ok && len(call.Args) == 1 {
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			if (imported(f, top, sel.X) == "net/http" && sel.Sel.Name == "HandlerFunc") || (sel.Sel.Name == "Wrap" && isWeb(sel.X, "Router")) {
				return readable(p.callee(f, top, call.Args[0], 0).fn)
			}
		}
	}
	m, _ := p.method(p.typeOf(f, top, e, 0), "ServeHTTP", 0)
	return readable(m)
}

func readable(f fn) []fn {
	if f.file == nil {
		return nil
	}
	return []fn{f}
}

func quoted(e ast.Expr, otherwise string) string {
	if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
		if s, err := strconv.Unquote(lit.Value); err == nil {
			return s
		}
	}
	return otherwise
}

func setsCookieHeader(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || (sel.Sel.Name != "Set" && sel.Sel.Name != "Add") || len(call.Args) != 2 {
		return false
	}
	return strings.EqualFold(quoted(call.Args[0], ""), "Set-Cookie")
}

func checkCached(s *Source, report func(int, string), is func(callee, *ast.CallExpr) string, message func(route, way string) string) error {
	if _, ok := appPackage(s.Path); !ok || s.prog == nil {
		return nil
	}
	f := s.prog.file(s.Path)
	if f == nil {
		return nil
	}
	for _, route := range s.prog.cachedRoutes(f) {
		for _, h := range route.handlers {
			if r, ok := s.prog.reach(h, is, map[*ast.BlockStmt]bool{}); ok {
				way := make([]string, len(r.via))
				for i, f := range r.via {
					way[i] = f.String()
				}
				report(f.set.Position(route.pos).Offset, message(route.route, strings.Join(way, " → ")+" → "+r.what+" at "+r.at))
				break
			}
		}
	}
	return nil
}

func checkCachedScope(s *Source, report func(int, string)) error {
	return checkCached(s, report, func(c callee, _ *ast.CallExpr) string {
		if slices.Contains(visitorReads, c.ref) {
			return c.ref.String()
		}
		return ""
	}, func(route, way string) string {
		return route + ", so every visitor gets the copy one visitor's request drew, and its handler reads who's visiting: " + way + ". Draw who's signed in, or what a cookie says, in a part of the page that isn't cached (a Turbo frame from a route added with Handle), or add this route with Handle"
	})
}

func checkCacheSetCookie(s *Source, report func(int, string)) error {
	return checkCached(s, report, func(c callee, call *ast.CallExpr) string {
		switch {
		case slices.Contains(cookieWrites, c.ref):
			return c.ref.String()
		case setsCookieHeader(call):
			return "a Set-Cookie header"
		}
		return ""
	}, func(route, way string) string {
		return route + ", and its handler sets a cookie: " + way + ". The cache keeps no page that sets one, and serves a kept page without running the handler, so most visitors never get it. Set it on a route added with Handle (the form's POST, say), not on a cached page"
	})
}
