package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"slices"
	"strings"
)

// PublicFiles is an app's public folder (assets.All): the router reads its
// error pages there, 404.html and the rest, as Rails' public/.
type PublicFiles interface {
	Public(name string) ([]byte, bool)
}

// Invalid is what's wrong with a form or a record, field by field (Rails'
// errors): a handler returns it to answer 422, and a client asking for JSON
// gets {"errors": {"name": ["can't be blank"]}}.
type Invalid map[string][]string

func (e Invalid) Error() string {
	var parts []string
	for _, field := range slices.Sorted(func(yield func(string) bool) {
		for f := range e {
			if !yield(f) {
				return
			}
		}
	}) {
		parts = append(parts, field+" "+strings.Join(e[field], ", "))
	}
	return strings.Join(parts, "; ")
}

// WantsJSON: a request for data, not a page: it went through AcceptJSON,
// it accepts JSON, or its path ends in .json.
func WantsJSON(r *http.Request) bool {
	if api, _ := Get(r, apiKey); api || path.Ext(r.URL.Path) == ".json" {
		return true
	}
	accept := r.Header.Get("Accept")
	return strings.Contains(accept, "application/json") || strings.Contains(accept, "+json")
}

var apiKey = NewKey[bool]("api")

// AcceptJSON is a filter for an API's routes: whatever the client's Accept
// says (a CLI often sends none), they're answered as data, errors included.
//
//	rt.Scope("/api/v1", web.Pipeline{web.AcceptJSON, tokens.Require}, ...)
func AcceptJSON(w http.ResponseWriter, r *http.Request) error {
	Set(r, apiKey, true)
	return nil
}

// answer answers status, for err (which may be nil):
//   - to a client asking for JSON: {"error": ...}, the handler's own message
//     below 500 (Status(409, err)), else the status's name; a 422 from
//     Invalid gives its fields' errors
//   - to a browser: the app's ErrorPage, if it has one and it doesn't fail;
//     else its public <status>.html; else the router's own page
//   - to anything else (an image, a script): the bare status
func (rt *Router) answer(w http.ResponseWriter, r *http.Request, status int, err error) {
	switch {
	case WantsJSON(r):
		var invalid Invalid
		if errors.As(err, &invalid) && status == http.StatusUnprocessableEntity {
			JSON(w, status, map[string]Invalid{"errors": invalid})
			return
		}
		msg := http.StatusText(status)
		var e *Error
		if status < 500 && errors.As(err, &e) && e.Err != nil {
			msg = e.Err.Error()
		}
		JSON(w, status, map[string]string{"error": msg})
	case !WantsHTML(r):
		w.WriteHeader(status)
	case rt.ErrorPage != nil && rt.appPage(w, r, status):
	default:
		rt.staticPage(w, status)
	}
}

// appPage draws the app's error page, and reports whether it did: a page
// that panics (its database is down, say) before writing is logged, and the
// static page is served instead.
func (rt *Router) appPage(w http.ResponseWriter, r *http.Request, status int) (drawn bool) {
	tw := &trackingWriter{ResponseWriter: w}
	defer func() {
		if v := recover(); v != nil {
			rt.Log.Error("the error page failed", "status", status, "path", r.URL.Path, "err", fmt.Sprint(v))
			drawn = tw.wrote
		}
	}()
	rt.ErrorPage(tw, r, status)
	return true
}

// staticPage is the app's public <status>.html, else the router's own page.
func (rt *Router) staticPage(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if rt.Public != nil {
		if page, ok := rt.Public.Public(fmt.Sprintf("%d.html", status)); ok {
			w.WriteHeader(status)
			w.Write(page)
			return
		}
	}
	w.WriteHeader(status)
	text := http.StatusText(status)
	fmt.Fprintf(w, "<!doctype html>\n<meta charset=\"utf-8\">\n<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">\n<title>%d %s</title>\n<h1>%s</h1>\n", status, text, text)
}

// JSON writes v as a JSON response with status (Rails' render json:).
func JSON(w http.ResponseWriter, status int, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, err = w.Write(append(b, '\n'))
	return err
}

// ReadJSON reads the request's JSON body into v, a struct, at most 1 MB:
// ReadJSONLimit's default.
func ReadJSON(r *http.Request, v any) error { return ReadJSONLimit(r, v, 1<<20) }

// ReadJSONLimit reads the request's JSON body into v, a struct; fields it
// doesn't have are ignored, as Rails ignores parameters it didn't permit. A
// body over limit bytes is a 413, and one that isn't JSON a 400, each
// answered as the handler's error.
func ReadJSONLimit(r *http.Request, v any, limit int64) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return Status(http.StatusBadRequest, fmt.Errorf("the request's body couldn't be read: %w", err))
	}
	if int64(len(body)) > limit {
		return Status(http.StatusRequestEntityTooLarge, fmt.Errorf("the request is larger than %d bytes", limit))
	}
	if len(body) == 0 {
		return Status(http.StatusBadRequest, errors.New("the request has no JSON"))
	}
	if err := json.Unmarshal(body, v); err != nil {
		return Status(http.StatusBadRequest, fmt.Errorf("the request isn't JSON: %v", err))
	}
	return nil
}
