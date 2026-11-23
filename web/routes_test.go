package web

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"testing"
)

func requireAdmin(w http.ResponseWriter, r *http.Request) error { return nil }

type guard struct{}

func (guard) Check(w http.ResponseWriter, r *http.Request) error { return nil }

func outer(next http.Handler) http.Handler { return next }

func deep(w http.ResponseWriter, r *http.Request) error { return nil }

// Routes lists every route as it was declared: its method and whole path,
// the scope it's in, the filters that run before it, by name, and whether
// the page cache serves it, it's mounted or a constraint has it.
func TestRoutes(t *testing.T) {
	rt := NewRouter(slog.New(slog.DiscardHandler), nil)
	rt.Cache = &PageCache{}
	file := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "a file") })
	rt.Handle("GET /{$}", answer)
	rt.Cached("GET /about", answer)
	rt.Mount("GET /assets/", file)
	rt.Scope("/admin", Pipeline{requireAdmin, guard{}.Check}, func(s *Scope) {
		s.Handle("GET /dashboard", answer)
		s.Mount("GET /files/", file)
		s.Scope("/deep", Pipeline{deep}, func(s *Scope) {
			s.Cached("GET /{$}", answer)
		})
		s.Resources("/posts", postsController{}, outer)
	})
	rt.Constraint(Host("hooks.example.com"), func(s *Scope) {
		s.Handle("POST /stripe", answer)
	})
	rt.CrossOriginCallback("POST /auth/callback", answer)
	rt.Handle("/any", answer)
	admin := []string{"web.requireAdmin", "web.guard.Check"}
	want := []Route{
		{Method: "GET", Path: "/up", Filters: []string{}},
		{Method: "GET", Path: "/{$}", Filters: []string{}},
		{Method: "GET", Path: "/about", Filters: []string{}, Cached: true},
		{Method: "GET", Path: "/assets/", Filters: []string{}, Mounted: true},
		{Method: "GET", Path: "/admin/dashboard", Scope: "/admin", Filters: admin},
		{Method: "GET", Path: "/admin/files/", Scope: "/admin", Filters: admin, Mounted: true},
		{Method: "GET", Path: "/admin/deep/{$}", Scope: "/admin/deep", Filters: []string{"web.requireAdmin", "web.guard.Check", "web.deep"}, Cached: true},
		{Method: "GET", Path: "/admin/posts/{id}", Scope: "/admin", Filters: []string{"web.outer", "web.requireAdmin", "web.guard.Check"}},
		{Method: "POST", Path: "/stripe", Filters: []string{}, Constrained: true},
		{Method: "POST", Path: "/auth/callback", Filters: []string{}},
		{Path: "/any", Filters: []string{}},
	}
	if got := rt.Routes(); !reflect.DeepEqual(got, want) {
		t.Errorf("Routes:\n got %+v\nwant %+v", got, want)
	}

	// Its JSON's field names are stable: gantry routes --json prints them.
	b, _ := json.Marshal(want[6])
	if got := string(b); got != `{"method":"GET","path":"/admin/deep/{$}","scope":"/admin/deep","filters":["web.requireAdmin","web.guard.Check","web.deep"],"cached":true,"mounted":false,"constrained":false}` {
		t.Errorf("JSON: %s", got)
	}
	if got := want[6].String(); got != "GET /admin/deep/{$}  [web.requireAdmin web.guard.Check web.deep] cached" {
		t.Errorf("String: %q", got)
	}
	if got := want[10].String(); got != "/any" {
		t.Errorf("String: %q", got)
	}

	// The routes still serve: listing them changed nothing.
	h := rt.Handler()
	for path, want := range map[string]int{"/about": 200, "/admin/files/x": 200, "/admin/deep/": 200, "/admin/posts/7": 200, "/assets/x": 200} {
		if w := serve(h, "GET", path); w.Code != want {
			t.Errorf("GET %s = %d, want %d", path, w.Code, want)
		}
	}
	// What Routes returns is a copy.
	rt.Routes()[1].Filters = append(rt.Routes()[1].Filters, "x")
	rt.Routes()[4].Filters[0] = "x"
	if got := rt.Routes(); !reflect.DeepEqual(got, want) {
		t.Errorf("a caller changed the router's routes: %+v", got)
	}
}

// Without a page cache, Cached routes aren't cached, and Routes says so.
func TestRoutesWithoutCache(t *testing.T) {
	rt := NewRouter(slog.New(slog.DiscardHandler), nil)
	rt.Cached("GET /about", answer)
	if got := rt.Routes()[1]; got.Cached || got.Path != "/about" {
		t.Errorf("Routes()[1] = %+v", got)
	}
}
