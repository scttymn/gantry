// Package turbo is Turbo's side of the server (turbo-rails): stream actions
// that change parts of a page, answered to a form or broadcast live (the
// live package), and what a request from Turbo says about itself.
//
//	if turbo.Accepts(r) {
//		return turbo.Stream(w, r, turbo.Append("messages", views.Message(m)),
//			turbo.Update("count", views.Count(n)))
//	}
//	web.Redirect(w, r, "/messages")
//
// Turbo's HTTP rules are web's: web.Redirect answers a form with 303, and a
// form with errors is rendered at 422 (web.Render(w, r, 422, form)).
package turbo

import (
	"context"
	"html"
	"io"
	"net/http"
	"strings"

	"github.com/a-h/templ"
)

// ContentType is a stream's: what Turbo asks for, and what Stream answers.
const ContentType = "text/vnd.turbo-stream.html"

// Action is one Turbo Stream action, a <turbo-stream> element, and a
// templ.Component: draw it in a page, answer it with Stream, or broadcast
// it. The functions below make the usual ones; any other (a custom
// action) is an Action too.
type Action struct {
	Name    string          // "append", "replace", "remove", "refresh"...
	Target  string          // the element's id
	Targets string          // or a CSS selector, for every element it matches
	Method  string          // "morph", for replace and update (Turbo 8)
	Content templ.Component // what goes in its <template>; nil for none
	// RequestID is a refresh's: the page whose request made the change
	// (RequestID(r)) skips it, having its own answer.
	RequestID string
}

// Render writes the action's element.
func (a Action) Render(ctx context.Context, w io.Writer) error {
	var b strings.Builder
	b.WriteString(`<turbo-stream action="` + html.EscapeString(a.Name) + `"`)
	if a.Target != "" {
		b.WriteString(` target="` + html.EscapeString(a.Target) + `"`)
	}
	if a.Targets != "" {
		b.WriteString(` targets="` + html.EscapeString(a.Targets) + `"`)
	}
	if a.Method != "" {
		b.WriteString(` method="` + html.EscapeString(a.Method) + `"`)
	}
	if a.RequestID != "" {
		b.WriteString(` request-id="` + html.EscapeString(a.RequestID) + `"`)
	}
	b.WriteString(">")
	if _, err := io.WriteString(w, b.String()); err != nil {
		return err
	}
	if a.Content != nil {
		if _, err := io.WriteString(w, "<template>"); err != nil {
			return err
		}
		if err := a.Content.Render(ctx, w); err != nil {
			return err
		}
		if _, err := io.WriteString(w, "</template>"); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "</turbo-stream>")
	return err
}

// Append adds c to the end of the element with id target.
func Append(target string, c templ.Component) Action {
	return Action{Name: "append", Target: target, Content: c}
}

// Prepend adds c to the start of target.
func Prepend(target string, c templ.Component) Action {
	return Action{Name: "prepend", Target: target, Content: c}
}

// Replace swaps target, the element itself, for c.
func Replace(target string, c templ.Component) Action {
	return Action{Name: "replace", Target: target, Content: c}
}

// Update swaps target's contents for c, keeping the element.
func Update(target string, c templ.Component) Action {
	return Action{Name: "update", Target: target, Content: c}
}

// Before puts c just before target.
func Before(target string, c templ.Component) Action {
	return Action{Name: "before", Target: target, Content: c}
}

// After puts c just after target.
func After(target string, c templ.Component) Action {
	return Action{Name: "after", Target: target, Content: c}
}

// Remove takes target out of the page.
func Remove(target string) Action { return Action{Name: "remove", Target: target} }

// Refresh has the page fetch itself again (Turbo 8's page refresh), for a
// change too wide to send piece by piece.
func Refresh() Action { return Action{Name: "refresh"} }

// Stream answers the request with actions, which Turbo applies to the page.
func Stream(w http.ResponseWriter, r *http.Request, actions ...templ.Component) error {
	w.Header().Set("Content-Type", ContentType+"; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	for _, a := range actions {
		if err := a.Render(r.Context(), w); err != nil {
			return err
		}
	}
	return nil
}

// Accepts reports whether the request takes stream actions for an answer:
// a form Turbo submitted.
func Accepts(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), ContentType)
}

// RequestID is the id Turbo gave the request (X-Turbo-Request-Id), for a
// refresh it causes: Action{Name: "refresh", RequestID: turbo.RequestID(r)}.
func RequestID(r *http.Request) string { return r.Header.Get("X-Turbo-Request-Id") }

// Frame is the id of the <turbo-frame> the request is for, "" when it's for
// the whole page: a frame's answer can leave out the layout.
func Frame(r *http.Request) string { return r.Header.Get("Turbo-Frame") }
