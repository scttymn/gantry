// Package auth signs people in: email and password, sessions, and password
// reset by emailed link. It works on two tables the app owns, as Rails 8's
// authentication generator leaves them (so a Rails app's users carry over,
// bcrypt hashes and all):
//
//	users:    id, email_address (unique, lowercased), password_digest,
//	          created_at, updated_at
//	sessions: id, user_id, token_digest (unique), ip_address, user_agent,
//	          created_at, last_seen_at
//
// A session's cookie holds a random token and the table holds its SHA-256,
// so a copy of the database signs nobody in, and ending a session is
// deleting its row. Only routes wrapped in Require look a session up: public
// pages never touch the sessions table.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/mail"
	"github.com/scttymn/gantry/sign"
	"github.com/scttymn/gantry/web"
)

// User is someone who can sign in.
type User struct {
	ID             int64
	EmailAddress   string
	PasswordDigest string
}

// Session is a signed-in browser.
type Session struct {
	ID         int64
	UserID     int64
	LastSeenAt time.Time
}

// Page is what gantry gives a view: the flash message for it, and where
// the sign-in pages are (for its forms' actions and links).
type Page struct {
	Notice, Alert string
	Paths         Paths
}

// Views draw the sign-in pages. Each writes the whole response, so an app
// draws them in its own layout (and may read its database to do so).
// gantry's defaults (DefaultViews) are plain forms.
type Views struct {
	Login          func(w http.ResponseWriter, r *http.Request, p Page) error
	ForgotPassword func(w http.ResponseWriter, r *http.Request, p Page) error
	EditPassword   func(w http.ResponseWriter, r *http.Request, p Page, token string) error
}

// Paths are where the sign-in pages are. Zero values take the defaults.
type Paths struct {
	Login      string // "/login": GET the form, POST to sign in
	Logout     string // "/logout": POST (or DELETE, or GET) to sign out
	Passwords  string // "/passwords": /new, POST, /{token}/edit, PUT or PATCH /{token}
	AfterLogin string // "/": where sign-in lands when nothing asked to come back
}

// Auth is an app's sign-in.
type Auth struct {
	DB     *db.DB
	Signer sign.Signer
	Mail   mail.Sender
	From   string // who reset emails are from
	// URL is an absolute address on the site for path, for emailed links:
	// the app's configured host, never the request's, which whoever asks
	// could set to their own.
	URL   func(path string) string
	Views Views
	Paths Paths
	// Rules are the app's rules for a new password: none means it's
	// acceptable. DefaultRules when nil.
	Rules  func(password, confirmation string) []string
	Limits *web.Limits
	Log    *slog.Logger
	Now    func() time.Time
	// Cookie is the session cookie's name: "session" when empty.
	Cookie string
}

// The messages are Rails 8's generator's.
const (
	msgBadLogin     = "Try another email address or password."
	msgTooMany      = "Try again later."
	msgResetSent    = "Password reset instructions sent (if user with that email address exists)."
	msgResetInvalid = "Password reset link is invalid or has expired."
	msgResetDone    = "Password has been reset."
)

// ResetValidity is how long a reset link works, as Rails'.
const ResetValidity = 15 * time.Minute

// bcryptCost is Rails' (has_secure_password's default). Tests lower it:
// a cost-12 hash takes a quarter of a second.
var bcryptCost = 12

func (a *Auth) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *Auth) cookie() string {
	if a.Cookie != "" {
		return a.Cookie
	}
	return "session"
}

func (a *Auth) paths() Paths {
	p := a.Paths
	if p.Login == "" {
		p.Login = "/login"
	}
	if p.Logout == "" {
		p.Logout = "/logout"
	}
	if p.Passwords == "" {
		p.Passwords = "/passwords"
	}
	if p.AfterLogin == "" {
		p.AfterLogin = "/"
	}
	return p
}

func (a *Auth) views() Views {
	v, d := a.Views, DefaultViews()
	if v.Login == nil {
		v.Login = d.Login
	}
	if v.ForgotPassword == nil {
		v.ForgotPassword = d.ForgotPassword
	}
	if v.EditPassword == nil {
		v.EditPassword = d.EditPassword
	}
	return v
}

func (a *Auth) flash() web.Flash { return web.Flash{Signer: a.Signer} }

// Normalize is how an email is stored and looked up: trimmed, lowercased.
func Normalize(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

// DefaultRules: 8 to 72 bytes (bcrypt reads no further), confirmed.
func DefaultRules(password, confirmation string) []string {
	var errs []string
	switch {
	case password == "":
		return []string{"Password can't be blank"}
	case len(password) > 72:
		errs = append(errs, "Password is too long (maximum is 72 bytes)")
	case len(password) < 8:
		errs = append(errs, "Password is too short (minimum is 8 characters)")
	}
	if confirmation != password {
		errs = append(errs, "Password confirmation doesn't match Password")
	}
	return errs
}

func (a *Auth) rules(password, confirmation string) []string {
	if a.Rules != nil {
		return a.Rules(password, confirmation)
	}
	return DefaultRules(password, confirmation)
}

// Hash is a password's bcrypt hash.
func Hash(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	return string(h), err
}

// A hash to compare against when there's no such user, so a wrong email
// takes as long as a wrong password. Made on first need: a cost-12 hash
// takes a quarter of a second.
var dummy = sync.OnceValue(func() []byte {
	h, _ := bcrypt.GenerateFromPassword([]byte("no such user"), bcryptCost)
	return h
})

// ErrNoUser is an email or id with no user behind it.
var ErrNoUser = errors.New("auth: no such user")

func (a *Auth) findBy(ctx context.Context, q db.Querier, column string, v any) (User, error) {
	var u User
	err := q.QueryRowContext(ctx, `SELECT id, email_address, password_digest FROM users WHERE `+column+` = $1`, v).
		Scan(&u.ID, &u.EmailAddress, &u.PasswordDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNoUser
	}
	return u, err
}

// FindByEmail is the user with this email.
func (a *Auth) FindByEmail(ctx context.Context, email string) (User, error) {
	return a.findBy(ctx, a.DB.Read, "email_address", Normalize(email))
}

// Find is the user with this id.
func (a *Auth) Find(ctx context.Context, id int64) (User, error) {
	return a.findBy(ctx, a.DB.Read, "id", id)
}

// Authenticate is the user with this email and password.
func (a *Auth) Authenticate(ctx context.Context, email, password string) (User, bool) {
	u, err := a.FindByEmail(ctx, email)
	if err != nil {
		bcrypt.CompareHashAndPassword(dummy(), []byte(password))
		return User{}, false
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordDigest), []byte(password)) != nil {
		return User{}, false
	}
	return u, true
}

// CreateUser adds a user with this email and password (the rules aren't
// checked: that's the caller's form).
func (a *Auth) CreateUser(ctx context.Context, email, password string) (User, error) {
	digest, err := Hash(password)
	if err != nil {
		return User{}, err
	}
	now := a.now()
	u := User{EmailAddress: Normalize(email), PasswordDigest: digest}
	err = a.DB.Write.QueryRowContext(ctx, `INSERT INTO users (email_address, password_digest, created_at, updated_at) VALUES ($1, $2, $3, $3) RETURNING id`,
		u.EmailAddress, u.PasswordDigest, now).Scan(&u.ID)
	return u, err
}

// SetPassword saves a new password and ends every session the user had, so
// a reset locks out whoever else was signed in.
func (a *Auth) SetPassword(ctx context.Context, userID int64, password string) error {
	digest, err := Hash(password)
	if err != nil {
		return err
	}
	return a.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET password_digest = $1, updated_at = $2 WHERE id = $3`, digest, a.now(), userID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID)
		return err
	})
}

func digestOf(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// StartSession records a new session for the user and sets its cookie,
// lasting until sign-out (Rails 8's permanent cookie).
func (a *Auth) StartSession(w http.ResponseWriter, r *http.Request, u User) (Session, error) {
	raw := make([]byte, 32)
	rand.Read(raw)
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := a.now()
	s := Session{UserID: u.ID, LastSeenAt: now}
	err := a.DB.Write.QueryRowContext(r.Context(), `INSERT INTO sessions (user_id, token_digest, ip_address, user_agent, created_at, last_seen_at) VALUES ($1, $2, $3, $4, $5, $5) RETURNING id`,
		u.ID, digestOf(token), web.ClientIP(r), r.UserAgent(), now).Scan(&s.ID)
	if err != nil {
		return Session{}, err
	}
	web.SetCookie(w, r, a.cookie(), token, 20*365*24*time.Hour)
	return s, nil
}

// touchEvery is how often a session's last_seen_at is written: often
// enough to show who's active, rarely enough not to write on every request.
const touchEvery = time.Hour

// sessionOf is the request's session and its user, if its cookie names
// one that still exists.
func (a *Auth) sessionOf(r *http.Request) (User, Session, bool) {
	c, err := r.Cookie(a.cookie())
	if err != nil || c.Value == "" {
		return User{}, Session{}, false
	}
	var u User
	var s Session
	err = a.DB.Read.QueryRowContext(r.Context(), `SELECT s.id, s.user_id, s.last_seen_at, u.email_address, u.password_digest
		FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.token_digest = $1`, digestOf(c.Value)).
		Scan(&s.ID, &s.UserID, &s.LastSeenAt, &u.EmailAddress, &u.PasswordDigest)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) && a.Log != nil {
			a.Log.Error("auth: session lookup", "err", err)
		}
		return User{}, Session{}, false
	}
	u.ID = s.UserID
	if now := a.now(); now.Sub(s.LastSeenAt) >= touchEvery {
		if _, err := a.DB.Write.ExecContext(r.Context(), `UPDATE sessions SET last_seen_at = $1 WHERE id = $2`, now, s.ID); err == nil {
			s.LastSeenAt = now
		}
	}
	return u, s, true
}

type userKey struct{}

// Current is the signed-in user of a request that went through Require.
func Current(r *http.Request) (User, bool) {
	u, ok := r.Context().Value(userKey{}).(User)
	return u, ok
}

// SignedIn reports whether the request carries a live session, for a page
// that looks different to someone signed in but doesn't need them (the
// sign-in page's own layout, say). It reads the sessions table.
func (a *Auth) SignedIn(r *http.Request) bool {
	if _, ok := Current(r); ok {
		return true
	}
	_, _, ok := a.sessionOf(r)
	return ok
}

const returnToCookie = "return_to"

// Require lets signed-in users through, with their user in the request
// (Current). Anyone else goes to sign in, and comes back here after.
func (a *Auth) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _, ok := a.sessionOf(r)
		if !ok {
			web.SetCookie(w, r, returnToCookie, a.Signer.Sign(returnToCookie, r.URL.RequestURI()), time.Hour)
			http.Redirect(w, r, a.paths().Login, http.StatusFound)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, u)))
	})
}

// Routes mounts the sign-in pages on rt.
func (a *Auth) Routes(rt *web.Router) {
	p := a.paths()
	rt.Handle("GET "+p.Login, a.loginPage)
	rt.Handle("POST "+p.Login, a.login)
	for _, m := range []string{"GET", "POST", "DELETE"} {
		rt.Handle(m+" "+p.Logout, a.logout)
	}
	rt.Handle("GET "+p.Passwords+"/new", a.forgotPassword)
	rt.Handle("POST "+p.Passwords, a.sendReset)
	rt.Handle("GET "+p.Passwords+"/{token}/edit", a.editPassword)
	rt.Handle("PUT "+p.Passwords+"/{token}", a.updatePassword)
	rt.Handle("PATCH "+p.Passwords+"/{token}", a.updatePassword)
}

// page is the flash for this page, taken.
func (a *Auth) page(w http.ResponseWriter, r *http.Request) Page {
	p := Page{Paths: a.paths()}
	if kind, msg, ok := a.flash().Take(w, r); ok {
		if kind == "alert" {
			p.Alert = msg
		} else {
			p.Notice = msg
		}
	}
	return p
}

func (a *Auth) allow(r *http.Request, action string) bool {
	if a.Limits == nil {
		return true
	}
	return a.Limits.Allow(action+":"+web.ClientIP(r), 10, 3*time.Minute, a.now())
}

func (a *Auth) loginPage(w http.ResponseWriter, r *http.Request) error {
	return a.views().Login(w, r, a.page(w, r))
}

func (a *Auth) login(w http.ResponseWriter, r *http.Request) error {
	p := a.paths()
	if !a.allow(r, "login") {
		a.flash().Redirect(w, r, p.Login, "alert", msgTooMany)
		return nil
	}
	u, ok := a.Authenticate(r.Context(), r.PostFormValue("email_address"), r.PostFormValue("password"))
	if !ok {
		a.flash().Redirect(w, r, p.Login, "alert", msgBadLogin)
		return nil
	}
	if _, err := a.StartSession(w, r, u); err != nil {
		return err
	}
	to := p.AfterLogin
	if back, ok := web.SignedCookie(r, a.Signer, returnToCookie); ok && strings.HasPrefix(back, "/") && !strings.HasPrefix(back, "//") && !strings.HasPrefix(back, "/\\") {
		to = back
	}
	web.ClearCookie(w, returnToCookie)
	http.Redirect(w, r, to, http.StatusFound)
	return nil
}

func (a *Auth) logout(w http.ResponseWriter, r *http.Request) error {
	if c, err := r.Cookie(a.cookie()); err == nil && c.Value != "" {
		if _, err := a.DB.Write.ExecContext(r.Context(), `DELETE FROM sessions WHERE token_digest = $1`, digestOf(c.Value)); err != nil {
			return err
		}
	}
	web.ClearCookie(w, a.cookie())
	http.Redirect(w, r, a.paths().Login, http.StatusSeeOther)
	return nil
}

func (a *Auth) forgotPassword(w http.ResponseWriter, r *http.Request) error {
	return a.views().ForgotPassword(w, r, a.page(w, r))
}

// fingerprint changes whenever the password does, so a used link dies.
func fingerprint(u User) string {
	sum := sha256.Sum256([]byte(u.PasswordDigest))
	return hex.EncodeToString(sum[:])[:10]
}

// ResetToken is a password reset token for the user, good for
// ResetValidity and until the password changes.
func (a *Auth) ResetToken(u User) string {
	return a.Signer.Sign("password_reset", fmt.Sprintf("%d|%d|%s", u.ID, a.now().Add(ResetValidity).Unix(), fingerprint(u)))
}

// ErrBadToken is a reset token that's forged, expired, or used.
var ErrBadToken = errors.New("auth: password reset link is invalid or has expired")

// UserByResetToken is the user the token was made for, if it's still good.
func (a *Auth) UserByResetToken(ctx context.Context, token string) (User, error) {
	value, ok := a.Signer.Verify("password_reset", token)
	if !ok {
		return User{}, ErrBadToken
	}
	parts := strings.Split(value, "|")
	if len(parts) != 3 {
		return User{}, ErrBadToken
	}
	id, err1 := strconv.ParseInt(parts[0], 10, 64)
	expires, err2 := strconv.ParseInt(parts[1], 10, 64)
	if err1 != nil || err2 != nil || a.now().Unix() > expires {
		return User{}, ErrBadToken
	}
	u, err := a.Find(ctx, id)
	if err != nil || fingerprint(u) != parts[2] {
		return User{}, ErrBadToken
	}
	return u, nil
}

// sendReset emails a reset link to a user with that address. Anyone else
// gets the same answer, so the form can't be used to find out who has an
// account.
func (a *Auth) sendReset(w http.ResponseWriter, r *http.Request) error {
	p := a.paths()
	if !a.allow(r, "passwords") {
		a.flash().Redirect(w, r, p.Passwords+"/new", "alert", msgTooMany)
		return nil
	}
	if u, err := a.FindByEmail(r.Context(), r.PostFormValue("email_address")); err == nil {
		link := a.URL(p.Passwords + "/" + url.PathEscape(a.ResetToken(u)) + "/edit")
		msg := mail.Message{From: a.From, To: u.EmailAddress, Subject: "Reset your password",
			Text: fmt.Sprintf("You can reset your password on\n%s\n\nThis link will expire in %d minutes.\n", link, int(ResetValidity/time.Minute))}
		if err := a.Mail.Send(r.Context(), msg); err != nil && a.Log != nil {
			a.Log.Error("auth: password reset email", "err", err)
		}
	}
	a.flash().Redirect(w, r, p.Login, "notice", msgResetSent)
	return nil
}

func (a *Auth) editPassword(w http.ResponseWriter, r *http.Request) error {
	token := r.PathValue("token")
	if _, err := a.UserByResetToken(r.Context(), token); err != nil {
		a.flash().Redirect(w, r, a.paths().Passwords+"/new", "alert", msgResetInvalid)
		return nil
	}
	return a.views().EditPassword(w, r, a.page(w, r), token)
}

func (a *Auth) updatePassword(w http.ResponseWriter, r *http.Request) error {
	p := a.paths()
	token := r.PathValue("token")
	u, err := a.UserByResetToken(r.Context(), token)
	if err != nil {
		a.flash().Redirect(w, r, p.Passwords+"/new", "alert", msgResetInvalid)
		return nil
	}
	password, confirmation := r.PostFormValue("password"), r.PostFormValue("password_confirmation")
	if errs := a.rules(password, confirmation); len(errs) > 0 {
		// Say what was actually wrong: a password that breaks the rules isn't a mismatch.
		a.flash().Redirect(w, r, p.Passwords+"/"+url.PathEscape(token)+"/edit", "alert", sentence(errs))
		return nil
	}
	if err := a.SetPassword(r.Context(), u.ID, password); err != nil {
		return err
	}
	a.flash().Redirect(w, r, p.Login, "notice", msgResetDone)
	return nil
}

// sentence joins messages as Rails' to_sentence.
func sentence(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	}
	return strings.Join(items[:len(items)-1], ", ") + ", and " + items[len(items)-1]
}
