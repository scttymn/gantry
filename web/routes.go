package web

import (
	"net/http"
	"reflect"
	"runtime"
	"slices"
	"strings"
)

// Route is one of a router's routes, as it was declared (Router.Routes):
// what `gantry routes` lists, and what an app's feature map is checked
// against. Its JSON field names are stable.
type Route struct {
	Method string `json:"method"` // "GET"; "" for any
	// Path is the route's whole path, its scopes' prefixes and its host
	// included: "/admin/posts/{id}", "api.example.com/v1/".
	Path  string `json:"path"`
	Scope string `json:"scope"` // its scope's prefix, "/admin"; "" outside one
	// Filters run before it, in order, named by their functions:
	// "auth.(*Auth).Required"; a closure's name is the compiler's, after the
	// function it's in. A Resources wrap is first.
	Filters     []string `json:"filters"`
	Cached      bool     `json:"cached"`      // served through the router's page cache
	Mounted     bool     `json:"mounted"`     // a plain http.Handler (Mount): files, a mounted package
	Constrained bool     `json:"constrained"` // there only for requests a Constraint passes
}

// String is the route on a line: "GET /admin/deep/{$}  [app.signedIn] cached".
func (r Route) String() string {
	s := r.Path
	if r.Method != "" {
		s = r.Method + " " + s
	}
	var notes []string
	if len(r.Filters) > 0 {
		notes = append(notes, "["+strings.Join(r.Filters, " ")+"]")
	}
	for _, n := range []struct {
		on   bool
		name string
	}{{r.Cached, "cached"}, {r.Mounted, "mounted"}, {r.Constrained, "constrained"}} {
		if n.on {
			notes = append(notes, n.name)
		}
	}
	if len(notes) > 0 {
		s += "  " + strings.Join(notes, " ")
	}
	return s
}

// Routes is every route the router has, in the order they were declared:
// /up first, then the app's. The page for a path no route has isn't one.
func (rt *Router) Routes() []Route {
	routes := slices.Clone(rt.routes)
	for i := range routes {
		routes[i].Filters = slices.Clone(routes[i].Filters)
	}
	return routes
}

// add routes pattern, under the scope's prefix, to h, and lists it (Routes)
// with the scope's filters after r's own.
func (s *Scope) add(pattern string, h http.Handler, r Route) {
	full := s.pattern(pattern)
	s.mux.Handle(full, h)
	if method, path, ok := strings.Cut(full, " "); ok {
		r.Method, r.Path = method, path
	} else {
		r.Path = full
	}
	r.Scope = s.prefix
	r.Filters = append(slices.Clip(r.Filters), funcNames(s.filters)...)
	if r.Filters == nil {
		r.Filters = []string{}
	}
	r.Constrained = s.check != nil
	s.rt.routes = append(s.rt.routes, r)
}

// funcNames are fns' names, their package's path trimmed:
// "auth.(*Auth).Required".
func funcNames[F any](fns []F) []string {
	names := make([]string, 0, len(fns))
	for _, f := range fns {
		name := "?"
		if fn := runtime.FuncForPC(reflect.ValueOf(f).Pointer()); fn != nil {
			name = strings.TrimSuffix(fn.Name(), "-fm") // a method value's
			name = name[strings.LastIndex(name, "/")+1:]
		}
		names = append(names, name)
	}
	return names
}
