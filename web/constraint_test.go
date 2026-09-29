package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func sayp(s string) Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		io.WriteString(w, s+r.PathValue("name"))
		return nil
	}
}

func at(h http.Handler, host, path string) (int, string) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", path, nil)
	r.Host = host
	h.ServeHTTP(w, r)
	return w.Code, strings.TrimSpace(w.Body.String())
}

func TestConstraints(t *testing.T) {
	base := "example.com" // decided at run time, as an app's setup might
	rt := NewRouter(slog.New(slog.DiscardHandler), nil)
	rt.Constraint(HostFunc(func(host string) bool { return host == "hooks."+base }), func(s *Scope) {
		s.Handle("GET /ping", sayp("hooks ping"))
		s.Handle("GET /hook/{name}", sayp("hook "))
	})
	rt.Constraint(Subdomain("api"), func(s *Scope) {
		s.Handle("GET /ping", sayp("api ping"))
		s.Constraint(func(r *http.Request) bool { return r.Header.Get("X-Beta") != "" }, func(s *Scope) {
			s.Handle("GET /ping", sayp("beta ping")) // nested: tried before its parent
		})
	})
	rt.Constraint(Host("admin.example.com"), func(s *Scope) {
		s.Handle("GET /ping", sayp("admin ping"))
		s.Scope("/admin", Pipeline{trace("gate")}, func(s *Scope) {
			s.Handle("GET /dash", answer)
		})
	})
	rt.Handle("GET /ping", sayp("plain ping"))
	rt.Handle("GET /about", sayp("about"))
	h := rt.Handler()

	for _, tc := range []struct{ host, path, want string }{
		{"hooks.example.com", "/ping", "hooks ping"},
		{"Hooks.Example.COM.:443", "/ping", "hooks ping"},    // the host cleaned: case, port, trailing dot
		{"hooks.example.com", "/hook/deploy", "hook deploy"}, // path values inside
		{"hooks.example.com", "/about", "about"},             // no route inside: the rest get a try
		{"api.example.com", "/ping", "api ping"},
		{"www.example.com", "/ping", "plain ping"},            // no constraint passes
		{"example.com", "/ping", "plain ping"},                // no subdomain
		{"api.localhost", "/ping", "plain ping"},              // "api" is the domain there, not a subdomain
		{"admin.example.com", "/admin/dash", "gate,handler,"}, // a scope inside, with its filters
		{"www.example.com", "/admin/dash", "404"},
	} {
		code, body := at(h, tc.host, tc.path)
		if code == 404 {
			body = "404"
		}
		if body != tc.want {
			t.Errorf("%s%s = %d %q, want %q", tc.host, tc.path, code, body, tc.want)
		}
	}

	// A nested constraint: both checks must pass, and it's tried first.
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/ping", nil)
	r.Host, r.Header = "api.example.com", http.Header{"X-Beta": {"1"}}
	h.ServeHTTP(w, r)
	if got := w.Body.String(); got != "beta ping" {
		t.Errorf("nested = %q", got)
	}
	r = httptest.NewRequest("GET", "/ping", nil)
	r.Host, r.Header = "www.example.com", http.Header{"X-Beta": {"1"}}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if got := w.Body.String(); got != "plain ping" {
		t.Errorf("the nested check alone passed: %q", got)
	}

	// Hosts known at run time: the check reads them each time.
	if _, body := at(h, "hooks.example.org", "/ping"); body != "plain ping" {
		t.Errorf("before the base changed: %q", body)
	}
	base = "example.org"
	if _, body := at(h, "hooks.example.org", "/ping"); body != "hooks ping" {
		t.Errorf("after the base changed: %q", body)
	}
}

// A constraint inside a scope: its routes are under the scope's prefix and
// run the scope's filters.
func TestConstraintInAScope(t *testing.T) {
	rt := NewRouter(slog.New(slog.DiscardHandler), nil)
	rt.Scope("/v1", Pipeline{trace("outer")}, func(s *Scope) {
		s.Constraint(Host("api.example.com"), func(s *Scope) {
			s.Handle("GET /x", answer)
		})
	})
	if _, body := at(rt.Handler(), "api.example.com", "/v1/x"); body != "outer,handler," {
		t.Errorf("= %q, want the scope's filter first", body)
	}
}

// Constraints are tried in the order they were declared.
func TestConstraintOrder(t *testing.T) {
	rt := NewRouter(slog.New(slog.DiscardHandler), nil)
	rt.Constraint(Host("a.example.com", "b.example.com"), func(s *Scope) { s.Handle("GET /x", sayp("first")) })
	rt.Constraint(Host("b.example.com"), func(s *Scope) { s.Handle("GET /x", sayp("second")) })
	if _, body := at(rt.Handler(), "b.example.com", "/x"); body != "first" {
		t.Errorf("= %q, want the first declared", body)
	}
}

func TestRequestHost(t *testing.T) {
	for in, want := range map[string]string{
		"Example.com":      "example.com",
		"example.com:8080": "example.com",
		"example.com.":     "example.com",
		"[::1]:8080":       "::1",
		"shop.localhost":   "shop.localhost",
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Host = in
		if got := RequestHost(r); got != want {
			t.Errorf("%q = %q, want %q", in, got, want)
		}
	}
}
