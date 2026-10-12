package auth

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	netmail "net/mail"
	"os"
	"path/filepath"
	"strings"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/token"
)

// FirstUser opts password auth into installation setup. The application owns
// the auth_setup migration: one row (id=1), completed, and code_digest. New
// installs seed completed=false; existing installs must seed completed=true.
// Nil preserves existing authentication behavior. This is not public signup.
type FirstUser struct {
	// CodeFile is an absolute path on a private persistent volume. Only its
	// digest is in the database. Do not put the code in URLs, logs or source.
	CodeFile string
	// CreateUser can populate app-specific fields/roles. It receives an already
	// hashed password and must insert using tx, returning the inserted user.
	CreateUser func(ctx context.Context, tx *db.Tx, email, passwordDigest string) (User, error)
}

var (
	ErrSetupClosed = errors.New("auth: first-user setup is closed")
	ErrSetupCode   = errors.New("auth: setup code is incorrect")
)

func (a *Auth) setupEnabled() bool { return a.FirstUser != nil && a.EmailCodes == nil }

// FirstUserPending reports installation state, failing closed on missing
// schema/state or database errors. An empty users table alone never enables it.
func (a *Auth) FirstUserPending(ctx context.Context) (bool, error) {
	if !a.setupEnabled() {
		return false, nil
	}
	var pending bool
	err := a.DB.Read.QueryRowContext(ctx, `SELECT NOT completed AND NOT EXISTS (SELECT 1 FROM users) FROM auth_setup WHERE id=1`).Scan(&pending)
	if errors.Is(err, sql.ErrNoRows) {
		return false, errors.New("auth: installation setup state is missing")
	}
	return pending, err
}

func (a *Auth) setupLock(ctx context.Context, tx *db.Tx) (bool, string, error) {
	// Also serialize with ordinary user inserts, not just setup requests.
	// SQLite's write transactions already begin IMMEDIATE.
	if a.DB.Engine == db.Postgres {
		if _, err := tx.ExecContext(ctx, `LOCK TABLE users IN SHARE ROW EXCLUSIVE MODE`); err != nil {
			return false, "", err
		}
	}
	query := `SELECT completed, code_digest FROM auth_setup WHERE id=1`
	if a.DB.Engine == db.Postgres {
		query += ` FOR UPDATE`
	}
	var completed bool
	var digest string
	err := tx.QueryRowContext(ctx, query).Scan(&completed, &digest)
	if err != nil {
		return false, "", err
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM users)`).Scan(&exists); err != nil {
		return false, "", err
	}
	if completed || exists {
		if !completed {
			if err := a.completeSetup(ctx, tx); err != nil {
				return false, "", err
			}
		}
		return false, "", nil
	}
	return true, digest, nil
}

func (a *Auth) completeSetup(ctx context.Context, tx *db.Tx) error {
	_, err := tx.ExecContext(ctx, `UPDATE auth_setup SET completed=true, code_digest='' WHERE id=1`)
	return err
}

// PrepareFirstUser is called at startup before serving requests. A fresh
// installation gets a random code; restarts preserve it. Existing users close
// setup permanently. The code is never logged. Missing state is an error.
func (a *Auth) PrepareFirstUser(ctx context.Context) error {
	if !a.setupEnabled() {
		return nil
	}
	return a.prepareSetup(ctx, false)
}

func (a *Auth) prepareSetup(ctx context.Context, rotate bool) error {
	if !a.setupEnabled() {
		return ErrSetupClosed
	}
	if a.DB == nil || !filepath.IsAbs(a.FirstUser.CodeFile) {
		return errors.New("auth: first-user setup needs a database and an absolute CodeFile on persistent storage")
	}
	return a.DB.Tx(ctx, func(tx *db.Tx) error {
		pending, digest, err := a.setupLock(ctx, tx)
		if err != nil {
			return err
		}
		if !pending {
			tx.AfterCommit(a.removeSetupCode)
			return nil
		}
		if digest != "" && !rotate {
			return nil
		}
		code, digest := token.New("setup_")
		if err := writeSetupCode(a.FirstUser.CodeFile, code); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE auth_setup SET code_digest=$1 WHERE id=1`, digest)
		return err
	})
}

// FirstUserCode is for an operator command over a trusted terminal (e.g.
// Houston's SSH connection), never an HTTP handler. rotate replaces a lost
// code only while setup is still pending; it cannot reopen a completed setup.
func (a *Auth) FirstUserCode(ctx context.Context, rotate bool) (string, error) {
	if err := a.prepareSetup(ctx, rotate); err != nil {
		return "", err
	}
	var code string
	err := a.DB.Tx(ctx, func(tx *db.Tx) error {
		pending, digest, err := a.setupLock(ctx, tx)
		if err != nil {
			return err
		}
		if !pending {
			return ErrSetupClosed
		}
		info, err := os.Lstat(a.FirstUser.CodeFile)
		if err != nil {
			return errors.New("auth: setup code file is unavailable; an operator can rotate the pending code")
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return errors.New("auth: setup code must be a regular file with mode 0600")
		}
		raw, err := os.ReadFile(a.FirstUser.CodeFile)
		if err != nil {
			return err
		}
		code = strings.TrimSpace(string(raw))
		if !token.Equal(token.Digest(code), digest) {
			return errors.New("auth: setup code file does not match this installation; rotate the pending code")
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return code, nil
}

func writeSetupCode(path, code string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".setup-code-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(code + "\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func (a *Auth) removeSetupCode() {
	if err := os.Remove(a.FirstUser.CodeFile); err != nil && !errors.Is(err, os.ErrNotExist) && a.Log != nil {
		a.Log.Warn("auth: could not remove consumed setup code file", "err", err)
	}
}

func (a *Auth) setupPage(w http.ResponseWriter, r *http.Request, alert, email string, status int) error {
	w.Header().Set("Cache-Control", "no-store")
	p := a.page(w, r)
	p.EmailAddress = email
	p.Paths.Passwords = ""
	if alert != "" {
		p.Alert = alert
	}
	// Views use Page.Status so generated web.Render wrappers preserve 422.
	p.Status = status
	return a.views().Setup(w, r, p)
}

func (a *Auth) setupUser(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Cache-Control", "no-store")
	if !a.setupEnabled() {
		return a.setupClosed(w, r)
	}
	if !a.allow(r, "setup") || (a.Attempt != nil && !a.Attempt(r)) {
		return a.setupPage(w, r, msgTooMany, "", http.StatusTooManyRequests)
	}
	code := strings.TrimSpace(r.PostFormValue("setup_code"))
	email := Normalize(r.PostFormValue("email_address"))
	// Check the random code before doing password hashing or reporting details.
	var digest string
	err := a.DB.Read.QueryRowContext(r.Context(), `SELECT code_digest FROM auth_setup WHERE id=1 AND NOT completed`).Scan(&digest)
	if errors.Is(err, sql.ErrNoRows) {
		return a.setupClosed(w, r)
	}
	if err != nil {
		return err
	}
	if digest == "" || !token.Equal(token.Digest(code), digest) {
		return a.setupPage(w, r, "The setup code is incorrect.", email, http.StatusUnprocessableEntity)
	}
	address, err := netmail.ParseAddress(email)
	if err != nil || address.Address != email || len(email) > 254 || strings.ContainsAny(email, "\r\n") {
		return a.setupPage(w, r, "Enter a valid email address.", email, http.StatusUnprocessableEntity)
	}
	password, confirmation := r.PostFormValue("password"), r.PostFormValue("password_confirmation")
	if errs := a.rules(password, confirmation); len(errs) > 0 {
		return a.setupPage(w, r, sentence(errs), email, http.StatusUnprocessableEntity)
	}
	passwordDigest, err := Hash(password)
	if err != nil {
		return err
	}
	var user User
	closed := false
	err = a.DB.Tx(r.Context(), func(tx *db.Tx) error {
		pending, currentDigest, err := a.setupLock(r.Context(), tx)
		if err != nil {
			return err
		}
		if !pending {
			closed = true
			return nil
		}
		if currentDigest == "" || !token.Equal(token.Digest(code), currentDigest) {
			return ErrSetupCode
		}
		if a.FirstUser.CreateUser != nil {
			user, err = a.FirstUser.CreateUser(r.Context(), tx, email, passwordDigest)
			if err != nil {
				return err
			}
			stored, err := a.findBy(r.Context(), tx, "id", user.ID)
			if err != nil || stored.EmailAddress != email || stored.PasswordDigest != passwordDigest {
				return errors.New("auth: first-user hook must insert and return the account for this email and password digest")
			}
			user = stored
		} else {
			user = User{EmailAddress: email, PasswordDigest: passwordDigest}
			now := a.now()
			err = tx.QueryRowContext(r.Context(), `INSERT INTO users(email_address,password_digest,created_at,updated_at) VALUES($1,$2,$3,$3) RETURNING id`, email, passwordDigest, now).Scan(&user.ID)
			if err != nil {
				return err
			}
		}
		if err := a.completeSetup(r.Context(), tx); err != nil {
			return err
		}
		tx.AfterCommit(a.removeSetupCode)
		return nil
	})
	if errors.Is(err, ErrSetupCode) {
		return a.setupPage(w, r, "The setup code changed. Retrieve the current code and try again.", email, http.StatusUnprocessableEntity)
	}
	if err != nil {
		return err
	}
	if closed {
		return a.setupClosed(w, r)
	}
	if _, err := a.StartSession(w, r, user); err != nil {
		return err
	}
	a.afterLogin(w, r, http.StatusSeeOther)
	return nil
}

func (a *Auth) setupClosed(w http.ResponseWriter, r *http.Request) error {
	a.flash().Redirect(w, r, a.paths().Login, "notice", "Setup is complete. Sign in with your account.")
	return nil
}
