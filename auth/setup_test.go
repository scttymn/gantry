package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/web"
)

func newSetupAuth(t *testing.T, engine string) *Auth {
	t.Helper()
	a, _, _ := newCodeApp(t, engine)
	a.EmailCodes = nil
	a.Mail = nil
	a.FirstUser = &FirstUser{CodeFile: filepath.Join(t.TempDir(), "initial-account-code")}
	_, err := a.DB.Write.ExecContext(t.Context(), `CREATE TABLE auth_setup(id integer PRIMARY KEY CHECK(id=1),completed boolean NOT NULL,code_digest text NOT NULL DEFAULT ''); INSERT INTO auth_setup(id,completed) VALUES(1,false)`)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func setupHandler(a *Auth) http.Handler {
	rt := web.NewRouter(slog.New(slog.DiscardHandler), nil)
	a.Routes(rt)
	return rt.Handler()
}
func setupRequest(h http.Handler, method, path string, v url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://example.com"+path, strings.NewReader(v.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.RemoteAddr = "203.0.113.8:2345"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func setupForm(code, email string) url.Values {
	return url.Values{"setup_code": {code}, "email_address": {email}, "password": {password}, "password_confirmation": {password}}
}

func TestFirstUserSetup(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			a := newSetupAuth(t, engine)
			a.Paths = Paths{Login: "/sign-in", AfterLogin: "/account"}
			h := setupHandler(a)
			code, err := a.FirstUserCode(t.Context(), false)
			if err != nil || !strings.HasPrefix(code, "setup_") || len(code) < 40 {
				t.Fatal("setup code unavailable", err)
			}
			stat, err := os.Stat(a.FirstUser.CodeFile)
			if err != nil || stat.Mode().Perm() != 0600 {
				t.Fatal("code file not private")
			}
			var digest string
			a.DB.Read.QueryRow(`SELECT code_digest FROM auth_setup`).Scan(&digest)
			if digest == code || len(digest) != 64 {
				t.Fatal("plaintext stored in database")
			}
			page := setupRequest(h, "GET", "/sign-in", nil)
			if page.Code != 200 || !strings.Contains(page.Body.String(), "Create your first account") || strings.Contains(page.Body.String(), code) || page.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("setup page missing or leaked code")
			}
			if err := a.PrepareFirstUser(t.Context()); err != nil {
				t.Fatal(err)
			}
			if again, err := a.FirstUserCode(t.Context(), false); err != nil || again != code {
				t.Fatal("restart changed pending code", err)
			}
			for _, tc := range []struct {
				name   string
				change func(url.Values)
			}{
				{"wrong code", func(v url.Values) { v.Set("setup_code", "wrong") }},
				{"invalid email", func(v url.Values) { v.Set("email_address", "invalid") }},
				{"display name", func(v url.Values) { v.Set("email_address", "Someone <owner@example.com>") }},
				{"weak password", func(v url.Values) { v.Set("password", "short"); v.Set("password_confirmation", "short") }},
				{"mismatch", func(v url.Values) { v.Set("password_confirmation", "different") }},
			} {
				t.Run(tc.name, func(t *testing.T) {
					v := setupForm(code, "owner@example.com")
					tc.change(v)
					response := setupRequest(h, "POST", "/sign-in/setup", v)
					if response.Code != 422 || strings.Contains(response.Body.String(), code) || strings.Contains(response.Body.String(), `value="`+password+`"`) {
						t.Fatal("invalid form was accepted or exposed credentials", response.Code)
					}
					var count int
					a.DB.Read.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count)
					if count != 0 {
						t.Fatal("invalid setup created a user")
					}
				})
			}
			// Cross-origin requests are rejected by the same Gantry router protection.
			r := httptest.NewRequest("POST", "https://example.com/sign-in/setup", strings.NewReader(setupForm(code, "owner@example.com").Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("Sec-Fetch-Site", "cross-site")
			blocked := httptest.NewRecorder()
			h.ServeHTTP(blocked, r)
			if blocked.Code != 422 {
				t.Fatal("cross-origin setup accepted", blocked.Code)
			}
			rotated, err := a.FirstUserCode(t.Context(), true)
			if err != nil || rotated == code {
				t.Fatal("rotation failed", err)
			}
			if setupRequest(h, "POST", "/sign-in/setup", setupForm(code, "owner@example.com")).Code != 422 {
				t.Fatal("old code accepted")
			}
			created := setupRequest(h, "POST", "/sign-in/setup", setupForm(rotated, " Owner@Example.com "))
			if created.Code != 303 || created.Header().Get("Location") != "/account" {
				t.Fatal("setup failed", created.Code, created.Body.String())
			}
			request := httptest.NewRequest("GET", "https://example.com/account", nil)
			for _, c := range created.Result().Cookies() {
				request.AddCookie(c)
			}
			if !a.SignedIn(request) {
				t.Fatal("no session after setup")
			}
			if _, ok := a.Authenticate(t.Context(), "owner@example.com", password); !ok {
				t.Fatal("password not usable")
			}
			if _, err := os.Stat(a.FirstUser.CodeFile); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("consumed code file remains")
			}
			if strings.Contains(setupRequest(h, "GET", "/sign-in", nil).Body.String(), "first account") {
				t.Fatal("setup stayed open")
			}
			setupRequest(h, "POST", "/sign-in/setup", setupForm(rotated, "intruder@example.com"))
			var count int
			a.DB.Read.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count)
			if count != 1 {
				t.Fatal("stale form created another account")
			}
			// Losing every user does not re-enable the wizard, including after restart.
			a.DB.Write.Exec(`DELETE FROM sessions`)
			a.DB.Write.Exec(`DELETE FROM users`)
			h = setupHandler(a)
			if pending, err := a.FirstUserPending(t.Context()); err != nil || pending {
				t.Fatal("deleting users reopened setup", err)
			}
			if _, err := a.FirstUserCode(t.Context(), true); !errors.Is(err, ErrSetupClosed) {
				t.Fatal("operator rotation reopened setup", err)
			}
			if strings.Contains(setupRequest(h, "GET", "/sign-in", nil).Body.String(), "first account") {
				t.Fatal("completed empty install offered setup")
			}
		})
	}
}

func TestFirstUserConcurrency(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			a := newSetupAuth(t, engine)
			first := setupHandler(a)
			other := *a
			if engine == "sqlite" {
				var seq int
				var name, filename string
				if err := a.DB.Read.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &filename); err != nil {
					t.Fatal(err)
				}
				d, err := db.Open(t.Context(), "sqlite://"+filename)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { d.Close() })
				other.DB = d
			}
			second := setupHandler(&other)
			code, err := a.FirstUserCode(t.Context(), false)
			if err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			var signedIn atomic.Int32
			for n := range 12 {
				wg.Go(func() {
					h := first
					if n%2 == 0 {
						h = second
					}
					response := setupRequest(h, "POST", "/login/setup", setupForm(code, fmt.Sprintf("owner%d@example.com", n)))
					if response.Code != 303 {
						t.Errorf("unexpected response: %d", response.Code)
					}
					for _, c := range response.Result().Cookies() {
						if c.Name == "session" && c.Value != "" {
							signedIn.Add(1)
						}
					}
				})
			}
			wg.Wait()
			var count int
			a.DB.Read.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count)
			if count != 1 || signedIn.Load() != 1 {
				t.Fatalf("%d users, %d successful browsers", count, signedIn.Load())
			}
		})
	}
}

func TestFirstUserFailureAndExistingInstall(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			t.Run("hook rolls back account and completion", func(t *testing.T) {
				a := newSetupAuth(t, engine)
				h := setupHandler(a)
				code, _ := a.FirstUserCode(t.Context(), false)
				a.FirstUser.CreateUser = func(ctx context.Context, tx *db.Tx, email, digest string) (User, error) {
					_, err := tx.ExecContext(ctx, `INSERT INTO users(email_address,password_digest,created_at,updated_at) VALUES($1,$2,$3,$3)`, email, digest, a.now())
					if err != nil {
						return User{}, err
					}
					return User{}, errors.New("app validation failed")
				}
				if w := setupRequest(h, "POST", "/login/setup", setupForm(code, "owner@example.com")); w.Code != 500 {
					t.Fatal("failed hook accepted", w.Code)
				}
				var count int
				a.DB.Read.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count)
				if count != 0 {
					t.Fatal("hook insert did not roll back")
				}
				if pending, err := a.FirstUserPending(t.Context()); err != nil || !pending {
					t.Fatal("failure completed setup", err)
				}
				a.FirstUser.CreateUser = nil
				if w := setupRequest(h, "POST", "/login/setup", setupForm(code, "owner@example.com")); w.Code != 303 {
					t.Fatal("retry failed")
				}
			})
			t.Run("seeded user closes setup", func(t *testing.T) {
				a := newSetupAuth(t, engine)
				if _, err := a.CreateUser(t.Context(), "seed@example.com", password); err != nil {
					t.Fatal(err)
				}
				a.DB.Write.Exec(`DELETE FROM users`)
				if _, err := a.FirstUserCode(t.Context(), false); !errors.Is(err, ErrSetupClosed) {
					t.Fatal("seeded install reopened", err)
				}
			})
			t.Run("existing install migration stays closed even empty", func(t *testing.T) {
				a := newSetupAuth(t, engine)
				a.DB.Write.Exec(`UPDATE auth_setup SET completed=true`)
				if err := a.PrepareFirstUser(t.Context()); err != nil {
					t.Fatal(err)
				}
				if _, err := a.FirstUserCode(t.Context(), true); !errors.Is(err, ErrSetupClosed) {
					t.Fatal("upgrade reopened setup", err)
				}
			})
			t.Run("missing state fails closed", func(t *testing.T) {
				a := newSetupAuth(t, engine)
				h := setupHandler(a)
				a.DB.Write.Exec(`DELETE FROM auth_setup`)
				if err := a.PrepareFirstUser(t.Context()); err == nil {
					t.Fatal("missing state recreated")
				}
				if w := setupRequest(h, "GET", "/login", nil); w.Code != 500 || strings.Contains(w.Body.String(), "first account") {
					t.Fatal("missing state exposed setup", w.Code)
				}
			})
		})
	}
}

func TestCompletedSetupDoesNotLockUsersTable(t *testing.T) {
	a := newSetupAuth(t, "postgres")
	if _, err := a.CreateUser(t.Context(), "seed@example.com", password); err != nil {
		t.Fatal(err)
	}
	tx, err := a.DB.Write.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(t.Context(), `UPDATE users SET updated_at=created_at`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := a.CreateUser(ctx, "next@example.com", password); err != nil {
		t.Fatal("completed setup blocked ordinary account creation behind unrelated user writes", err)
	}
}
