package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	netmail "net/mail"
	"strings"
	"time"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/mail"
	"github.com/scttymn/gantry/token"
	"github.com/scttymn/gantry/web"
)

// EmailCodes selects passwordless login when set on Auth. Codes live in the
// app-owned auth_login_codes and auth_code_limits tables (g auth --email-code).
// A code is six digits, good for ten minutes and five attempts. All instances
// must use the same database and key.
type EmailCodes struct {
	// Key is a random secret of at least 32 bytes, supplied from outside the
	// database (AUTH_CODE_KEY). Hashing a six-digit code without this secret
	// would let someone with the database try all million values offline.
	Key []byte
	// Signup permits creating users after they prove control of their email.
	// False accepts existing accounts only. Both modes send identical emails
	// to valid addresses, without looking up accounts on the request path.
	Signup bool
	// CreateUser optionally fills app-specific fields, within the same
	// transaction that consumes the code. Only called for a new, verified
	// address when Signup is true. It must use tx, not another connection,
	// and return the inserted user. Returning an error rolls everything back.
	CreateUser func(ctx context.Context, tx *db.Tx, email string) (User, error)
	// DailyLimit caps accepted send requests in a 24-hour window across all
	// instances. Zero means no app-wide cap; email/IP limits still apply.
	DailyLimit int
}

const (
	CodeValidity = 10 * time.Minute
	CodeAttempts = 5
	codeCookie   = "login_code"
	codeWindow   = 15 * time.Minute
)

var (
	ErrBadCode      = errors.New("auth: login code is incorrect, expired or already used")
	ErrCodeEmail    = errors.New("auth: enter a valid email address")
	ErrCodeLimited  = errors.New("auth: too many login code attempts; try again later")
	ErrCodeDelivery = errors.New("auth: couldn't send a login code; try again later")
)

// ValidateEmailCodes checks the configuration, without querying the database.
// Routes calls it at startup when email-code mode is enabled. Direct API users
// can call it at startup too; RequestLoginCode and VerifyLoginCode also check it.
func (a *Auth) ValidateEmailCodes() error {
	if a.EmailCodes == nil {
		return errors.New("auth: email-code login is disabled")
	}
	if a.DB == nil || a.Mail == nil {
		return errors.New("auth: email codes need a database and mail sender")
	}
	if len(a.EmailCodes.Key) < 32 {
		return errors.New("auth: email codes need an external secret of at least 32 bytes")
	}
	if a.EmailCodes.DailyLimit < 0 {
		return errors.New("auth: email-code daily limit cannot be negative")
	}
	if _, err := netmail.ParseAddress(a.From); err != nil {
		return errors.New("auth: email codes need a valid From address")
	}
	switch sender := a.Mail.(type) {
	case *mail.Mailer:
		if !sender.Ready() {
			return errors.New("auth: configure the app mailer to deliver or capture email codes")
		}
	case mail.Log, *mail.Log:
		return errors.New("auth: mail.Log does not deliver login codes; configure a real sender or development mail capture")
	}
	return nil
}

func (a *Auth) codeDigest(purpose, value string) string {
	m := hmac.New(sha256.New, a.EmailCodes.Key)
	m.Write([]byte("gantry.auth." + purpose + "\x00" + value))
	return hex.EncodeToString(m.Sum(nil))
}

// codeLimit atomically reserves a slot; its conditional upsert serializes
// callers sharing the bucket on both SQLite and PostgreSQL.
func (a *Auth) codeLimit(ctx context.Context, tx *db.Tx, key string, limit int, window time.Duration, now time.Time) error {
	var n int
	err := tx.QueryRowContext(ctx, `INSERT INTO auth_code_limits (bucket, count, expires_at) VALUES ($1, 1, $2)
ON CONFLICT (bucket) DO UPDATE SET
count = CASE WHEN auth_code_limits.expires_at <= $3 THEN 1 ELSE auth_code_limits.count + 1 END,
expires_at = CASE WHEN auth_code_limits.expires_at <= $3 THEN $2 ELSE auth_code_limits.expires_at END
WHERE auth_code_limits.expires_at <= $3 OR auth_code_limits.count < $4
RETURNING count`, a.codeDigest("limit", key), now.Add(window).Unix(), now.Unix(), limit).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrCodeLimited
	}
	return err
}

// RequestLoginCode emails a code and returns a random challenge to keep in the
// requesting browser (or mobile client). Keep this value out of URLs and logs.
// Each successful request replaces that email's previous challenge. A user is
// never created here. ip must come from the trusted request's client address.
// The provider call runs after commit, without holding a database connection.
func (a *Auth) RequestLoginCode(ctx context.Context, email, ip string) (string, error) {
	if err := a.ValidateEmailCodes(); err != nil {
		return "", err
	}
	email = Normalize(email)
	address, err := netmail.ParseAddress(email)
	if err != nil || address.Address != email || len(email) > 254 || strings.ContainsAny(email, "\r\n") {
		return "", ErrCodeEmail
	}
	if strings.TrimSpace(ip) == "" {
		return "", errors.New("auth: login code requests need a client IP")
	}
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	code := fmt.Sprintf("%06d", n.Int64())
	challenge, digest := token.New("")
	now := a.now()
	err = a.DB.Tx(ctx, func(tx *db.Tx) error {
		for _, rule := range []struct {
			key    string
			limit  int
			window time.Duration
		}{
			{"send-ip:" + ip, 20, codeWindow},
			{"send-email:" + email, 3, codeWindow},
			{"resend:" + email, 1, time.Minute},
		} {
			if err := a.codeLimit(ctx, tx, rule.key, rule.limit, rule.window, now); err != nil {
				return err
			}
		}
		if a.EmailCodes.DailyLimit > 0 {
			if err := a.codeLimit(ctx, tx, "daily", a.EmailCodes.DailyLimit, 24*time.Hour, now); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO auth_login_codes (challenge_digest, email_address, code_digest, expires_at, attempts)
VALUES ($1, $2, $3, $4, 0) ON CONFLICT (email_address) DO UPDATE SET
challenge_digest = excluded.challenge_digest, code_digest = excluded.code_digest, expires_at = excluded.expires_at, attempts = 0`,
			digest, email, a.codeDigest("login", digest+"\x00"+email+"\x00"+code), now.Add(CodeValidity).Unix())
		return err
	})
	if err != nil {
		return "", err
	}
	msg := mail.Message{From: a.From, To: email, Subject: "Your sign-in code",
		Text: "Your sign-in code is: " + code + "\n\nIt expires in 10 minutes and can be used only once.\nIf you didn't request this code, you can ignore this email.\n"}
	if err := a.Mail.Send(ctx, msg); err != nil {
		// A request may have been cancelled during delivery. Invalidate only
		// its own challenge, without accidentally deleting a later resend.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, cleanupErr := a.DB.Write.ExecContext(cleanup, `DELETE FROM auth_login_codes WHERE challenge_digest = $1`, digest)
		if a.Log != nil {
			a.Log.Error("auth: login code delivery failed", "err", err, "cleanup_err", cleanupErr)
		}
		return "", ErrCodeDelivery
	}
	return challenge, nil
}

// VerifyLoginCode consumes a correct challenge/code exactly once and returns
// its user. Wrong guesses consume attempts, including simultaneous guesses.
// Verification, optional signup and consumption are one transaction. Browser
// routes then use StartSession; API controllers can issue their usual tokens.
func (a *Auth) VerifyLoginCode(ctx context.Context, challenge, code, ip string) (User, error) {
	if err := a.ValidateEmailCodes(); err != nil {
		return User{}, err
	}
	if strings.TrimSpace(ip) == "" {
		return User{}, errors.New("auth: login code verification needs a client IP")
	}
	var user User
	result := ErrBadCode
	now := a.now()
	err := a.DB.Tx(ctx, func(tx *db.Tx) error {
		if err := a.codeLimit(ctx, tx, "verify-ip:"+ip, 50, codeWindow, now); err != nil {
			return err
		}
		digest := token.Digest(challenge)
		var email, expected string
		err := tx.QueryRowContext(ctx, `UPDATE auth_login_codes SET attempts = attempts + 1
WHERE challenge_digest = $1 AND expires_at > $2 AND attempts < $3
RETURNING email_address, code_digest`, digest, now.Unix(), CodeAttempts).Scan(&email, &expected)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		} // Commit the IP attempt.
		if err != nil {
			return err
		}
		actual := a.codeDigest("login", digest+"\x00"+email+"\x00"+strings.TrimSpace(code))
		if !hmac.Equal([]byte(expected), []byte(actual)) {
			return nil
		} // Commit the failed guess.
		user, err = a.findBy(ctx, tx, "email_address", email)
		if errors.Is(err, ErrNoUser) && a.EmailCodes.Signup {
			if a.EmailCodes.CreateUser != nil {
				user, err = a.EmailCodes.CreateUser(ctx, tx, email)
				if err == nil && (user.ID == 0 || user.EmailAddress != email) {
					return errors.New("auth: signup must return the user for the verified email")
				}
			} else {
				_, err = tx.ExecContext(ctx, `INSERT INTO users (email_address, password_digest, created_at, updated_at)
VALUES ($1, '', $2, $2) ON CONFLICT (email_address) DO NOTHING`, email, now)
				if err == nil {
					user, err = a.findBy(ctx, tx, "email_address", email)
				}
			}
		}
		if err != nil && !errors.Is(err, ErrNoUser) {
			return err
		}
		if _, deleteErr := tx.ExecContext(ctx, `DELETE FROM auth_login_codes WHERE challenge_digest = $1`, digest); deleteErr != nil {
			return deleteErr
		}
		if err == nil {
			result = nil
		}
		return nil
	})
	if err != nil {
		return User{}, err
	}
	if result != nil {
		return User{}, result
	}
	return user, nil
}

// PruneLoginCodes removes expired challenges and rate-limit buckets. Schedule
// it as a maintenance job, e.g. hourly. It never removes unexpired attempts.
func (a *Auth) PruneLoginCodes(ctx context.Context) error {
	return a.DB.Tx(ctx, func(tx *db.Tx) error {
		for _, table := range []string{"auth_code_limits", "auth_login_codes"} {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE expires_at <= $1`, a.now().Unix()); err != nil {
				return err
			}
		}
		return nil
	})
}

func (a *Auth) requestCodePage(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Cache-Control", "no-store")
	return a.views().RequestCode(w, r, a.page(w, r))
}

func (a *Auth) sendCode(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Cache-Control", "no-store")
	email := Normalize(r.PostFormValue("email_address"))
	if a.Attempt != nil && !a.Attempt(r) {
		a.flash().Redirect(w, r, a.paths().Login, "alert", msgTooMany)
		return nil
	}
	challenge, err := a.RequestLoginCode(r.Context(), email, web.ClientIP(r))
	if err != nil {
		msg := "We couldn't send a code. Please try again later."
		switch {
		case errors.Is(err, ErrCodeEmail):
			msg = "Enter a valid email address."
		case errors.Is(err, ErrCodeLimited):
			msg = "Too many code requests. Please try again later."
		default:
			if a.Log != nil {
				a.Log.Error("auth: requesting login code", "err", err)
			}
		}
		to := a.paths().Login
		// A failed resend must leave the browser able to use its earlier
		// challenge (unless delivery failed after it was replaced).
		if _, pending, ok := a.codeRequest(r); ok && pending == email {
			to += "/verify"
		}
		a.flash().Redirect(w, r, to, "alert", msg)
		return nil
	}
	web.SetCookie(w, r, codeCookie, a.Signer.Sign(codeCookie, challenge+"|"+email), CodeValidity)
	http.Redirect(w, r, a.paths().Login+"/verify", http.StatusSeeOther)
	return nil
}

func (a *Auth) codeRequest(r *http.Request) (challenge, email string, ok bool) {
	value, valid := web.SignedCookie(r, a.Signer, codeCookie)
	if !valid {
		return "", "", false
	}
	challenge, email, ok = strings.Cut(value, "|")
	return challenge, email, ok && challenge != "" && email != ""
}

func (a *Auth) verifyCodePage(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Cache-Control", "no-store")
	_, email, ok := a.codeRequest(r)
	if !ok {
		http.Redirect(w, r, a.paths().Login, http.StatusSeeOther)
		return nil
	}
	p := a.page(w, r)
	p.EmailAddress = email
	return a.views().VerifyCode(w, r, p)
}

func (a *Auth) confirmCode(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Cache-Control", "no-store")
	challenge, email, ok := a.codeRequest(r)
	if !ok {
		http.Redirect(w, r, a.paths().Login, http.StatusSeeOther)
		return nil
	}
	if a.Attempt != nil && !a.Attempt(r) {
		a.flash().Redirect(w, r, a.paths().Login+"/verify", "alert", msgTooMany)
		return nil
	}
	u, err := a.VerifyLoginCode(r.Context(), challenge, r.PostFormValue("code"), web.ClientIP(r))
	if err != nil {
		msg := "That code is incorrect, expired, or already used."
		if errors.Is(err, ErrBadCode) && a.Failed != nil {
			a.Failed(r, email)
		}
		if errors.Is(err, ErrCodeLimited) {
			msg = "Too many attempts. Please try again later."
		}
		if !errors.Is(err, ErrBadCode) && !errors.Is(err, ErrCodeLimited) {
			return err
		}
		a.flash().Redirect(w, r, a.paths().Login+"/verify", "alert", msg)
		return nil
	}
	if _, err := a.StartSession(w, r, u); err != nil {
		return err
	}
	web.ClearCookie(w, codeCookie)
	a.afterLogin(w, r, http.StatusSeeOther)
	return nil
}
