package testkit

import (
	"bytes"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/scttymn/gantry/internal/env"
	"github.com/scttymn/gantry/internal/featuremap"
	"github.com/scttymn/gantry/web"
)

// Feature is one of an app's features in its feature map
// (test/features.yaml): what it's called, the routes it's made of, who it's
// for, and the pages Verify visits to check it.
//
//	features:
//	  - name: posts
//	    routes: [/posts, /posts/]
//	    role: public
//	    visit:
//	      - path: /posts
//	        selector: h1
type Feature struct {
	Name string `yaml:"name"`
	// Routes are its routes, as `gantry routes` shows them: "GET /posts",
	// or with no method, any method's. One ending in / is every route
	// under it too, as ServeMux's patterns are: "/posts/" has
	// "GET /posts/{id}" and "DELETE /posts/{id}".
	Routes []string `yaml:"routes"`
	Role   Role     `yaml:"role"`
	// Cacheable: its routes are Cached, and its pages are the same for
	// everyone its role lets in.
	Cacheable bool    `yaml:"cacheable"`
	Visit     []Visit `yaml:"visit"`
}

// Visit is a page Verify requests, and what it must answer.
type Visit struct {
	Path string `yaml:"path"`
	// 200 when 0
	Status int `yaml:"status"`
	// an element the page has: "h1", "form#new_post"
	Selector string `yaml:"selector"`
}

// Role is who a feature is for. Each can do what the one before it can: a
// public page is for everyone, an admin's only for admins.
type Role string

const (
	Public   Role = "public"
	SignedIn Role = "signed-in"
	Admin    Role = "admin"
)

var allRoles = []Role{Public, SignedIn, Admin}

// SignIn signs v in as the nth (0 or 1) of a role's test users, and returns
// what pages show only for that user (their email), which Verify checks no
// one else is shown; "" skips that check.
type SignIn func(v *Visitor, n int) (who string)

// Roles are how Verify signs in as each role but Public.
type Roles map[Role]SignIn

// Features reads a feature map: its features, under "features".
func Features(t testing.TB, path string) []Feature {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("testkit: %v", err)
	}
	var m struct {
		Features []Feature `yaml:"features"`
	}
	d := yaml.NewDecoder(bytes.NewReader(b))
	d.KnownFields(true)
	if err := d.Decode(&m); err != nil {
		t.Fatalf("testkit: %s: %v", path, err)
	}
	seen := map[string]bool{}
	for _, f := range m.Features {
		switch {
		case f.Name == "":
			t.Fatalf("testkit: %s: a feature with no name", path)
		case seen[f.Name]:
			t.Fatalf("testkit: %s: %s: two features have that name", path, f.Name)
		case len(f.Routes) == 0:
			t.Fatalf("testkit: %s: %s: no routes", path, f.Name)
		case !slices.Contains(allRoles, f.Role):
			t.Fatalf("testkit: %s: %s: role %q: public, signed-in or admin", path, f.Name, f.Role)
		}
		seen[f.Name] = true
		for _, v := range f.Visit {
			if !strings.HasPrefix(v.Path, "/") {
				t.Fatalf("testkit: %s: %s: visit %q: a path starts with /", path, f.Name, v.Path)
			}
		}
	}
	return m.Features
}

func (f Feature) covers(r web.Route) bool {
	return slices.ContainsFunc(f.Routes, func(route string) bool { return featuremap.Covers(route, r.Method, r.Path) })
}

func rank(r Role) int { return slices.Index(allRoles, r) }

// Verify checks an app against its feature map, the file at path
// (test/features.yaml), in the test's process: `gantry verify` runs the
// test that calls it. routes are h's (Router.Routes).
//
//   - Every route is in a feature, but those Mounted (files, a mounted
//     package): a route in none fails. A cacheable feature's routes are
//     Cached, and a Cached route's feature is cacheable.
//   - Each feature's pages are visited as each role there's a way to sign
//     in as (roles): one its role lets in gets the visit's status (200)
//     and has its selector, and everything the page refers to loads
//     (Links); a role before it is turned away (not a 2xx).
//   - A cacheable feature's pages come from the page cache (an ETag and a
//     Last-Modified, and a 304 for a browser that has the page), and no
//     signed-in user's copy is shown to another, or to the public: for
//     each role, its user 0 is the first to request the page (at a
//     gantry-verify=<role> query of the role's own, so each role renders
//     its own copy), and user 1 and a public visitor mustn't be shown
//     user 0's who there.
//
// A feature for a role with no SignIn in roles fails. So does Verify under
// GANTRY_ENV=development, where the reload script the router adds takes the
// page cache's headers off every page: run it as GANTRY_ENV=test.
func Verify(t testing.TB, h http.Handler, routes []web.Route, path string, roles Roles) {
	t.Helper()
	if env.Development() {
		t.Fatalf("testkit: Verify runs the app as GANTRY_ENV=development, whose reload script takes the page cache's ETag and Last-Modified off every page: run it as GANTRY_ENV=test, as bin/gantry verify does")
	}
	features := Features(t, path)
	for _, r := range routes {
		if r.Mounted {
			continue
		}
		name := strings.TrimSpace(r.Method + " " + r.Path)
		in := 0
		for _, f := range features {
			if !f.covers(r) {
				continue
			}
			in++
			if f.Cacheable && !r.Cached {
				t.Errorf("%s: %s is cacheable, and %s isn't Cached", path, f.Name, name)
			}
			if r.Cached && !f.Cacheable {
				t.Errorf("%s: %s is Cached, and its feature %s isn't cacheable", path, name, f.Name)
			}
		}
		if in == 0 {
			t.Errorf("%s: %s is in no feature: add it to one (gantry routes lists the app's routes)", path, name)
		}
	}
	for _, f := range features {
		if f.Role != Public && roles[f.Role] == nil {
			t.Errorf("%s: %s is for %s visitors, and Verify has no SignIn for %s", path, f.Name, f.Role, f.Role)
		}
	}
	for _, f := range features {
		if f.Cacheable {
			for _, visit := range f.Visit {
				leaks(t, h, f, visit.Path, roles)
			}
		}
	}
	for _, f := range features {
		for _, visit := range f.Visit {
			for _, role := range allRoles {
				if role == Public || roles[role] != nil {
					visitAs(t, h, f, visit, role, roles[role])
				}
			}
			if f.Cacheable && (f.Role == Public || roles[f.Role] != nil) {
				cached(t, h, f, visit.Path, roles[f.Role])
			}
		}
	}
}

func visitAs(t testing.TB, h http.Handler, f Feature, visit Visit, role Role, signIn SignIn) {
	t.Helper()
	v := Browser(t, h)
	if signIn != nil {
		signIn(v, 0)
	}
	p := v.Get(visit.Path)
	as := f.Name + " as " + string(role)
	if rank(role) < rank(f.Role) {
		if p.Code >= 200 && p.Code < 300 {
			t.Errorf("%s: GET %s = %d: a %s page, shown to a %s visitor", as, visit.Path, p.Code, f.Role, role)
		} else {
			t.Logf("%s: GET %s %d, turned away", as, visit.Path, p.Code)
		}
		return
	}
	want := visit.Status
	if want == 0 {
		want = http.StatusOK
	}
	if p.Code != want {
		t.Errorf("%s: GET %s = %d, want %d", as, visit.Path, p.Code, want)
		return
	}
	if visit.Selector != "" && p.Find(visit.Selector).Length() == 0 {
		t.Errorf("%s: GET %s has no %q", as, visit.Path, visit.Selector)
	}
	if p.Code == http.StatusOK && strings.HasPrefix(p.Header.Get("Content-Type"), "text/html") {
		links(t, h, Page{Path: visit.Path, Header: v.headers(visit.Path)})
	}
	t.Logf("%s: GET %s %d", as, visit.Path, p.Code)
}

func cached(t testing.TB, h http.Handler, f Feature, path string, signIn SignIn) {
	t.Helper()
	v := Browser(t, h)
	if signIn != nil {
		signIn(v, 0)
	}
	v.Get(path)
	p := v.Get(path)
	etag := p.Header.Get("ETag")
	if etag == "" || p.Header.Get("Last-Modified") == "" {
		t.Errorf("%s: GET %s has no ETag or Last-Modified: the page cache didn't serve it", f.Name, path)
		return
	}
	v.Header.Set("If-None-Match", etag)
	if again := v.Get(path); again.Code != http.StatusNotModified {
		t.Errorf("%s: GET %s with its ETag = %d, want 304", f.Name, path, again.Code)
	}
}

func leaks(t testing.TB, h http.Handler, f Feature, path string, roles Roles) {
	t.Helper()
	for _, role := range allRoles[1:] {
		signIn := roles[role]
		if signIn == nil || rank(role) < rank(f.Role) {
			continue
		}
		first := Browser(t, h)
		who := signIn(first, 0)
		if who == "" {
			continue
		}
		own := probe(t, path, role)
		first.Get(own)
		second := Browser(t, h)
		other := signIn(second, 1)
		if strings.Contains(second.Get(own).Body, who) {
			t.Errorf("%s: GET %s showed %s %s's page to %s %s", f.Name, path, role, who, role, other)
		}
		if f.Role == Public && strings.Contains(Browser(t, h).Get(own).Body, who) {
			t.Errorf("%s: GET %s showed %s %s's page to a public visitor", f.Name, path, role, who)
		}
	}
}

func probe(t testing.TB, path string, role Role) string {
	t.Helper()
	u, err := url.Parse(path)
	if err != nil {
		t.Fatalf("testkit: %q isn't a path: %v", path, err)
	}
	q := u.Query()
	q.Set("gantry-verify", string(role))
	u.RawQuery = q.Encode()
	return u.String()
}

func (v *Visitor) headers(path string) http.Header {
	u, _ := url.Parse(origin)
	u, _ = u.Parse(path)
	header := v.Header.Clone()
	header.Set("Host", u.Host)
	for _, c := range v.jar.Cookies(u) {
		header.Add("Cookie", c.String())
	}
	return header
}
