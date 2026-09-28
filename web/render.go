package web

import (
	"net/http"

	"github.com/a-h/templ"
)

// Render writes a templ component as the page, with status.
func Render(w http.ResponseWriter, r *http.Request, status int, c templ.Component) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	return c.Render(r.Context(), w)
}
