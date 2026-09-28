// Package web is gantry's HTTP layer, over net/http's ServeMux. Handlers
// return an error, and one place turns errors into responses:
//
//	func (c Controller) Show(w http.ResponseWriter, r *http.Request) error {
//		post, err := c.Q.GetPost(r.Context(), web.ID(r, "id"))
//		if err != nil {
//			return err // no such row: the 404 page
//		}
//		return web.Render(w, r, http.StatusOK, show(post))
//	}
//
// Router.Handler wraps the routes in the middleware every app wants: gzip,
// panics recovered, security headers, cross-origin form protection, and
// method override for HTML forms.
package web

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"strings"
)

// Handler is a request handler that can fail. An error it returns before
// writing anything becomes the error page for its status (see StatusOf).
type Handler func(w http.ResponseWriter, r *http.Request) error

// ErrorPage writes the app's page for an error status. The router calls it
// only for a browser (WantsHTML); anything else gets the bare status.
type ErrorPage func(w http.ResponseWriter, r *http.Request, status int)

// Error carries the status a handler wants: web.Status(422, err).
type Error struct {
	Status int
	Err    error
}

func (e *Error) Error() string {
	if e.Err == nil {
		return http.StatusText(e.Status)
	}
	return e.Err.Error()
}

func (e *Error) Unwrap() error { return e.Err }

// Status is an error answered with status. err may be nil.
func Status(status int, err error) error { return &Error{Status: status, Err: err} }

// NotFound is the 404 page.
var NotFound = Status(http.StatusNotFound, nil)

// StatusOf is the status an error is answered with:
//   - an *Error's own status
//   - 404 for a row that doesn't exist (sql.ErrNoRows)
//   - 413 for a body over its limit
//   - 500 for anything else
func StatusOf(err error) int {
	var e *Error
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &e):
		return e.Status
	case errors.Is(err, sql.ErrNoRows):
		return http.StatusNotFound
	case errors.As(err, &tooLarge):
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusInternalServerError
}

// Router is the app's routes.
type Router struct {
	Log       *slog.Logger
	ErrorPage ErrorPage
	Cache     *PageCache // for Cached routes; nil: they aren't cached
	mux       *http.ServeMux
}

// NewRouter is an empty router. page may be nil: errors are then answered
// with their status text.
func NewRouter(log *slog.Logger, page ErrorPage) *Router {
	rt := &Router{Log: log, ErrorPage: page, mux: http.NewServeMux()}
	rt.mux.HandleFunc("GET /up", Up)
	return rt
}

// Handle routes pattern ("GET /posts/{id}", ServeMux's syntax) to h.
func (rt *Router) Handle(pattern string, h Handler) {
	rt.mux.Handle(pattern, rt.Wrap(h))
}

// Mount routes pattern to a plain http.Handler (files, a mounted package).
func (rt *Router) Mount(pattern string, h http.Handler) { rt.mux.Handle(pattern, h) }

// Wrap makes h an http.Handler, answering its error.
func (rt *Router) Wrap(h Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tw := &trackingWriter{ResponseWriter: w}
		if err := h(tw, r); err != nil {
			rt.Fail(tw, r, err)
		}
	})
}

// Fail answers err: its error page, unless the response has started (then
// it's only logged) or the client has gone.
func (rt *Router) Fail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, context.Canceled) && r.Context().Err() != nil {
		return
	}
	status := StatusOf(err)
	if status >= 500 {
		rt.Log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	}
	if tw, ok := w.(*trackingWriter); ok && tw.wrote {
		if status < 500 {
			rt.Log.Warn("error after the response started", "path", r.URL.Path, "err", err)
		}
		return
	}
	rt.Error(w, r, status)
}

// Error answers with status: the app's page for a browser, else the bare
// status.
func (rt *Router) Error(w http.ResponseWriter, r *http.Request, status int) {
	if rt.ErrorPage == nil || !WantsHTML(r) {
		w.WriteHeader(status)
		return
	}
	rt.ErrorPage(w, r, status)
}

// WantsHTML: a request for a page, not a file or data. An extension other
// than .html, or an Accept header naming neither HTML nor anything, isn't.
func WantsHTML(r *http.Request) bool {
	if ext := path.Ext(r.URL.Path); ext != "" && ext != ".html" {
		return false
	}
	accept := r.Header.Get("Accept")
	return accept == "" || strings.Contains(accept, "text/html") || strings.Contains(accept, "*/*")
}

// Handler is the routes inside gantry's middleware, with the error page for
// any path nothing else matches. Outermost first:
//   - compress: gzip for text
//   - Recover: a panic is the 500 page, logged
//   - Headers and cross-origin protection: a form posted from another site
//     is refused with 422
//   - MethodOverride: HTML forms can PUT, PATCH and DELETE, with bodies capped
func (rt *Router) Handler() http.Handler {
	rt.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { rt.Error(w, r, http.StatusNotFound) })
	var h http.Handler = rt.mux
	h = MethodOverride(h, DefaultBodyLimits)
	h = CrossOrigin(h, func(w http.ResponseWriter, r *http.Request) { rt.Error(w, r, http.StatusUnprocessableEntity) })
	h = Headers(h)
	h = rt.Recover(h)
	return compressHandler(h)
}

// ID is the path value name as an int64: 0 when it isn't one, which no row
// has, so a lookup finds nothing and the page is a 404.
func ID(r *http.Request, name string) int64 {
	id, _ := strconv.ParseInt(r.PathValue(name), 10, 64)
	return id
}

// Up answers a health check without touching the database.
func Up(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte("up\n"))
}

// trackingWriter notes whether the response has started, so an error after
// that isn't written into the middle of a page.
type trackingWriter struct {
	http.ResponseWriter
	wrote bool
}

func (w *trackingWriter) WriteHeader(code int) {
	w.wrote = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *trackingWriter) Write(b []byte) (int, error) {
	w.wrote = true
	return w.ResponseWriter.Write(b)
}

func (w *trackingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// IDText is an id for a path: "7".
func IDText(id int64) string { return strconv.FormatInt(id, 10) }
