package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/scttymn/gantry/sign"
)

func TestSecure(t *testing.T) {
	for host, want := range map[string]bool{
		"example.com": true, "example.com:443": true, "localhost": false, "localhost:8080": false,
		"app.localhost": false, "branch.app.localhost": false, "127.0.0.1:3000": false,
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Host = host
		if Secure(r) != want {
			t.Errorf("%s: %v", host, !want)
		}
	}
}

func TestFlash(t *testing.T) {
	f := Flash{Signer: sign.Signer{Key: []byte("k")}}
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "https://example.com/login", nil)
	f.Redirect(rec, r, "/login", "alert", "Try again | later.")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
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
