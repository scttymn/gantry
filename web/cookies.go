package web

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/scttymn/gantry/sign"
)

// Secure reports whether cookies for this request should be Secure: when
// the visitor used https (Scheme: a trusted proxy's X-Forwarded-Proto, or
// TLS), as Rails' cookies are. Over plain http a browser drops a Secure
// cookie, so an app reached at http://<LAN address> couldn't sign anyone in.
func Secure(r *http.Request) bool { return Scheme(r) == "https" }

// SetCookie sets an HttpOnly, SameSite=Lax cookie for the whole site,
// Secure when the request was https. maxAge 0 is a cookie for the session.
func SetCookie(w http.ResponseWriter, r *http.Request, name, value string, maxAge time.Duration) {
	http.SetCookie(w, cookie(r, name, value, maxAge))
}

func cookie(r *http.Request, name, value string, maxAge time.Duration) *http.Cookie {
	return &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: int(maxAge / time.Second),
		HttpOnly: true, Secure: Secure(r), SameSite: http.SameSiteLaxMode}
}

// MaxCookie is the most a cookie may be, its name, value and attributes
// together: browsers keep no more (Rails' MAX_COOKIE_SIZE).
const MaxCookie = 4096

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
type Flash struct {
	Signer sign.Signer
	Log    *slog.Logger // for a message cut to fit; slog.Default() when nil
}

const flashCookie = "flash"

// Set keeps a message of kind ("notice" or "alert") for the next page. One
// too long for its cookie (MaxCookie) is cut to fit, ending "…", and a
// warning logged: Rails raises CookieOverflow, but a cut message beats a
// failed page.
func (f Flash) Set(w http.ResponseWriter, r *http.Request, kind, message string) {
	signed := func(m string) string { return f.Signer.Sign(flashCookie, kind+"|"+m) }
	fits := func(value string) bool { return len(cookie(r, flashCookie, value, time.Minute).String()) <= MaxCookie }
	value := signed(message)
	if !fits(value) {
		runes := []rune(message)
		keep, over := 0, len(runes) // runes[:keep] fits; runes[:over] doesn't
		for over-keep > 1 {
			if mid := (keep + over) / 2; fits(signed(string(runes[:mid]) + "…")) {
				keep = mid
			} else {
				over = mid
			}
		}
		value = signed(string(runes[:keep]) + "…")
		log := f.Log
		if log == nil {
			log = slog.Default()
		}
		log.Warn("the flash was cut to fit its cookie", "kind", kind, "runes", len(runes), "kept", keep)
	}
	SetCookie(w, r, flashCookie, value, time.Minute)
}

// Redirect sends the browser to "to", with a message for the page it lands
// on when message isn't empty; see web.Redirect for the status.
func (f Flash) Redirect(w http.ResponseWriter, r *http.Request, to, kind, message string) {
	if message != "" {
		f.Set(w, r, kind, message)
	}
	Redirect(w, r, to)
}

// Redirect sends the browser to "to": 302 from a GET or HEAD, and 303 See
// Other from a form (POST, and PATCH or DELETE through _method), which
// browsers follow with a GET, as Turbo needs.
func Redirect(w http.ResponseWriter, r *http.Request, to string) {
	status := http.StatusSeeOther
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		status = http.StatusFound
	}
	http.Redirect(w, r, to, status)
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
