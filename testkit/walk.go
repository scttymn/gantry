package testkit

import (
	"fmt"
	"math/rand/v2"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
)

// WalkOptions configures Walk, a seeded stateful property-based walker for a
// Gantry application.
type WalkOptions struct {
	// Seed makes the sequence of actions reproducible; 0 uses a fixed default
	// seed (1) so failures repeat across runs.
	Seed uint64
	// Steps is the number of actions to execute (default 60).
	Steps int
	// StartPaths are the initial paths to visit (default []string{"/"}).
	StartPaths []string
	// Roles are the sign-in functions for non-public roles.
	Roles Roles
	// AllowDelete lets Walk submit forms with class "delete" or DELETE method.
	AllowDelete bool
	// CheckLinks checks testkit.Links on every 200 HTML response when true.
	CheckLinks bool
	// FieldValue optionally supplies form field values during random submissions.
	FieldValue func(rng *rand.Rand, formAction, fieldName, fieldType string) string
}

// WalkStep is one recorded action in WalkReport.Trace for reproducing a failure.
type WalkStep struct {
	Step   int    `json:"step"`
	Role   Role   `json:"role"`
	Action string `json:"action"`
	Target string `json:"target"`
	Status int    `json:"status"`
}

// WalkReport summarizes a completed Walk run.
type WalkReport struct {
	Seed   uint64     `json:"seed"`
	Steps  int        `json:"steps"`
	Passed bool       `json:"passed"`
	Trace  []WalkStep `json:"trace"`
	Errors []string   `json:"errors"`
}

type walkTB struct {
	testing.TB
	errs []string
}

func (c *walkTB) Errorf(format string, args ...any) {
	c.TB.Helper()
	msg := fmt.Sprintf(format, args...)
	c.errs = append(c.errs, msg)
	c.TB.Errorf("%s", msg)
}

type walkForm struct {
	pagePath string
	selector string
	action   string
	method   string
	isDelete bool
	hasLock  bool
}

// Walk drives h through a seeded random sequence of link visits, role switches,
// form submissions, and stale lock_version replays, asserting at every step:
//
//   - No panics and no 5xx responses.
//   - When CheckLinks is set, all referenced assets and links resolve on 200 HTML pages.
//   - Signed-in visitors' identifying markers (returned by Roles) never appear
//     on responses served to a public visitor.
//   - When an edit form carries a hidden lock_version field and succeeds once,
//     replaying the earlier stale form returns 409 Conflict.
func Walk(t testing.TB, h http.Handler, opts WalkOptions) WalkReport {
	t.Helper()
	seed := opts.Seed
	if seed == 0 {
		seed = 1
	}
	steps := opts.Steps
	if steps <= 0 {
		steps = 60
	}
	startPaths := opts.StartPaths
	if len(startPaths) == 0 {
		startPaths = []string{"/"}
	}
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	report := WalkReport{
		Seed:   seed,
		Passed: true,
		Trace:  []WalkStep{},
		Errors: []string{},
	}

	availableRoles := []Role{Public}
	visitors := map[Role]*Visitor{Public: Browser(t, h)}
	roleSecrets := map[Role]string{}
	for _, r := range allRoles[1:] {
		if opts.Roles[r] != nil {
			v := Browser(t, h)
			who := opts.Roles[r](v, 0)
			visitors[r] = v
			availableRoles = append(availableRoles, r)
			if who != "" {
				roleSecrets[r] = who
			}
		}
	}

	knownPaths := slices.Clone(startPaths)
	seenPath := map[string]bool{}
	for _, p := range knownPaths {
		seenPath[p] = true
	}
	var knownForms []walkForm
	seenForm := map[string]bool{}
	testedStale := map[string]bool{}

	fail := func(step int, role Role, action, target string, status int, msg string) {
		t.Helper()
		report.Passed = false
		report.Errors = append(report.Errors, msg)
		var traceLines []string
		for _, s := range report.Trace {
			traceLines = append(traceLines, fmt.Sprintf("  #%d [%s] %s %s -> %d", s.Step, s.Role, s.Action, s.Target, s.Status))
		}
		traceLines = append(traceLines, fmt.Sprintf("  #%d [%s] %s %s -> %d  <-- %s", step, role, action, target, status, msg))
		t.Errorf("testkit.Walk(seed=%d) failed at step %d: %s\ntrace:\n%s", seed, step, msg, strings.Join(traceLines, "\n"))
	}

	safeDo := func(fn func() *Response) (resp *Response, panicked any) {
		defer func() {
			if r := recover(); r != nil {
				panicked = r
			}
		}()
		return fn(), nil
	}

	inspectResponse := func(step int, role Role, action, target string, resp *Response, v *Visitor) {
		t.Helper()
		if resp == nil {
			return
		}
		report.Trace = append(report.Trace, WalkStep{Step: step, Role: role, Action: action, Target: target, Status: resp.Code})
		if resp.Code >= 500 {
			fail(step, role, action, target, resp.Code, fmt.Sprintf("%s %s returned %d", action, target, resp.Code))
			return
		}
		if role == Public {
			for r, secret := range roleSecrets {
				if strings.Contains(resp.Body, secret) {
					fail(step, role, action, target, resp.Code, fmt.Sprintf("public response for %s leaked %s marker %q", target, r, secret))
				}
			}
		}
		if resp.Code != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
			return
		}
		if opts.CheckLinks {
			cap := &walkTB{TB: t}
			links(cap, h, Page{Path: resp.URL.RequestURI(), Header: v.headers(resp.URL.RequestURI())})
			for _, e := range cap.errs {
				fail(step, role, action, target, resp.Code, e)
			}
		}
		resp.Find("a[href]").Each(func(_ int, s *goquery.Selection) {
			href, ok := s.Attr("href")
			if !ok {
				return
			}
			href = strings.TrimSpace(href)
			if href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(href, "mailto:") || strings.HasPrefix(href, "tel:") || strings.HasPrefix(href, "javascript:") {
				return
			}
			u, err := resp.URL.Parse(href)
			if err != nil || u.Host != resp.URL.Host {
				return
			}
			u.Fragment = ""
			next := u.RequestURI()
			if !seenPath[next] && len(knownPaths) < 200 {
				seenPath[next] = true
				knownPaths = append(knownPaths, next)
			}
		})
		resp.Find("form").Each(func(idx int, s *goquery.Selection) {
			actionAttr, _ := s.Attr("action")
			methodAttr, _ := s.Attr("method")
			if methodAttr == "" {
				methodAttr = "GET"
			}
			isDel := s.HasClass("delete") || strings.EqualFold(methodAttr, "DELETE")
			if isDel && !opts.AllowDelete {
				return
			}
			hasLock := s.Find("input[type=hidden][name$='[lock_version]']").Length() > 0
			key := resp.URL.RequestURI() + "|" + actionAttr + "|" + strings.ToUpper(methodAttr)
			if seenForm[key] || len(knownForms) >= 100 {
				return
			}
			seenForm[key] = true
			sel := fmt.Sprintf("form:nth-of-type(%d)", idx+1)
			if id, ok := s.Attr("id"); ok && id != "" {
				sel = "form#" + id
			} else if !isDel {
				sel = "form:not(.delete)"
			}
			knownForms = append(knownForms, walkForm{
				pagePath: resp.URL.RequestURI(),
				selector: sel,
				action:   actionAttr,
				method:   strings.ToUpper(methodAttr),
				isDelete: isDel,
				hasLock:  hasLock,
			})
		})
	}

	role := Public
	for step := 1; step <= steps; step++ {
		report.Steps = step
		if step <= len(startPaths) {
			path := startPaths[step-1]
			resp, pan := safeDo(func() *Response { return visitors[role].Get(path) })
			if pan != nil {
				fail(step, role, "GET", path, 0, fmt.Sprintf("panic: %v", pan))
				return report
			}
			inspectResponse(step, role, "GET", path, resp, visitors[role])
			continue
		}

		roll := rng.IntN(10)
		switch {
		case roll == 0 && len(availableRoles) > 1:
			role = availableRoles[rng.IntN(len(availableRoles))]
			path := knownPaths[rng.IntN(len(knownPaths))]
			resp, pan := safeDo(func() *Response { return visitors[role].Get(path) })
			if pan != nil {
				fail(step, role, "GET", path, 0, fmt.Sprintf("panic: %v", pan))
				return report
			}
			inspectResponse(step, role, "GET", path, resp, visitors[role])

		case roll <= 3 && len(knownForms) > 0:
			wf := knownForms[rng.IntN(len(knownForms))]
			v := visitors[role]
			page, pan := safeDo(func() *Response { return v.Get(wf.pagePath) })
			if pan != nil {
				fail(step, role, "GET", wf.pagePath, 0, fmt.Sprintf("panic: %v", pan))
				return report
			}
			if page.Code != http.StatusOK || page.Find(wf.selector).Length() == 0 {
				inspectResponse(step, role, "GET", wf.pagePath, page, v)
				continue
			}
			fields := fuzzFormFields(rng, page, wf, opts.FieldValue)
			resp, pan := safeDo(func() *Response { return page.Submit(wf.selector, fields) })
			if pan != nil {
				fail(step, role, "SUBMIT", wf.pagePath, 0, fmt.Sprintf("panic: %v", pan))
				return report
			}
			inspectResponse(step, role, "SUBMIT", wf.pagePath, resp, v)
			if wf.hasLock && !testedStale[wf.pagePath] && resp.Code >= 300 && resp.Code < 400 {
				testedStale[wf.pagePath] = true
				stale, pan := safeDo(func() *Response { return page.Submit(wf.selector, fields) })
				if pan != nil {
					fail(step, role, "STALE-SUBMIT", wf.pagePath, 0, fmt.Sprintf("panic: %v", pan))
					return report
				}
				report.Trace = append(report.Trace, WalkStep{Step: step, Role: role, Action: "STALE-SUBMIT", Target: wf.pagePath, Status: stale.Code})
				if stale.Code != http.StatusConflict {
					fail(step, role, "STALE-SUBMIT", wf.pagePath, stale.Code, fmt.Sprintf("stale lock_version submission returned %d, want 409", stale.Code))
				}
			}

		default:
			path := knownPaths[rng.IntN(len(knownPaths))]
			v := visitors[role]
			resp, pan := safeDo(func() *Response { return v.Get(path) })
			if pan != nil {
				fail(step, role, "GET", path, 0, fmt.Sprintf("panic: %v", pan))
				return report
			}
			inspectResponse(step, role, "GET", path, resp, v)
		}
	}
	return report
}

var walkSampleStrings = []string{
	"Hello World",
	"",
	"   ",
	"Café & <script>alert(1)</script>",
	"2026-11-24",
	"42",
	"-1",
	strings.Repeat("a", 200),
}

func fuzzFormFields(rng *rand.Rand, page *Response, wf walkForm, custom func(*rand.Rand, string, string, string) string) map[string]string {
	fields := map[string]string{}
	form := page.Find(wf.selector).First()
	form.Find("input, textarea").Each(func(_ int, s *goquery.Selection) {
		name, ok := s.Attr("name")
		if !ok || name == "" {
			return
		}
		typ, _ := s.Attr("type")
		typ = strings.ToLower(typ)
		if typ == "hidden" || typ == "submit" || typ == "button" || typ == "file" || typ == "checkbox" || typ == "radio" {
			return
		}
		if custom != nil {
			fields[name] = custom(rng, wf.action, name, typ)
			return
		}
		switch typ {
		case "number":
			fields[name] = fmt.Sprintf("%d", rng.IntN(20)-5)
		case "date":
			if rng.IntN(3) == 0 {
				fields[name] = "not-a-date"
			} else {
				fields[name] = "2026-11-24"
			}
		case "email":
			fields[name] = fmt.Sprintf("walk%d@example.com", rng.IntN(5))
		default:
			fields[name] = walkSampleStrings[rng.IntN(len(walkSampleStrings))]
		}
	})
	return fields
}
