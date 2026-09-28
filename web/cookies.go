package web

import (
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/scttymn/gantry/sign"
)

// Secure reports whether cookies for this request should be Secure: always,
// but on a local development host, which browsers don't all treat as secure
// over plain http (localhost, *.localhost, 127.0.0.1).
func Secure(r *http.Request) bool {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return !(host == "localhost" || strings.HasSuffix(host, ".localhost") || host == "127.0.0.1" || host == "::1")
}

// SetCookie sets an HttpOnly, SameSite=Lax cookie for the whole site,
// Secure unless the host is local. maxAge 0 is a cookie for the session.
func SetCookie(w http.ResponseWriter, r *http.Request, name, value string, maxAge time.Duration) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: int(maxAge / time.Second),
		HttpOnly: true, Secure: Secure(r), SameSite: http.SameSiteLaxMode})
}

// ClearCookie removes a cookie SetCookie set.
func ClearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1})
}

// SignedCookie is a cookie's value, if it was signed for its name.
func SignedCookie(r *http.Request, s sign.Signer, name string) (string, bool) {
	c, err := r.Cookie(name)
	if err != nil {
		return "", false
	}
	return s.Verify(name, c.Value)
}

// Flash is a one-page message, Rails' flash: set before a redirect, shown
// by the next page, and gone after. It lives in a signed cookie for a
// minute, so there's nothing to keep on the server.
type Flash struct{ Signer sign.Signer }

const flashCookie = "flash"

// Set keeps a message of kind ("notice" or "alert") for the next page.
func (f Flash) Set(w http.ResponseWriter, r *http.Request, kind, message string) {
	SetCookie(w, r, flashCookie, f.Signer.Sign(flashCookie, kind+"|"+message), time.Minute)
}

// Redirect sends the browser to "to" (302), with a message for the page it
// lands on when message isn't empty.
func (f Flash) Redirect(w http.ResponseWriter, r *http.Request, to, kind, message string) {
	if message != "" {
		f.Set(w, r, kind, message)
	}
	http.Redirect(w, r, to, http.StatusFound)
}

// Take is the message for this page, if there is one, and clears it.
func (f Flash) Take(w http.ResponseWriter, r *http.Request) (kind, message string, ok bool) {
	value, ok := SignedCookie(r, f.Signer, flashCookie)
	if !ok {
		return "", "", false
	}
	ClearCookie(w, flashCookie)
	kind, message, _ = strings.Cut(value, "|")
	return kind, message, true
}
