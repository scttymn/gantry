package web

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/scttymn/gantry/sign"
)

// Secure when the visitor used https: over TLS, or a trusted proxy's
// X-Forwarded-Proto; plain http (a LAN address, localhost) isn't.
func TestSecure(t *testing.T) {
	for url, want := range map[string]bool{
		"https://example.com/": true, "http://example.com/": false, "http://192.0.2.10:3000/": false,
		"http://localhost:8080/": false, "http://app.localhost/": false,
	} {
		if got := Secure(httptest.NewRequest("GET", url, nil)); got != want {
			t.Errorf("%s: %v", url, got)
		}
	}
	r := httptest.NewRequest("GET", "http://example.com/", nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	if Secure(r) {
		t.Error("an untrusted proxy's https")
	}
	r.RemoteAddr = "10.0.0.2:4000" // a private proxy: trusted by default
	if !Secure(r) {
		t.Error("a trusted proxy's https")
	}
}

func TestFlash(t *testing.T) {
	f := Flash{Signer: sign.Signer{Key: []byte("k")}}
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "https://example.com/login", nil)
	f.Redirect(rec, r, "/login", "alert", "Try again | later.")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	cookie := rec.Result().Cookies()[0]
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.MaxAge != 60 {
		t.Errorf("%+v", cookie)
	}
	next := httptest.NewRequest("GET", "https://example.com/login", nil)
	next.AddCookie(cookie)
	shown := httptest.NewRecorder()
	kind, msg, ok := f.Take(shown, next)
	if !ok || kind != "alert" || msg != "Try again | later." {
		t.Fatalf("%q %q %v", kind, msg, ok)
	}
	if c := shown.Result().Cookies(); len(c) != 1 || c[0].MaxAge != -1 {
		t.Error("not cleared once shown")
	}
	t.Run("a forged message isn't shown", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/", nil)
		r.AddCookie(&http.Cookie{Name: "flash", Value: "YWxlcnR8aGk--forged"})
		if _, _, ok := f.Take(httptest.NewRecorder(), r); ok {
			t.Error("shown")
		}
	})
}

func TestRedirect(t *testing.T) {
	for method, want := range map[string]int{"GET": 302, "HEAD": 302, "POST": 303, "PATCH": 303, "DELETE": 303} {
		w := httptest.NewRecorder()
		Redirect(w, httptest.NewRequest(method, "/posts/1", nil), "/posts")
		if w.Code != want || w.Header().Get("Location") != "/posts" {
			t.Errorf("%s: %d %s", method, w.Code, w.Header().Get("Location"))
		}
	}
}

// A message too long for its cookie is cut to fit, not left for the
// browser to drop.
func TestFlashTooLong(t *testing.T) {
	var logged bytes.Buffer
	f := Flash{Signer: sign.Signer{Key: []byte("k")}, Log: slog.New(slog.NewTextHandler(&logged, nil))}
	message := strings.Repeat("é", 5000) // two bytes each
	r := httptest.NewRequest("POST", "https://example.com/", nil)
	w := httptest.NewRecorder()
	f.Set(w, r, "notice", message)
	c := w.Result().Cookies()[0]
	if n := len(w.Header().Get("Set-Cookie")); n > 4096 {
		t.Fatalf("the cookie is %d bytes", n)
	}
	next := httptest.NewRequest("GET", "/", nil)
	next.AddCookie(c)
	_, got, ok := f.Take(httptest.NewRecorder(), next)
	kept := strings.TrimSuffix(got, "…")
	if !ok || kept == got || !strings.HasPrefix(message, kept) {
		t.Fatalf("%v %q", ok, got)
	}
	// As much as fits: one more letter wouldn't.
	more := httptest.NewRecorder()
	SetCookie(more, r, "flash", f.Signer.Sign("flash", "notice|"+kept+"é…"), time.Minute)
	if n := len(more.Header().Get("Set-Cookie")); n <= MaxCookie {
		t.Errorf("cut to %d letters, but %d would fit (%d bytes)", len([]rune(kept)), len([]rune(kept))+1, n)
	}
	if !strings.Contains(logged.String(), "the flash was cut") || !strings.Contains(logged.String(), "runes=5000") {
		t.Errorf("logged: %s", logged.String())
	}
	// A message that fits isn't touched, and nothing's logged.
	logged.Reset()
	f.Set(httptest.NewRecorder(), r, "notice", strings.Repeat("é", 1000))
	if logged.Len() != 0 {
		t.Errorf("logged: %s", logged.String())
	}
}
