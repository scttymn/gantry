package auth

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/mail"
	"github.com/scttymn/gantry/sign"
	"github.com/scttymn/gantry/testkit"
	"github.com/scttymn/gantry/token"
	"github.com/scttymn/gantry/web"
)

// The tables as an app's migration makes them (gantry g auth, later).
var schema = fstest.MapFS{"00001_auth.sql": {Data: []byte(`-- +goose Up
CREATE TABLE users (id integer PRIMARY KEY, email_address text NOT NULL UNIQUE, password_digest text NOT NULL,
  created_at datetime NOT NULL, updated_at datetime NOT NULL);
CREATE TABLE sessions (id integer PRIMARY KEY, user_id integer NOT NULL REFERENCES users (id), token_digest text NOT NULL UNIQUE,
  ip_address text NOT NULL DEFAULT '', user_agent text NOT NULL DEFAULT '', created_at datetime NOT NULL, last_seen_at datetime NOT NULL);
`)}}

type fakeMail struct{ sent []mail.Message }

func (f *fakeMail) Send(_ context.Context, m mail.Message) error {
	f.sent = append(f.sent, m)
	return nil
}

type app struct {
	t    *testing.T
	auth *Auth
	h    http.Handler
	mail *fakeMail
	now  time.Time
	user User
	jar  []*http.Cookie
}

const password = "correct horse"

func init() { bcryptCost = bcrypt.MinCost }

func newApp(t *testing.T) *app {
	t.Helper()
	d := testkit.DB(t, func(ctx context.Context, d *db.DB) error { return d.Migrate(ctx, schema, "app_migrations") })
	a := &app{t: t, mail: &fakeMail{}, now: time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)}
	a.auth = &Auth{DB: d, Signer: sign.Signer{Key: []byte("test key")}, Mail: a.mail, From: "Site <no-reply@example.com>",
		URL: func(path string) string { return "https://example.com" + path }, Limits: &web.Limits{},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return a.now },
		Paths: Paths{AfterLogin: "/admin"}}
	u, err := a.auth.CreateUser(context.Background(), " One@Example.com ", password)
	if err != nil {
		t.Fatal(err)
	}
	a.user = u
	rt := web.NewRouter(slog.New(slog.DiscardHandler), nil)
	a.auth.Routes(rt)
	rt.Mount("GET /admin", a.auth.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _ := Current(r)
		io.WriteString(w, "hello "+u.EmailAddress)
	})))
	a.h = rt.Handler()
	return a
}

// do sends a request with the jar's cookies (as a browser would, same site),
// and keeps what the response sets.
func (a *app) do(method, path string, form url.Values) *httptest.ResponseRecorder {
	a.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, "https://example.com"+path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.RemoteAddr = "203.0.113.9:4000"
	for _, c := range a.jar {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	for _, c := range rec.Result().Cookies() {
		kept := a.jar[:0]
		for _, old := range a.jar {
			if old.Name != c.Name {
				kept = append(kept, old)
			}
		}
		a.jar = kept
		if c.MaxAge >= 0 {
			a.jar = append(a.jar, c)
		}
	}
	return rec
}

func (a *app) signIn(email, pw string) *httptest.ResponseRecorder {
	return a.do("POST", "/login", url.Values{"email_address": {email}, "password": {pw}})
}

func (a *app) cookie(name string) *http.Cookie {
	for _, c := range a.jar {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func (a *app) count(table string) int {
	var n int
	a.auth.DB.Read.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n)
	return n
}

func TestSessions(t *testing.T) {
	t.Run("new", func(t *testing.T) {
		rec := newApp(t).do("GET", "/login", nil)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `name="email_address"`) {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
	})
	t.Run("create with valid credentials", func(t *testing.T) {
		a := newApp(t)
		rec := a.signIn("one@example.com", password)
		if rec.Code != 302 || rec.Header().Get("Location") != "/admin" || a.cookie("session") == nil {
			t.Fatalf("%d %v", rec.Code, rec.Header())
		}
		if got := a.do("GET", "/admin", nil).Body.String(); got != "hello one@example.com" {
			t.Errorf("%q", got)
		}
	})
	t.Run("create with invalid credentials", func(t *testing.T) {
		a := newApp(t)
		for _, creds := range [][2]string{{"one@example.com", "wrong"}, {"nobody@example.com", password}} {
			rec := a.signIn(creds[0], creds[1])
			if rec.Code != 302 || rec.Header().Get("Location") != "/login" || a.cookie("session") != nil {
				t.Fatalf("%v: %d %v", creds, rec.Code, rec.Header())
			}
			if body := a.do("GET", "/login", nil).Body.String(); !strings.Contains(body, "Try another email address or password.") {
				t.Errorf("no alert: %s", body)
			}
		}
	})
	t.Run("an email is matched however it's typed", func(t *testing.T) {
		a := newApp(t)
		if rec := a.signIn("  ONE@example.COM ", password); rec.Header().Get("Location") != "/admin" {
			t.Fatalf("%v", rec.Header())
		}
	})
	t.Run("destroy", func(t *testing.T) {
		a := newApp(t)
		a.signIn("one@example.com", password)
		rec := a.do("POST", "/logout", url.Values{})
		if rec.Code != 303 || rec.Header().Get("Location") != "/login" || a.cookie("session") != nil || a.count("sessions") != 0 {
			t.Fatalf("%d %v sessions %d", rec.Code, rec.Header(), a.count("sessions"))
		}
	})
	t.Run("the session cookie is a random token, HttpOnly, Lax, lasting and Secure", func(t *testing.T) {
		a := newApp(t)
		a.signIn("one@example.com", password)
		c := a.cookie("session")
		if c == nil || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.MaxAge < 10*365*24*3600 || len(c.Value) < 40 {
			t.Fatalf("%+v", c)
		}
		var stored string
		a.auth.DB.Read.QueryRow(`SELECT token_digest FROM sessions`).Scan(&stored)
		if stored == c.Value || stored != token.Digest(c.Value) {
			t.Error("the table holds the token itself, not its digest")
		}
	})
	t.Run("a tampered cookie or a session ended elsewhere signs nobody in", func(t *testing.T) {
		a := newApp(t)
		a.signIn("one@example.com", password)
		a.cookie("session").Value += "x"
		if rec := a.do("GET", "/admin", nil); rec.Code != 302 {
			t.Error("a tampered cookie got in")
		}
		a.jar = nil
		a.signIn("one@example.com", password)
		a.auth.DB.Write.Exec(`DELETE FROM sessions`)
		if rec := a.do("GET", "/admin", nil); rec.Code != 302 {
			t.Error("an ended session got in")
		}
	})
	t.Run("an eleventh try in three minutes is turned away", func(t *testing.T) {
		a := newApp(t)
		for range 10 {
			a.signIn("one@example.com", "wrong")
		}
		if rec := a.signIn("one@example.com", password); a.cookie("session") != nil || rec.Header().Get("Location") != "/login" {
			t.Fatal("signed in past the limit")
		}
		if body := a.do("GET", "/login", nil).Body.String(); !strings.Contains(body, "Try again later.") {
			t.Error(body)
		}
		a.now = a.now.Add(3 * time.Minute)
		if a.signIn("one@example.com", password); a.cookie("session") == nil {
			t.Error("still turned away after three minutes")
		}
	})
	t.Run("a session's last sight is written at most hourly", func(t *testing.T) {
		a := newApp(t)
		a.signIn("one@example.com", password)
		seen := func() time.Time {
			var at time.Time
			a.auth.DB.Read.QueryRow(`SELECT last_seen_at FROM sessions`).Scan(&at)
			return at
		}
		start := seen()
		a.now = a.now.Add(30 * time.Minute)
		a.do("GET", "/admin", nil)
		if !seen().Equal(start) {
			t.Error("written within the hour")
		}
		a.now = a.now.Add(31 * time.Minute)
		a.do("GET", "/admin", nil)
		if !seen().Equal(a.now) {
			t.Errorf("not written after an hour: %v", seen())
		}
	})
}

// resetLink is the path in the last reset email.
func (a *app) resetLink() string {
	a.t.Helper()
	if len(a.mail.sent) == 0 {
		a.t.Fatal("no email")
	}
	m := regexp.MustCompile(`https://example\.com(/passwords/\S+/edit)`).FindStringSubmatch(a.mail.sent[len(a.mail.sent)-1].Text)
	if m == nil {
		a.t.Fatalf("no link in %q", a.mail.sent[len(a.mail.sent)-1].Text)
	}
	return m[1]
}

func TestPasswords(t *testing.T) {
	t.Run("new", func(t *testing.T) {
		if rec := newApp(t).do("GET", "/passwords/new", nil); rec.Code != 200 {
			t.Fatal(rec.Code)
		}
	})
	t.Run("create", func(t *testing.T) {
		a := newApp(t)
		rec := a.do("POST", "/passwords", url.Values{"email_address": {"one@example.com"}})
		if rec.Code != 302 || rec.Header().Get("Location") != "/login" || len(a.mail.sent) != 1 {
			t.Fatalf("%d %v sent %d", rec.Code, rec.Header(), len(a.mail.sent))
		}
		m := a.mail.sent[0]
		if m.To != "one@example.com" || m.Subject != "Reset your password" || m.From != "Site <no-reply@example.com>" || !strings.Contains(m.Text, "15 minutes") {
			t.Errorf("%+v", m)
		}
	})
	t.Run("create for an unknown user redirects but sends no mail", func(t *testing.T) {
		a := newApp(t)
		rec := a.do("POST", "/passwords", url.Values{"email_address": {"nobody@example.com"}})
		if rec.Header().Get("Location") != "/login" || len(a.mail.sent) != 0 {
			t.Fatal("told apart from a real address")
		}
	})
	t.Run("edit", func(t *testing.T) {
		a := newApp(t)
		a.do("POST", "/passwords", url.Values{"email_address": {"one@example.com"}})
		if rec := a.do("GET", a.resetLink(), nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), `name="password_confirmation"`) {
			t.Fatalf("%d", rec.Code)
		}
	})
	t.Run("edit with invalid password reset token", func(t *testing.T) {
		rec := newApp(t).do("GET", "/passwords/forged--token/edit", nil)
		if rec.Code != 302 || rec.Header().Get("Location") != "/passwords/new" {
			t.Fatalf("%d %v", rec.Code, rec.Header())
		}
	})
	update := func(a *app, link, pw, confirm string) *httptest.ResponseRecorder {
		return a.do("PATCH", strings.TrimSuffix(link, "/edit"), url.Values{"password": {pw}, "password_confirmation": {confirm}})
	}
	t.Run("update", func(t *testing.T) {
		a := newApp(t)
		a.do("POST", "/passwords", url.Values{"email_address": {"one@example.com"}})
		rec := update(a, a.resetLink(), "a new password", "a new password")
		if rec.Code != 302 || rec.Header().Get("Location") != "/login" {
			t.Fatalf("%d %v", rec.Code, rec.Header())
		}
		if _, ok := a.auth.Authenticate(context.Background(), "one@example.com", "a new password"); !ok {
			t.Error("the new password doesn't work")
		}
	})
	t.Run("update with non matching passwords", func(t *testing.T) {
		a := newApp(t)
		a.do("POST", "/passwords", url.Values{"email_address": {"one@example.com"}})
		link := a.resetLink()
		rec := update(a, link, "a new password", "another one")
		if rec.Header().Get("Location") != link {
			t.Fatalf("%v", rec.Header())
		}
		if body := a.do("GET", link, nil).Body.String(); !strings.Contains(body, "Password confirmation doesn&#39;t match Password") {
			t.Error(body)
		}
	})
	t.Run("update with a password that breaks the rules says which rule", func(t *testing.T) {
		a := newApp(t)
		a.auth.Rules = func(pw, confirm string) []string {
			if !strings.ContainsAny(pw, "0123456789") {
				return []string{"Password must include a number"}
			}
			return nil
		}
		a.do("POST", "/passwords", url.Values{"email_address": {"one@example.com"}})
		link := a.resetLink()
		update(a, link, "no digits here", "no digits here")
		if body := a.do("GET", link, nil).Body.String(); !strings.Contains(body, "Password must include a number") {
			t.Error(body)
		}
	})
	t.Run("a link lasts fifteen minutes, and dies once used", func(t *testing.T) {
		a := newApp(t)
		a.do("POST", "/passwords", url.Values{"email_address": {"one@example.com"}})
		link := a.resetLink()
		a.now = a.now.Add(15*time.Minute + time.Second)
		if rec := a.do("GET", link, nil); rec.Code != 302 {
			t.Error("an expired link worked")
		}
		a.now = a.now.Add(-time.Minute)
		update(a, link, "a new password", "a new password")
		if rec := a.do("GET", link, nil); rec.Code != 302 {
			t.Error("a used link worked")
		}
	})
	t.Run("a reset signs out every session", func(t *testing.T) {
		a := newApp(t)
		a.signIn("one@example.com", password)
		a.do("POST", "/passwords", url.Values{"email_address": {"one@example.com"}})
		update(a, a.resetLink(), "a new password", "a new password")
		if a.count("sessions") != 0 || a.do("GET", "/admin", nil).Code != 302 {
			t.Error("a session outlived the reset")
		}
	})
	t.Run("the link uses the site's address, whatever Host asked", func(t *testing.T) {
		a := newApp(t)
		req := httptest.NewRequest("POST", "https://evil.example/passwords", strings.NewReader("email_address=one%40example.com"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		a.h.ServeHTTP(httptest.NewRecorder(), req)
		if len(a.mail.sent) != 1 || !strings.Contains(a.mail.sent[0].Text, "https://example.com/passwords/") || strings.Contains(a.mail.sent[0].Text, "evil") {
			t.Fatalf("%+v", a.mail.sent)
		}
	})
}

func TestRequire(t *testing.T) {
	t.Run("sends you to sign in, and back after", func(t *testing.T) {
		a := newApp(t)
		rec := a.do("GET", "/admin?tab=2", nil)
		if rec.Code != 302 || rec.Header().Get("Location") != "/login" {
			t.Fatalf("%d %v", rec.Code, rec.Header())
		}
		if rec := a.signIn("one@example.com", password); rec.Header().Get("Location") != "/admin?tab=2" {
			t.Fatalf("%v", rec.Header())
		}
	})
	t.Run("an off-site return address is refused", func(t *testing.T) {
		a := newApp(t)
		for _, evil := range []string{"//evil.example/", "https://evil.example/", "/\\evil.example"} {
			a.jar = []*http.Cookie{{Name: "return_to", Value: a.auth.Signer.Sign("return_to", evil)}}
			if rec := a.signIn("one@example.com", password); rec.Header().Get("Location") != "/admin" {
				t.Errorf("%s: %v", evil, rec.Header())
			}
		}
	})
	t.Run("Current is only set behind Require", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/", nil)
		if _, ok := Current(r); ok {
			t.Error("a user on a bare request")
		}
	})
	t.Run("SignedIn reads the session without requiring one", func(t *testing.T) {
		a := newApp(t)
		req := httptest.NewRequest("GET", "/login", nil)
		if a.auth.SignedIn(req) {
			t.Error("signed in with no cookie")
		}
		a.signIn("one@example.com", password)
		req.AddCookie(a.cookie("session"))
		if !a.auth.SignedIn(req) {
			t.Error("not signed in with a live session")
		}
	})
}

func TestPaths(t *testing.T) {
	t.Run("the pages move where the app says, forms and all", func(t *testing.T) {
		a := newApp(t)
		a.auth.Paths = Paths{Login: "/session/new", Logout: "/session", Passwords: "/reset", AfterLogin: "/"}
		rt := web.NewRouter(slog.New(slog.DiscardHandler), nil)
		a.auth.Routes(rt)
		a.h = rt.Handler()
		body := a.do("GET", "/session/new", nil).Body.String()
		if !strings.Contains(body, `action="/session/new"`) || !strings.Contains(body, `href="/reset/new"`) {
			t.Fatal(body)
		}
		if rec := a.do("POST", "/session/new", url.Values{"email_address": {"one@example.com"}, "password": {password}}); rec.Header().Get("Location") != "/" {
			t.Fatalf("%v", rec.Header())
		}
	})
}

func TestDefaultRules(t *testing.T) {
	for pw, want := range map[string]string{
		"":                      "Password can't be blank",
		"short":                 "Password is too short (minimum is 8 characters)",
		strings.Repeat("x", 73): "Password is too long (maximum is 72 bytes)",
		"long enough":           "",
	} {
		got := strings.Join(DefaultRules(pw, pw), "; ")
		if got != want {
			t.Errorf("%q: %q, want %q", pw, got, want)
		}
	}
	if errs := DefaultRules("long enough", "different"); len(errs) != 1 {
		t.Errorf("%v", errs)
	}
}
