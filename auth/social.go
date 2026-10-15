package auth

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	netmail "net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/token"
	"github.com/scttymn/gantry/web"
	"golang.org/x/oauth2"
)

const socialValidity = 10 * time.Minute

// Social adds provider identities to the same users and database sessions as
// password and email-code auth. Apply the social recipe's migration first.
// Signup is explicit; safe auto-linking to a verified email is always enabled.
type Social struct {
	Providers []*SocialProvider
	Signup    bool
	// CreateUser inserts and returns a new user using tx. It is called only
	// after verified, authoritative email proof and only when Signup is enabled.
	CreateUser func(context.Context, *db.Tx, SocialIdentity) (User, error)
}

type SocialButton struct {
	Name, Label, StartURL, LinkURL string
	Linked                         bool
}

func (a *Auth) socialButtons() []SocialButton {
	if a.Social == nil {
		return nil
	}
	buttons := make([]SocialButton, 0, len(a.Social.Providers))
	for _, p := range a.Social.Providers {
		buttons = append(buttons, SocialButton{Name: p.name, Label: p.label, StartURL: a.paths().Login + "/" + p.name, LinkURL: a.paths().Login + "/" + p.name + "/link"})
	}
	return buttons
}

func (a *Auth) socialRoutes(rt web.Routes) {
	if a.Social == nil {
		return
	}
	if a.DB == nil || len(a.Social.Providers) == 0 {
		panic("auth: Social needs a database and providers")
	}
	seen := map[string]bool{}
	for _, p := range a.Social.Providers {
		if p == nil || p.keys == nil || seen[p.name] {
			panic("auth: social providers must be configured and unique")
		}
		seen[p.name] = true
		base := a.paths().Login + "/" + p.name
		rt.Handle("POST "+base, func(w http.ResponseWriter, r *http.Request) error { return a.startSocial(w, r, p, false) })
		rt.Handle("POST "+base+"/link", func(w http.ResponseWriter, r *http.Request) error { return a.startSocial(w, r, p, true) })
		callback := func(w http.ResponseWriter, r *http.Request) error { return a.finishSocial(w, r, p) }
		if p.name == "apple" {
			special, ok := rt.(interface{ CrossOriginCallback(string, web.Handler) })
			if !ok {
				panic("auth: Apple requires web.Router or web.Scope for its protected cross-site callback")
			}
			special.CrossOriginCallback("POST "+base+"/callback", callback)
		} else {
			rt.Handle("GET "+base+"/callback", callback)
		}
	}
	// Fail early on missing app-owned schema, without provider network calls.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, query := range []string{`SELECT provider,subject,user_id,email_address,verified_at FROM auth_identities LIMIT 0`, `SELECT state_digest,provider,browser_digest,nonce,verifier,link_session_id,expires_at FROM auth_oauth_states LIMIT 0`} {
		rows, err := a.DB.Read.QueryContext(ctx, query)
		if err != nil {
			panic("auth: apply the social identities migration before enabling Social")
		}
		rows.Close()
	}
	rt.Handle("GET "+a.paths().Login+"/identities", a.identitiesPage)
}

func (p *SocialProvider) flowCookie() string {
	name := "gantry_oauth_" + p.name + "_" + token.Digest(p.config.RedirectURL)[:12]
	if strings.HasPrefix(p.config.RedirectURL, "https:") {
		name = "__Host-" + name
	}
	return name
}
func (p *SocialProvider) setFlowCookie(w http.ResponseWriter, value string, age int) {
	sameSite := http.SameSiteLaxMode
	if p.name == "apple" {
		sameSite = http.SameSiteNoneMode
	}
	http.SetCookie(w, &http.Cookie{Name: p.flowCookie(), Value: value, Path: "/", HttpOnly: true, Secure: strings.HasPrefix(p.config.RedirectURL, "https:"), SameSite: sameSite, MaxAge: age})
}

func (a *Auth) startSocial(w http.ResponseWriter, r *http.Request, p *SocialProvider, link bool) error {
	w.Header().Set("Cache-Control", "no-store")
	if !a.allow(r, "social") || (a.Attempt != nil && !a.Attempt(r)) {
		return web.Status(429, nil)
	}
	if pending, err := a.FirstUserPending(r.Context()); err != nil {
		return err
	} else if pending {
		return a.socialError(w, r, "Complete first-account setup before using social sign-in.")
	}
	var sessionID int64
	if link {
		_, s, ok := a.sessionOf(r)
		if !ok {
			return a.linkSignIn(w, r)
		}
		var created time.Time
		if err := a.DB.Read.QueryRowContext(r.Context(), `SELECT created_at FROM sessions WHERE id=$1`, s.ID).Scan(&created); err != nil {
			return err
		}
		if a.now().Sub(created) > socialValidity {
			return a.linkSignIn(w, r)
		}
		sessionID = s.ID
	}
	state, digest := token.New("")
	browser, browserDigest := token.New("")
	nonce, _ := token.New("")
	verifier := oauth2.GenerateVerifier()
	if _, err := a.DB.Write.ExecContext(r.Context(), `INSERT INTO auth_oauth_states (state_digest,provider,browser_digest,nonce,verifier,link_session_id,expires_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`, digest, p.name, browserDigest, nonce, verifier, sessionID, a.now().Add(socialValidity).Unix()); err != nil {
		return err
	}
	p.setFlowCookie(w, browser, int(socialValidity/time.Second))
	http.Redirect(w, r, p.authorization(state, nonce, verifier), http.StatusSeeOther)
	return nil
}

func (a *Auth) linkSignIn(w http.ResponseWriter, r *http.Request) error {
	web.SetCookie(w, r, returnToCookie, a.Signer.Sign(returnToCookie, a.paths().Login+"/identities"), time.Hour)
	return a.socialError(w, r, "Sign in with your existing method, then connect your provider under Sign-in methods.")
}
func (a *Auth) socialError(w http.ResponseWriter, r *http.Request, message string) error {
	a.flash().Set(w, r, "alert", message)
	http.Redirect(w, r, a.paths().Login, http.StatusSeeOther)
	return nil
}

type socialFlow struct {
	nonce, verifier string
	sessionID       int64
}

func (a *Auth) finishSocial(w http.ResponseWriter, r *http.Request, p *SocialProvider) error {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	var values url.Values
	if p.name == "apple" {
		if r.Method != "POST" {
			return web.Status(405, nil)
		}
		r.Body = http.MaxBytesReader(w, r.Body, 32768)
		if err := r.ParseForm(); err != nil {
			return web.Status(400, nil)
		}
		values = r.PostForm
	} else {
		if r.Method != "GET" {
			return web.Status(405, nil)
		}
		values = r.URL.Query()
	}
	cookie, err := r.Cookie(p.flowCookie())
	if err != nil || cookie.Value == "" || len(values["state"]) != 1 || len(values.Get("state")) != 43 || len(values["code"]) > 1 || len(values.Get("code")) > 8192 {
		return web.Status(400, ErrSocialProof)
	}
	var flow socialFlow
	err = a.DB.Write.QueryRowContext(r.Context(), `DELETE FROM auth_oauth_states WHERE state_digest=$1 AND provider=$2 AND browser_digest=$3 AND expires_at>$4 RETURNING nonce,verifier,link_session_id`, token.Digest(values.Get("state")), p.name, token.Digest(cookie.Value), a.now().Unix()).Scan(&flow.nonce, &flow.verifier, &flow.sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return web.Status(400, ErrSocialProof)
	}
	if err != nil {
		return err
	}
	p.setFlowCookie(w, "", -1)
	if values.Get("error") != "" {
		return a.socialError(w, r, "Sign-in was canceled. You can try again.")
	}
	if values.Get("code") == "" {
		return web.Status(400, ErrSocialProof)
	}
	identity, err := p.exchange(r.Context(), values.Get("code"), flow.nonce, flow.verifier, a.now())
	if err != nil {
		return a.socialError(w, r, "We couldn't verify that sign-in. Please try again.")
	}
	user, err := a.socialUser(r, identity, flow.sessionID)
	switch {
	case errors.Is(err, ErrSocialLinkRequired):
		return a.linkSignIn(w, r)
	case errors.Is(err, ErrNoUser):
		return a.socialError(w, r, "This account isn't enabled for sign-in. Contact the app owner.")
	case errors.Is(err, ErrSocialProof):
		return a.socialError(w, r, "Verify your email using an existing sign-in method before connecting this provider.")
	case errors.Is(err, ErrSetupClosed):
		return a.socialError(w, r, "Complete first-account setup before using social sign-in.")
	case err != nil:
		return err
	}
	// Every mechanism creates the ordinary revocable database session.
	if _, err := a.StartSession(w, r, user); err != nil {
		return err
	}
	if flow.sessionID != 0 {
		a.flash().Set(w, r, "notice", "Sign-in method connected.")
		http.Redirect(w, r, a.paths().Login+"/identities", http.StatusSeeOther)
	} else {
		a.afterLogin(w, r, http.StatusSeeOther)
	}
	return nil
}

func validIdentityEmail(i SocialIdentity) bool {
	parsed, err := netmail.ParseAddress(i.Email)
	return i.EmailVerified && err == nil && parsed.Address == i.Email && len(i.Email) <= 254 && !strings.ContainsAny(i.Email, "\r\n")
}

func (a *Auth) socialUser(r *http.Request, i SocialIdentity, linkSession int64) (User, error) {
	var user User
	err := a.DB.Tx(r.Context(), func(tx *db.Tx) error {
		ctx := r.Context()
		var linkedUser int64
		if linkSession != 0 {
			var created, seen time.Time
			var boundTo string
			bound := "''"
			if a.Bind != nil {
				bound = "bound_to"
			}
			query := `SELECT user_id,created_at,last_seen_at,` + bound + ` FROM sessions WHERE id=$1`
			if a.DB.Engine == db.Postgres {
				query += ` FOR UPDATE`
			}
			err := tx.QueryRowContext(ctx, query, linkSession).Scan(&linkedUser, &created, &seen, &boundTo)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrSocialLinkRequired
			}
			if err != nil {
				return err
			}
			if a.ended(created, seen, a.now()) || a.now().Sub(created) > socialValidity || (a.Bind != nil && a.Bind(r) != boundTo) {
				return ErrSocialLinkRequired
			}
		}
		var owner int64
		err := tx.QueryRowContext(ctx, `SELECT user_id FROM auth_identities WHERE provider=$1 AND subject=$2`, i.Provider, i.Subject).Scan(&owner)
		if err == nil {
			if linkedUser != 0 && linkedUser != owner {
				return ErrSocialLinkRequired
			}
			user, err = a.findBy(ctx, tx, "id", owner)
			return err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if linkedUser != 0 {
			user, err = a.findBy(ctx, tx, "id", linkedUser)
			if err != nil {
				return err
			}
		} else {
			if !validIdentityEmail(i) {
				return ErrSocialProof
			}
			user, err = a.findBy(ctx, tx, "email_address", i.Email)
			if err == nil {
				// Auto-link only to a separately verified email identity for this user.
				// A password hash alone proves no control over its email address.
				var proof bool
				err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM auth_identities WHERE provider='email' AND subject=$1 AND user_id=$2)`, i.Email, user.ID).Scan(&proof)
				if err != nil {
					return err
				}
				if !proof || !i.AuthoritativeEmail {
					return ErrSocialLinkRequired
				}
			} else if errors.Is(err, ErrNoUser) {
				if !a.Social.Signup {
					return ErrNoUser
				}
				if !i.AuthoritativeEmail {
					return ErrSocialProof
				}
				if a.setupEnabled() {
					pending, _, err := a.setupLock(ctx, tx)
					if err != nil {
						return err
					}
					if pending {
						return ErrSetupClosed
					}
				}
				if a.Social.CreateUser != nil {
					user, err = a.Social.CreateUser(ctx, tx, i)
					if err != nil {
						return err
					}
					stored, err := a.findBy(ctx, tx, "id", user.ID)
					if err != nil || stored.EmailAddress != i.Email {
						return errors.New("auth: social signup must insert and return the verified user")
					}
					user = stored
				} else {
					now := a.now()
					err = tx.QueryRowContext(ctx, `INSERT INTO users (email_address,password_digest,created_at,updated_at) VALUES ($1,'',$2,$2) ON CONFLICT(email_address) DO NOTHING RETURNING id`, i.Email, now).Scan(&user.ID)
					if errors.Is(err, sql.ErrNoRows) {
						return ErrSocialLinkRequired
					}
					if err != nil {
						return err
					}
					user.EmailAddress = i.Email
				}
				if err := a.recordVerifiedEmail(ctx, tx, user); err != nil {
					return err
				}
			} else {
				return err
			}
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO auth_identities (provider,subject,user_id,email_address,verified_at) VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, i.Provider, i.Subject, user.ID, i.Email, a.now().Unix())
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrSocialLinkRequired
		}
		return nil
	})
	return user, err
}

// recordVerifiedEmail is called only after proof (OTP or authoritative social
// signup), inside the account transaction. Password-only accounts are not
// marked verified. Existing apps can migrate their independently verified emails.
func (a *Auth) recordVerifiedEmail(ctx context.Context, tx *db.Tx, u User) error {
	if a.Social == nil {
		return nil
	}
	// Changing the user's email invalidates the previous email identity.
	if _, err := tx.ExecContext(ctx, `DELETE FROM auth_identities WHERE provider='email' AND user_id=$1 AND subject<>$2`, u.ID, u.EmailAddress); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO auth_identities (provider,subject,user_id,email_address,verified_at) VALUES ('email',$1,$2,$1,$3) ON CONFLICT(provider,subject) DO UPDATE SET verified_at=excluded.verified_at WHERE auth_identities.user_id=excluded.user_id`, u.EmailAddress, u.ID, a.now().Unix())
	return err
}

// PruneSocialFlows removes expired browser handshakes; schedule with Gantry jobs.
func (a *Auth) PruneSocialFlows(ctx context.Context) error {
	if a.Social == nil {
		return nil
	}
	_, err := a.DB.Write.ExecContext(ctx, `DELETE FROM auth_oauth_states WHERE expires_at<=$1`, a.now().Unix())
	return err
}

func (a *Auth) identitiesPage(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Cache-Control", "no-store")
	user, _, ok := a.sessionOf(r)
	if !ok {
		return a.linkSignIn(w, r)
	}
	p := a.page(w, r)
	p.EmailAddress = user.EmailAddress
	for n, b := range p.Social {
		if err := a.DB.Read.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM auth_identities WHERE user_id=$1 AND provider=$2)`, user.ID, b.Name).Scan(&p.Social[n].Linked); err != nil {
			return err
		}
	}
	return a.views().Identities(w, r, p)
}
