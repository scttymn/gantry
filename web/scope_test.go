package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

type user struct{ Name string }

var (
	userKey  = NewKey[user]("user")
	traceKey = NewKey[[]string]("trace")
)

// trace is a filter that notes its name in Current, in order.
func trace(name string) Filter {
	return func(w http.ResponseWriter, r *http.Request) error {
		t, _ := Get(r, traceKey)
		Set(r, traceKey, append(t, name))
		return nil
	}
}

// answer is a handler that writes what the filters before it left.
func answer(w http.ResponseWriter, r *http.Request) error {
	t, _ := Get(r, traceKey)
	u, _ := Get(r, userKey)
	io.WriteString(w, strings.Join(append(t, "handler", u.Name), ","))
	return nil
}

func serve(h http.Handler, method, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, path, nil))
	return w
}

func TestPipelines(t *testing.T) {
	rt := NewRouter(slog.New(slog.DiscardHandler), nil)
	signIn := func(w http.ResponseWriter, r *http.Request) error {
		if r.URL.Query().Get("as") == "" {
			http.Redirect(w, r, "/login", http.StatusFound)
			return nil
		}
		Set(r, userKey, user{Name: r.URL.Query().Get("as")})
		return nil
	}
	refuse := func(w http.ResponseWriter, r *http.Request) error {
		if r.URL.Query().Get("token") != "ok" {
			return Status(http.StatusUnauthorized, nil)
		}
		return nil
	}
	rt.Handle("GET /open", answer)
	rt.Scope("/admin", Pipeline{trace("a"), signIn, trace("b")}, func(s *Scope) {
		s.Handle("GET /dashboard", answer)
		s.Scope("/deep", Pipeline{trace("c")}, func(s *Scope) {
			s.Handle("GET /{$}", answer)
		})
	})
	rt.Scope("/api", Pipeline{refuse}, func(s *Scope) {
		s.Handle("GET /ping", answer)
		s.Mount("GET /files/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "a file") }))
		s.Resources("/posts", postsController{}, nil)
	})
	h := rt.Handler()

	for _, tc := range []struct{ path, code, body string }{
		{"/open", "200", "handler,"},                                 // no scope, no filters
		{"/admin/dashboard?as=sam", "200", "a,b,handler,sam"},        // in order, Current passed on
		{"/admin/deep/?as=sam", "200", "a,b,c,handler,sam"},          // the outer scope's first
		{"/admin/dashboard", "302", "<a href=\"/login\">Found</a>."}, // a filter that answers ends it
		{"/api/ping", "401", ""},                                     // a filter's error, answered as a handler's
		{"/api/ping?token=ok", "200", "handler,"},
		{"/api/files/x?token=ok", "200", "a file"}, // Mount, through the scope's filters
		{"/api/files/x", "401", ""},
		{"/api/posts/7?token=ok", "200", "post 7"}, // Resources, prefixed and filtered
		{"/api/posts/7", "401", ""},
		{"/dashboard", "404", ""}, // a scope's routes are only under its prefix
	} {
		w := serve(h, "GET", tc.path)
		if got := strings.TrimSpace(w.Body.String()); strconv.Itoa(w.Code) != tc.code || got != tc.body {
			t.Errorf("%s = %d %q, want %s %q", tc.path, w.Code, got, tc.code, tc.body)
		}
	}
}

type postsController struct{}

func (postsController) Show(w http.ResponseWriter, r *http.Request) error {
	io.WriteString(w, "post "+r.PathValue("id"))
	return nil
}

func TestCurrent(t *testing.T) {
	r := WithCurrent(httptest.NewRequest("GET", "/", nil))
	if _, ok := Get(r, userKey); ok {
		t.Error("a value before any was set")
	}
	Set(r, userKey, user{Name: "sam"})
	if u, ok := Get(r, userKey); !ok || u.Name != "sam" {
		t.Errorf("Get = %v, %v", u, ok)
	}
	// Two keys with one name are two keys.
	other := NewKey[user]("user")
	if _, ok := Get(r, other); ok {
		t.Error("another key with the same name read the first's value")
	}
	// A request that didn't come through a router has no Current to set.
	defer func() {
		if v := recover(); v == nil || !strings.Contains(v.(string), "WithCurrent") {
			t.Errorf("Set without Current: %v", v)
		}
	}()
	Set(httptest.NewRequest("GET", "/", nil), userKey, user{})
}

// Each request has a Current of its own.
func TestCurrentPerRequest(t *testing.T) {
	rt := NewRouter(slog.New(slog.DiscardHandler), nil)
	rt.Scope("", Pipeline{trace("x")}, func(s *Scope) { s.Handle("GET /t", answer) })
	h := rt.Handler()
	for range 3 {
		if got := serve(h, "GET", "/t").Body.String(); got != "x,handler," {
			t.Fatalf("a value leaked from another request: %q", got)
		}
	}
}
