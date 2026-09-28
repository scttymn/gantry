package web

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/scttymn/gantry/compress"
)

var compressHandler = compress.Handler

// BodyLimits caps what a request body can be, read before any handler.
type BodyLimits struct {
	Form      int64 // an urlencoded form, or anything else
	Multipart int64 // a form with files
	InMemory  int64 // how much of a multipart form is kept in memory; the rest goes to temporary files
}

// DefaultBodyLimits: 1 MB for a form, 25 MB for one with files (photos from
// a phone), 8 MB of which in memory.
var DefaultBodyLimits = BodyLimits{Form: 1 << 20, Multipart: 25 << 20, InMemory: 8 << 20}

// MethodOverride reads a POSTed form's _method field, so an HTML form can
// PUT, PATCH or DELETE. It parses the form to read it, so it's where the
// body's size is capped: over the limit is 413.
func MethodOverride(next http.Handler, limits BodyLimits) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			limit, parse := limits.Form, r.ParseForm
			if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
				limit, parse = limits.Multipart, func() error { return r.ParseMultipartForm(limits.InMemory) }
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			if err := parse(); err != nil {
				http.Error(w, "Too large", http.StatusRequestEntityTooLarge)
				return
			}
			if m := strings.ToUpper(r.PostFormValue("_method")); m == http.MethodPut || m == http.MethodPatch || m == http.MethodDelete {
				r.Method = m
			}
		}
		next.ServeHTTP(w, r)
	})
}

// Headers sets the headers every response gets (Rails' defaults).
func Headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-XSS-Protection", "0")
		h.Set("X-Permitted-Cross-Domain-Policies", "none")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

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
