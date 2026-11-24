package web

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/scttymn/gantry/compress"
)

var compressHandler = compress.Handler

// BodyLimits caps what a request body can be.
type BodyLimits struct {
	// an urlencoded form, read before any handler
	Form int64
	// a form with files, read before any handler
	Multipart int64
	// how much of a multipart form is kept in memory; the rest goes to temporary files
	InMemory int64
	// Data is any other body (JSON, a webhook's payload): read by its
	// handler, with a tighter limit of its own (ReadJSONLimit); this is the
	// most any handler can read.
	Data int64
}

// DefaultBodyLimits: 1 MB for a form, 25 MB for one with files (photos from
// a phone), 8 MB of which in memory, and 25 MB for anything else.
var DefaultBodyLimits = BodyLimits{Form: 1 << 20, Multipart: 25 << 20, InMemory: 8 << 20, Data: 25 << 20}

// MethodOverride reads a POSTed form's _method field, so an HTML form can
// PUT, PATCH or DELETE. It parses the form to read it, so it's where a
// form's size is capped: over the limit is 413. Any other body is capped
// at limits.Data for its handler to read. A body that keeps arriving at a
// phone's pace isn't cut off by the server's ReadTimeout; one that stops,
// or trickles, is: a form that doesn't arrive in time is 408, and one that
// isn't a form at all 400.
func MethodOverride(next http.Handler, limits BodyLimits) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if read, write := timeouts(r); r.Body != nil && r.Body != http.NoBody && (read > 0 || write > 0) {
			r.Body = &movingBody{ReadCloser: r.Body, rc: http.NewResponseController(w),
				read: pace{start: time.Now(), timeout: read}, write: write}
		}
		mediaType, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";")
		mediaType = strings.TrimSpace(strings.ToLower(mediaType))
		form := mediaType == "application/x-www-form-urlencoded" || mediaType == "multipart/form-data"
		if r.Method == http.MethodPost && form {
			limit, parse := limits.Form, r.ParseForm
			if mediaType == "multipart/form-data" {
				limit, parse = limits.Multipart, func() error { return r.ParseMultipartForm(limits.InMemory) }
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			if err := parse(); err != nil {
				var tooLarge *http.MaxBytesError
				switch {
				case errors.As(err, &tooLarge):
					http.Error(w, "Too large", http.StatusRequestEntityTooLarge)
				case errors.Is(err, os.ErrDeadlineExceeded):
					http.Error(w, "Request timeout", http.StatusRequestTimeout)
				default:
					http.Error(w, "Bad request", http.StatusBadRequest)
				}
				return
			}
			if m := strings.ToUpper(r.PostFormValue("_method")); m == http.MethodPut || m == http.MethodPatch || m == http.MethodDelete {
				r.Method = m
			}
		} else if r.Body != nil && r.Body != http.NoBody {
			r.Body = http.MaxBytesReader(w, r.Body, limits.Data)
		}
		next.ServeHTTP(w, r)
	})
}

// Security is what Headers sends beyond Rails' defaults: each is off when
// it's zero.
type Security struct {
	// HSTS is Strict-Transport-Security's max-age (Rails' force_ssl), sent
	// on a request made over https (Scheme): a browser that's seen it goes
	// to the site over https alone until it lapses, so a network along the
	// way can't serve it a plain http page first. Turn it on only for a
	// site that will serve https for that long. A year is the usual.
	HSTS time.Duration
	// CSP is the Content-Security-Policy, which says where a page may load
	// scripts, styles and the rest from, so one an attacker slips into it
	// doesn't run. ContentSecurityPolicy is one for the app's own assets.
	CSP string
	// CSPReportOnly sends CSP as Content-Security-Policy-Report-Only: the
	// browser reports in its console what the policy would block, and
	// blocks nothing. For trying a policy before enforcing it.
	CSPReportOnly bool
}

// Headers sets the headers every response gets (Rails' defaults).
func Headers(next http.Handler) http.Handler { return Security{}.Headers(next) }

// Headers sets the headers every response gets (Rails' defaults), and s's.
func (s Security) Headers(next http.Handler) http.Handler {
	var hsts string
	if age := int64(s.HSTS / time.Second); age > 0 {
		hsts = fmt.Sprintf("max-age=%d", age)
	}
	csp := s.cspHeader()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-XSS-Protection", "0")
		h.Set("X-Permitted-Cross-Domain-Policies", "none")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		if hsts != "" && Scheme(r) == "https" {
			h.Set("Strict-Transport-Security", hsts)
		}
		if s.CSP != "" {
			h.Set(csp, s.CSP)
		}
		next.ServeHTTP(w, r)
	})
}

func (s Security) cspHeader() string {
	if s.CSPReportOnly {
		return "Content-Security-Policy-Report-Only"
	}
	return "Content-Security-Policy"
}

const staticPagePolicy = "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; object-src 'none'; base-uri 'self'; frame-ancestors 'self'"

// ContentSecurityPolicy is a policy for an app's pages: everything from the
// app itself, and inline only the scripts and styles named by hash, which
// are the app's own (scripts: assets.ImportMap.Hashes(); styles:
// assets.Styles.Hash()) and Turbo's progress bar's. Images may also be
// data: URIs (pictures' placeholders); nothing may be a plugin, move the
// page's base URL, or frame the page from another site. A style="…"
// attribute or an inline script of a page's own is blocked: move it to the
// app's stylesheet or a Stimulus controller. Add to it for anything the app
// loads from elsewhere.
func ContentSecurityPolicy(scripts, styles string) string {
	return "default-src 'self'; script-src " + sources("'self'", scripts) + "; style-src " + sources("'self'", styles, turboProgressBar) +
		"; img-src 'self' data:; object-src 'none'; base-uri 'self'; frame-ancestors 'self'"
}

func sources(lists ...string) string {
	return strings.Join(strings.Fields(strings.Join(lists, " ")), " ")
}

const turboProgressBar = "'sha256-WAyOw4V+FqDc35lQPyRADLBWbuNK8ahvYEaQIYF1+Ps='"

// CrossOrigin refuses a state-changing request sent from another site (the
// browser says so in Sec-Fetch-Site or Origin), which is what a CSRF token
// guards against, with no token. deny answers the refusal. bypass lists
// patterns that take cross-site posts on purpose (Sign in with Apple's
// callback); they must check their own proof instead.
func CrossOrigin(next http.Handler, deny http.HandlerFunc, bypass ...string) http.Handler {
	cop := http.NewCrossOriginProtection()
	cop.SetDenyHandler(deny)
	for _, p := range bypass {
		cop.AddInsecureBypassPattern(p)
	}
	return cop.Handler(next)
}

// Recover turns a panic into the 500 page, logged.
func (rt *Router) Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				rt.Log.Error("panic", "method", r.Method, "path", r.URL.Path, "err", fmt.Sprint(v))
				rt.Error(w, r, http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
