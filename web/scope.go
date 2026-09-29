package web

import (
	"net/http"
	"slices"
	"strings"
)

// Filter is a Handler run in a pipeline, before the route's handler: a
// self-contained step that takes the request from the filter before it,
// checks it or adds to it, and passes it on (the pipes-and-filters
// pattern; Rails' before_action, Phoenix's plugs). It passes the request on
// by returning nil without writing, leaving what it learned in Current
// (Set). It stops the request by returning an error, answered as a
// handler's is, or by writing a response itself (a redirect to sign in).
type Filter = Handler

// Pipeline is filters that run in order before a route's handler:
//
//	api := web.Pipeline{web.AcceptJSON, tokens.Require}
//	rt.Scope("/api/v1", api, func(s *web.Scope) { ... })
type Pipeline []Filter

// then is h behind the pipeline's filters.
func (p Pipeline) then(h Handler) Handler {
	if len(p) == 0 {
		return h
	}
	return func(w http.ResponseWriter, r *http.Request) error {
		for _, f := range p {
			if err := f(w, r); err != nil {
				return err
			}
			if tw, ok := w.(*trackingWriter); ok && tw.wrote {
				return nil // the filter answered
			}
		}
		return h(w, r)
	}
}

// Scope is where routes are added: the router's own (its root scope), or
// one under a path prefix running a pipeline for its routes (Rails' and
// Phoenix's scope). Scopes nest: prefixes join, and the outer scope's
// filters run first.
type Scope struct {
	rt      *Router
	mux     *http.ServeMux
	prefix  string
	filters Pipeline
	check   Check // a constraint's, nil outside one
}

// Scope adds routes under prefix ("/api/v1", or "" for none) that run
// pipeline's filters before their handlers.
func (s *Scope) Scope(prefix string, pipeline Pipeline, routes func(s *Scope)) {
	routes(&Scope{rt: s.rt, mux: s.mux, prefix: s.prefix + prefix,
		filters: append(slices.Clip(s.filters), pipeline...), check: s.check})
}

// Handle routes pattern ("GET /posts/{id}", ServeMux's syntax, under the
// scope's prefix) to h, behind the scope's filters.
func (s *Scope) Handle(pattern string, h Handler) {
	s.mux.Handle(s.pattern(pattern), s.rt.Wrap(s.filters.then(h)))
}

// Mount routes pattern to a plain http.Handler (files, a mounted package),
// behind the scope's filters.
func (s *Scope) Mount(pattern string, h http.Handler) {
	if len(s.filters) == 0 {
		s.mux.Handle(s.pattern(pattern), h)
		return
	}
	s.Handle(pattern, func(w http.ResponseWriter, r *http.Request) error {
		h.ServeHTTP(w, r)
		return nil
	})
}

// Cached routes pattern to h through the router's page cache (rt.Cache),
// or straight to h when the router has none.
func (s *Scope) Cached(pattern string, h Handler) {
	if s.rt.Cache == nil {
		s.Handle(pattern, h)
		return
	}
	s.mux.Handle(s.pattern(pattern), s.rt.Cache.Handler(s.rt.Wrap(s.filters.then(h))))
}

// pattern is a route's pattern under the scope's prefix: "GET /posts" in
// "/admin" is "GET /admin/posts".
func (s *Scope) pattern(pattern string) string {
	if s.prefix == "" {
		return pattern
	}
	method, path, ok := strings.Cut(pattern, " ")
	if !ok {
		method, path = "", pattern
	}
	host, rest := "", path
	if i := strings.Index(path, "/"); i > 0 {
		host, rest = path[:i], path[i:]
	}
	p := host + s.prefix + rest
	if method == "" {
		return p
	}
	return method + " " + p
}

// The router's routes are its root scope's.

// Scope adds routes under prefix that run pipeline's filters first.
func (rt *Router) Scope(prefix string, pipeline Pipeline, routes func(s *Scope)) {
	rt.root.Scope(prefix, pipeline, routes)
}

// Handle routes pattern ("GET /posts/{id}", ServeMux's syntax) to h.
func (rt *Router) Handle(pattern string, h Handler) { rt.root.Handle(pattern, h) }

// Mount routes pattern to a plain http.Handler (files, a mounted package).
func (rt *Router) Mount(pattern string, h http.Handler) { rt.root.Mount(pattern, h) }

// Cached routes pattern to h through the router's page cache (rt.Cache),
// or straight to h when the router has none.
func (rt *Router) Cached(pattern string, h Handler) { rt.root.Cached(pattern, h) }

// Resources routes a controller's actions under base, as Rails' resources
// does (see Scope.Resources).
func (rt *Router) Resources(base string, controller any, wrap func(http.Handler) http.Handler) {
	rt.root.Resources(base, controller, wrap)
}
