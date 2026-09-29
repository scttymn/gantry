package web

import (
	"net"
	"net/http"
	"slices"
	"strings"
)

// Check is a constraint's test of a request (Rails' matches?).
type Check = func(*http.Request) bool

// constrained is a constraint's routes: its own mux, reached when its check
// passes.
type constrained struct {
	check Check
	mux   *http.ServeMux
}

// Constraint adds routes that exist only for requests check passes (Rails'
// constraints): a webhooks host, a subdomain, a client's address. A request
// tries the constraints in the order they were declared, a nested one
// before its parent, and is served by the first whose check passes and
// which has a route for it; otherwise by the routes outside any constraint
// (Rails' fall-through), else 404. Its routes are under the scope's prefix
// and run its filters. A constraint decides whether a route exists; a
// filter, whether a request may go on (401, a redirect).
func (s *Scope) Constraint(check Check, routes func(s *Scope)) {
	if parent := s.check; parent != nil {
		own := check
		check = func(r *http.Request) bool { return parent(r) && own(r) }
	}
	c := &Scope{rt: s.rt, mux: http.NewServeMux(), prefix: s.prefix, filters: slices.Clip(s.filters), check: check}
	routes(c)
	// After its routes: a constraint declared inside it comes first.
	s.rt.constraints = append(s.rt.constraints, constrained{check: check, mux: c.mux})
}

// Constraint adds routes that exist only for requests check passes (see
// Scope.Constraint).
func (rt *Router) Constraint(check Check, routes func(s *Scope)) { rt.root.Constraint(check, routes) }

// route serves r from the first constraint that has it, else from the
// routes outside any.
func (rt *Router) route(w http.ResponseWriter, r *http.Request) {
	for _, c := range rt.constraints {
		if !c.check(r) {
			continue
		}
		if _, pattern := c.mux.Handler(r); pattern != "" {
			c.mux.ServeHTTP(w, r)
			return
		}
	}
	rt.mux.ServeHTTP(w, r)
}

// RequestHost is the host a request is for, cleaned for comparing: without
// its port, lowercase, and without a trailing dot.
func RequestHost(r *http.Request) string {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.TrimSuffix(strings.ToLower(host), ".")
}

// Host is a check that passes for any of hosts ("hooks.example.com").
func Host(hosts ...string) Check {
	want := map[string]bool{}
	for _, h := range hosts {
		want[strings.TrimSuffix(strings.ToLower(h), ".")] = true
	}
	return func(r *http.Request) bool { return want[RequestHost(r)] }
}

// HostFunc is a check on the request's host, cleaned (RequestHost), for
// hosts known only at run time: an app's domain set in its settings.
func HostFunc(match func(host string) bool) Check {
	return func(r *http.Request) bool { return match(RequestHost(r)) }
}

// Subdomain is a check that passes when the host's subdomain is one of
// names: what's left of the host after its last two labels, as Rails'
// (tld_length 1): "api" in api.example.com, and none in api.localhost.
func Subdomain(names ...string) Check {
	return func(r *http.Request) bool {
		labels := strings.Split(RequestHost(r), ".")
		if len(labels) < 3 {
			return false
		}
		return slices.Contains(names, strings.Join(labels[:len(labels)-2], "."))
	}
}
