package testkit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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

// VerifyReport is the structured result of Verify, also written as one JSON
// line prefixed with VerifyJSONPrefix when GANTRY_VERIFY_JSON=1 (for
// `bin/gantry verify --json`).
type VerifyReport struct {
	Passed         bool            `json:"passed"`
	Failures       int             `json:"failures"`
	Routes         int             `json:"routes"`
	UnmappedRoutes []string        `json:"unmapped_routes"`
	Features       []FeatureResult `json:"features"`
	// failures of features GANTRY_VERIFY_FEATURES left out, which have no
	// entry in Features
	Errors  []string         `json:"errors"`
	Browser *BrowserManifest `json:"browser,omitempty"`
}

// FeatureResult is one feature's verification outcome in VerifyReport.
type FeatureResult struct {
	Name      string        `json:"name"`
	Role      Role          `json:"role"`
	Cacheable bool          `json:"cacheable"`
	Passed    bool          `json:"passed"`
	Routes    []string      `json:"routes"`
	Visits    []VisitResult `json:"visits"`
	Cache     []CacheResult `json:"cache"`
	Leaks     []LeakResult  `json:"leaks"`
	Errors    []string      `json:"errors"`
}

// VisitResult is one visit check for a feature as a specific role.
type VisitResult struct {
	Path    string `json:"path"`
	Role    Role   `json:"role"`
	Status  int    `json:"status"`
	Allowed bool   `json:"allowed"`
	Passed  bool   `json:"passed"`
	Error   string `json:"error,omitempty"`
}

// CacheResult is one page-cache header and 304 check for a cacheable visit.
type CacheResult struct {
	Path   string `json:"path"`
	Passed bool   `json:"passed"`
	Error  string `json:"error,omitempty"`
}

// LeakResult is one cross-user cache-leak check for a cacheable visit and role.
type LeakResult struct {
	Path   string `json:"path"`
	Role   Role   `json:"role"`
	Passed bool   `json:"passed"`
	Error  string `json:"error,omitempty"`
}

// VerifyJSONPrefix marks the compact JSON line Verify prints when
// GANTRY_VERIFY_JSON=1 so `bin/gantry verify --json` can extract it from test output.
const VerifyJSONPrefix = "GANTRY_VERIFY_JSON:"

var verifyJSONOut io.Writer = os.Stdout

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
func Verify(t testing.TB, h http.Handler, routes []web.Route, path string, roles Roles) VerifyReport {
	t.Helper()
	if env.Development() {
		t.Fatalf("testkit: Verify runs the app as GANTRY_ENV=development, whose reload script takes the page cache's ETag and Last-Modified off every page: run it as GANTRY_ENV=test, as bin/gantry verify does")
	}
	allFeatures := Features(t, path)
	features := filterFeatures(t, path, allFeatures, parseVerifyFeatures(os.Getenv("GANTRY_VERIFY_FEATURES")))
	report := VerifyReport{
		Passed:         true,
		UnmappedRoutes: []string{},
		Features:       make([]FeatureResult, len(features)),
		Errors:         []string{},
	}
	indexByName := make(map[string]int, len(features))
	for i, f := range features {
		indexByName[f.Name] = i
		report.Features[i] = FeatureResult{
			Name:      f.Name,
			Role:      f.Role,
			Cacheable: f.Cacheable,
			Passed:    true,
			Routes:    []string{},
			Visits:    []VisitResult{},
			Cache:     []CacheResult{},
			Leaks:     []LeakResult{},
			Errors:    []string{},
		}
	}
	failFeature := func(i int, format string, args ...any) {
		t.Helper()
		msg := fmt.Sprintf(format, args...)
		t.Errorf("%s", msg)
		report.Passed = false
		report.Failures++
		if i < 0 {
			report.Errors = append(report.Errors, msg)
			return
		}
		report.Features[i].Passed = false
		report.Features[i].Errors = append(report.Features[i].Errors, msg)
	}
	for _, r := range routes {
		if r.Mounted {
			continue
		}
		report.Routes++
		name := strings.TrimSpace(r.Method + " " + r.Path)
		in := 0
		for _, f := range allFeatures {
			if !f.covers(r) {
				continue
			}
			in++
			idx, selected := indexByName[f.Name]
			if !selected {
				idx = -1
			} else {
				report.Features[idx].Routes = append(report.Features[idx].Routes, name)
			}
			if f.Cacheable && !r.Cached {
				failFeature(idx, "%s: %s is cacheable, and %s isn't Cached", path, f.Name, name)
			}
			if r.Cached && !f.Cacheable {
				failFeature(idx, "%s: %s is Cached, and its feature %s isn't cacheable", path, name, f.Name)
			}
		}
		if in == 0 {
			t.Errorf("%s: %s is in no feature: add it to one (gantry routes lists the app's routes)", path, name)
			report.Passed = false
			report.Failures++
			report.UnmappedRoutes = append(report.UnmappedRoutes, name)
		}
	}
	for i, f := range features {
		if f.Role != Public && roles[f.Role] == nil {
			failFeature(i, "%s: %s is for %s visitors, and Verify has no SignIn for %s", path, f.Name, f.Role, f.Role)
		}
	}
	for i, f := range features {
		if f.Cacheable {
			for _, visit := range f.Visit {
				for _, res := range leaks(t, h, f, visit.Path, roles) {
					report.Features[i].Leaks = append(report.Features[i].Leaks, res)
					if !res.Passed {
						report.Passed = false
						report.Failures++
						report.Features[i].Passed = false
					}
				}
			}
		}
	}
	for i, f := range features {
		for _, visit := range f.Visit {
			for _, role := range allRoles {
				if role == Public || roles[role] != nil {
					res := visitAs(t, h, f, visit, role, roles[role])
					report.Features[i].Visits = append(report.Features[i].Visits, res)
					if !res.Passed {
						report.Passed = false
						report.Failures++
						report.Features[i].Passed = false
					}
				}
			}
			if f.Cacheable && (f.Role == Public || roles[f.Role] != nil) {
				res := cached(t, h, f, visit.Path, roles[f.Role])
				report.Features[i].Cache = append(report.Features[i].Cache, res)
				if !res.Passed {
					report.Passed = false
					report.Failures++
					report.Features[i].Passed = false
				}
			}
		}
	}
	if os.Getenv("GANTRY_VERIFY_BROWSER") == "1" {
		selectedNames := make([]string, 0, len(features))
		for _, f := range features {
			selectedNames = append(selectedNames, f.Name)
		}
		bm := VerifyBrowser(t, h, path, roles, BrowserOptions{
			ArtifactsDir: os.Getenv("GANTRY_VERIFY_ARTIFACTS"),
			CDPURL:       os.Getenv("GANTRY_CDP_URL"),
			ChromePath:   os.Getenv("GANTRY_CHROME_BIN"),
			Features:     selectedNames,
		})
		report.Browser = &bm
		if !bm.Passed {
			report.Passed = false
			report.Failures += bm.Failures
		}
	}
	if os.Getenv("GANTRY_VERIFY_JSON") == "1" {
		if data, err := json.Marshal(report); err == nil {
			fmt.Fprintf(verifyJSONOut, "%s%s\n", VerifyJSONPrefix, data)
		}
	}
	return report
}

func parseVerifyFeatures(raw string) []string {
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		name := strings.TrimSpace(part)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

func filterFeatures(t testing.TB, path string, all []Feature, selected []string) []Feature {
	t.Helper()
	if len(selected) == 0 {
		return all
	}
	byName := make(map[string]Feature, len(all))
	valid := make([]string, 0, len(all))
	for _, f := range all {
		byName[f.Name] = f
		valid = append(valid, f.Name)
	}
	want := make(map[string]bool, len(selected))
	for _, name := range selected {
		if _, ok := byName[name]; !ok {
			t.Fatalf("testkit: %s has no feature %q (available: %s)", path, name, strings.Join(valid, ", "))
		}
		want[name] = true
	}
	var filtered []Feature
	for _, f := range all {
		if want[f.Name] {
			filtered = append(filtered, f)
		}
	}
	return filtered
}

type captureTB struct {
	testing.TB
	errs []string
}

func (c *captureTB) Errorf(format string, args ...any) {
	c.TB.Helper()
	msg := fmt.Sprintf(format, args...)
	c.errs = append(c.errs, msg)
	c.TB.Errorf("%s", msg)
}

func visitAs(t testing.TB, h http.Handler, f Feature, visit Visit, role Role, signIn SignIn) VisitResult {
	t.Helper()
	v := Browser(t, h)
	if signIn != nil {
		signIn(v, 0)
	}
	p := v.Get(visit.Path)
	as := f.Name + " as " + string(role)
	allowed := rank(role) >= rank(f.Role)
	res := VisitResult{Path: visit.Path, Role: role, Status: p.Code, Allowed: allowed, Passed: true}
	if !allowed {
		if p.Code >= 200 && p.Code < 300 {
			msg := fmt.Sprintf("%s: GET %s = %d: a %s page, shown to a %s visitor", as, visit.Path, p.Code, f.Role, role)
			t.Errorf("%s", msg)
			res.Passed, res.Error = false, msg
		} else {
			t.Logf("%s: GET %s %d, turned away", as, visit.Path, p.Code)
		}
		return res
	}
	want := visit.Status
	if want == 0 {
		want = http.StatusOK
	}
	if p.Code != want {
		msg := fmt.Sprintf("%s: GET %s = %d, want %d", as, visit.Path, p.Code, want)
		t.Errorf("%s", msg)
		res.Passed, res.Error = false, msg
		return res
	}
	var errs []string
	if visit.Selector != "" && p.Find(visit.Selector).Length() == 0 {
		msg := fmt.Sprintf("%s: GET %s has no %q", as, visit.Path, visit.Selector)
		t.Errorf("%s", msg)
		errs = append(errs, msg)
	}
	if p.Code == http.StatusOK && strings.HasPrefix(p.Header.Get("Content-Type"), "text/html") {
		cap := &captureTB{TB: t}
		links(cap, h, Page{Path: visit.Path, Header: v.headers(visit.Path)})
		errs = append(errs, cap.errs...)
	}
	if len(errs) > 0 {
		res.Passed, res.Error = false, strings.Join(errs, "; ")
	}
	t.Logf("%s: GET %s %d", as, visit.Path, p.Code)
	return res
}

func cached(t testing.TB, h http.Handler, f Feature, path string, signIn SignIn) CacheResult {
	t.Helper()
	res := CacheResult{Path: path, Passed: true}
	v := Browser(t, h)
	if signIn != nil {
		signIn(v, 0)
	}
	v.Get(path)
	p := v.Get(path)
	etag := p.Header.Get("ETag")
	if etag == "" || p.Header.Get("Last-Modified") == "" {
		msg := fmt.Sprintf("%s: GET %s has no ETag or Last-Modified: the page cache didn't serve it", f.Name, path)
		t.Errorf("%s", msg)
		res.Passed, res.Error = false, msg
		return res
	}
	v.Header.Set("If-None-Match", etag)
	if again := v.Get(path); again.Code != http.StatusNotModified {
		msg := fmt.Sprintf("%s: GET %s with its ETag = %d, want 304", f.Name, path, again.Code)
		t.Errorf("%s", msg)
		res.Passed, res.Error = false, msg
	}
	return res
}

func leaks(t testing.TB, h http.Handler, f Feature, path string, roles Roles) []LeakResult {
	t.Helper()
	var results []LeakResult
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
		res := LeakResult{Path: path, Role: role, Passed: true}
		var errs []string
		own := probe(t, path, role)
		first.Get(own)
		second := Browser(t, h)
		other := signIn(second, 1)
		if strings.Contains(second.Get(own).Body, who) {
			msg := fmt.Sprintf("%s: GET %s showed %s %s's page to %s %s", f.Name, path, role, who, role, other)
			t.Errorf("%s", msg)
			errs = append(errs, msg)
		}
		if f.Role == Public && strings.Contains(Browser(t, h).Get(own).Body, who) {
			msg := fmt.Sprintf("%s: GET %s showed %s %s's page to a public visitor", f.Name, path, role, who)
			t.Errorf("%s", msg)
			errs = append(errs, msg)
		}
		if len(errs) > 0 {
			res.Passed, res.Error = false, strings.Join(errs, "; ")
		}
		results = append(results, res)
	}
	return results
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
