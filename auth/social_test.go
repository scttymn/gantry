package auth

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/web"
	"golang.org/x/oauth2"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

func TestSocialConfiguration(t *testing.T) {
	if _, err := NewGoogle(GoogleConfig{}); err == nil {
		t.Fatal("missing Google credentials accepted")
	}
	if _, err := NewGoogle(GoogleConfig{ClientID: "id", ClientSecret: "secret", RedirectURL: "http://example.com/login/google/callback"}); err == nil {
		t.Fatal("insecure remote callback accepted")
	}
	if _, err := NewGoogle(GoogleConfig{ClientID: "id", ClientSecret: "secret", RedirectURL: "http://localhost:8080/login/google/callback"}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewApple(AppleConfig{ClientID: "id", TeamID: "team", KeyID: "key", RedirectURL: "https://app.example/login/apple/callback", PrivateKey: []byte("bad")}); err == nil {
		t.Fatal("bad Apple key accepted")
	}
}

type socialFixture struct {
	a                        *Auth
	now                      *time.Time
	p                        *SocialProvider
	h                        http.Handler
	key                      *rsa.PrivateKey
	clientKey                *ecdsa.PrivateKey
	mu                       sync.Mutex
	codes                    map[string]map[string]any
	exchangeCount, jwksCount int
	lastToken                url.Values
	fail                     bool
}

type socialTransport func(*http.Request) (*http.Response, error)

func (f socialTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newSocialFixture(t *testing.T, engine, provider string) *socialFixture {
	t.Helper()
	a, _, now := newCodeApp(t, engine)
	raw, err := os.ReadFile("../cmd/gantry/templates/social/migration.sql.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Migrate(t.Context(), fstest.MapFS{"00001_social.sql": {Data: raw}}, "social_migrations"); err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &socialFixture{a: a, now: now, key: key, codes: map[string]map[string]any{}}
	client := &http.Client{Transport: socialTransport(func(r *http.Request) (*http.Response, error) { return f.serve(t, r) })}
	if provider == "google" {
		f.p, err = NewGoogle(GoogleConfig{ClientID: "client", ClientSecret: "secret", RedirectURL: "https://app.example/login/google/callback", HTTPClient: client})
	} else {
		f.clientKey, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		der, _ := x509.MarshalPKCS8PrivateKey(f.clientKey)
		f.p, err = NewApple(AppleConfig{ClientID: "client", TeamID: "team", KeyID: "key", PrivateKey: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), RedirectURL: "https://app.example/login/apple/callback", HTTPClient: client})
	}
	if err != nil {
		t.Fatal(err)
	}
	a.Social = &Social{Providers: []*SocialProvider{f.p}, Signup: true}
	f.routes()
	return f
}
func (f *socialFixture) routes() {
	rt := web.NewRouter(slog.New(slog.DiscardHandler), nil)
	f.a.Routes(rt)
	rt.Mount("GET /account", f.a.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { u, _ := Current(r); fmt.Fprint(w, u.ID) })))
	f.h = rt.Handler()
}
func (f *socialFixture) serve(t *testing.T, r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return nil, errors.New("upstream secret response must never appear")
	}
	var body []byte
	if strings.Contains(r.URL.Path, "certs") || strings.HasSuffix(r.URL.Path, "keys") {
		f.jwksCount++
		body, _ = json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &f.key.PublicKey, KeyID: "test", Algorithm: "RS256", Use: "sig"}}})
	} else {
		f.exchangeCount++
		r.ParseForm()
		f.lastToken = r.PostForm
		claims := f.codes[r.PostForm.Get("code")]
		if claims == nil {
			return &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":"invalid_grant","error_description":"private"}`))}, nil
		}
		signer, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: f.key}, (&jose.SignerOptions{}).WithHeader("kid", "test"))
		signed, err := jwt.Signed(signer).Claims(claims).Serialize()
		if err != nil {
			t.Fatal(err)
		}
		body, _ = json.Marshal(map[string]any{"access_token": "unused-access-token", "token_type": "Bearer", "id_token": signed, "refresh_token": "never-persist"})
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(body))}, nil
}
func (f *socialFixture) request(method, path string, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	r := httptest.NewRequest(method, "https://app.example"+path, body)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if method == "POST" && strings.HasSuffix(path, "/apple/callback") {
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		r.Header.Set("Origin", "https://appleid.apple.com")
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}
func (f *socialFixture) begin(t *testing.T, link bool, cookies ...*http.Cookie) (url.Values, *http.Cookie) {
	t.Helper()
	path := "/login/" + f.p.name
	if link {
		path += "/link"
	}
	w := f.request("POST", path, nil, cookies...)
	if w.Code != 303 {
		t.Fatalf("start %d %s", w.Code, w.Body)
	}
	u, err := url.Parse(w.Header().Get("Location"))
	if err != nil || u.Host == "" {
		t.Fatalf("no provider redirect: %s", w.Header().Get("Location"))
	}
	var cookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == f.p.flowCookie() {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no browser binding")
	}
	if !cookie.HttpOnly || !cookie.Secure || cookie.Path != "/" {
		t.Fatal("unsafe flow cookie", cookie)
	}
	if f.p.name == "apple" && cookie.SameSite != http.SameSiteNoneMode {
		t.Fatal("Apple must send cookie on form_post")
	}
	return u.Query(), cookie
}
func (f *socialFixture) issue(v url.Values, override map[string]any) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	code := fmt.Sprintf("code-%d", len(f.codes))
	claims := map[string]any{"iss": f.p.issuer, "aud": "client", "sub": "subject-1", "email": "person@gmail.com", "email_verified": true, "nonce": v.Get("nonce"), "iat": f.a.now().Unix(), "exp": f.a.now().Add(time.Hour).Unix()}
	for k, v := range override {
		claims[k] = v
	}
	f.codes[code] = claims
	return code
}
func (f *socialFixture) finish(v url.Values, code string, cookie *http.Cookie) *httptest.ResponseRecorder {
	values := url.Values{"state": {v.Get("state")}, "code": {code}}
	if f.p.name == "apple" {
		return f.request("POST", "/login/apple/callback", values, cookie)
	}
	return f.request("GET", "/login/google/callback?"+values.Encode(), nil, cookie)
}
func sessionCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == "session" && c.MaxAge > 0 {
			return c
		}
	}
	t.Fatalf("no session: %d %s %s", w.Code, w.Header(), w.Body)
	return nil
}

func TestSocialRoundTrip(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		for _, provider := range []string{"google", "apple"} {
			t.Run(engine+"/"+provider, func(t *testing.T) {
				f := newSocialFixture(t, engine, provider)
				v, c := f.begin(t, false)
				if v.Get("state") == "" || v.Get("nonce") == "" || v.Get("redirect_uri") != f.p.config.RedirectURL {
					t.Fatal(v)
				}
				if provider == "google" && (v.Get("code_challenge_method") != "S256" || v.Get("code_challenge") == "") {
					t.Fatal("no PKCE", v)
				}
				if provider == "apple" && v.Get("response_mode") != "form_post" {
					t.Fatal(v)
				}
				code := f.issue(v, nil)
				w := f.finish(v, code, c)
				session := sessionCookie(t, w)
				if w.Header().Get("Location") != "/" {
					t.Fatal(w.Header())
				}
				u, err := f.a.FindByEmail(t.Context(), "person@gmail.com")
				if err != nil {
					t.Fatal(err)
				}
				if u.PasswordDigest != "" {
					t.Fatal("social signup created a password")
				}
				if f.request("GET", "/account", nil, session).Code != 200 {
					t.Fatal("not a normal session")
				}
				if f.lastToken.Get("redirect_uri") != f.p.config.RedirectURL || f.lastToken.Get("client_id") != "client" {
					t.Fatal(f.lastToken)
				}
				if provider == "google" && oauth2.S256ChallengeFromVerifier(f.lastToken.Get("code_verifier")) != v.Get("code_challenge") {
					t.Fatal("PKCE mismatch")
				}
				if provider == "apple" {
					signed, err := jwt.ParseSigned(f.lastToken.Get("client_secret"), []jose.SignatureAlgorithm{jose.ES256})
					if err != nil {
						t.Fatal(err)
					}
					var claims jwt.Claims
					if err := signed.Claims(&f.clientKey.PublicKey, &claims); err != nil {
						t.Fatal(err)
					}
					if claims.Issuer != "team" || claims.Subject != "client" || len(claims.Audience) != 1 || claims.Audience[0] != "https://appleid.apple.com" || claims.Expiry.Time().Sub(f.a.now()) != time.Hour {
						t.Fatal("bad Apple client secret")
					}
				}
				if f.finish(v, code, c).Code != 400 {
					t.Fatal("replayed flow accepted")
				}
				// Provider subject stays linked when email changes; no duplicate user.
				v, c = f.begin(t, false)
				w = f.finish(v, f.issue(v, map[string]any{"email": "changed@example.com", "email_verified": false}), c)
				session = sessionCookie(t, w)
				var count int
				f.a.DB.Read.QueryRow(`SELECT count(*) FROM users`).Scan(&count)
				if count != 1 {
					t.Fatal(count)
				}
				if f.jwksCount != 1 {
					t.Fatal("signing keys were not cached", f.jwksCount)
				}
				f.a.Lifetime = time.Minute
				*f.now = f.now.Add(2 * time.Minute)
				if f.request("GET", "/account", nil, session).Code != 302 {
					t.Fatal("social session did not expire")
				}
			})
		}
	}
}

func TestSocialRejectsBadProof(t *testing.T) {
	tests := []struct {
		name   string
		claims map[string]any
	}{
		{"issuer", map[string]any{"iss": "https://attacker.example"}},
		{"audience", map[string]any{"aud": "other-client"}},
		{"azp", map[string]any{"azp": "other-client"}},
		{"multiple audiences", map[string]any{"aud": []string{"client", "other"}}},
		{"nonce", map[string]any{"nonce": "wrong"}},
		{"expired", map[string]any{"exp": int64(0)}},
		{"no issued time", map[string]any{"iat": int64(0)}},
		{"no subject", map[string]any{"sub": ""}},
		{"email unverified", map[string]any{"email_verified": false}},
		{"malformed email", map[string]any{"email": "Name <person@gmail.com>"}},
		{"non-authoritative email", map[string]any{"email": "person@example.com"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newSocialFixture(t, "sqlite", "google")
			v, c := f.begin(t, false)
			w := f.finish(v, f.issue(v, tc.claims), c)
			for _, cookie := range w.Result().Cookies() {
				if cookie.Name == "session" {
					t.Fatal("bad proof signed in")
				}
			}
			var n int
			f.a.DB.Read.QueryRow(`SELECT count(*) FROM users`).Scan(&n)
			if n != 0 {
				t.Fatal("bad proof created user")
			}
		})
	}
}

func TestSocialStateProtection(t *testing.T) {
	for _, mode := range []string{"cookie", "state", "expired", "missing", "denied", "provider-error", "signature"} {
		t.Run(mode, func(t *testing.T) {
			f := newSocialFixture(t, "sqlite", "google")
			v, c := f.begin(t, false)
			code := f.issue(v, nil)
			switch mode {
			case "cookie":
				c.Value = "someone-else"
			case "state":
				v.Set("state", strings.Repeat("x", 43))
			case "expired":
				*f.now = f.now.Add(socialValidity)
			case "missing":
				c.Name = "other"
			case "denied":
				code = "invalid-grant"
			case "provider-error":
				f.fail = true
			case "signature":
				// Cache an unrelated public key before signing the response with a new key.
				payload, _ := json.Marshal(map[string]any{"iss": f.p.issuer, "aud": "client", "sub": "none", "nonce": "x", "exp": f.a.now().Add(time.Hour).Unix(), "iat": f.a.now().Unix()})
				signer, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: f.key}, (&jose.SignerOptions{}).WithHeader("kid", "test"))
				j, _ := signer.Sign(payload)
				raw, _ := j.CompactSerialize()
				f.p.keys.VerifySignature(t.Context(), raw)
				old := f.key
				f.key, _ = rsa.GenerateKey(rand.Reader, 2048)
				f.p.keys = &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&old.PublicKey}}
			}
			w := f.finish(v, code, c)
			for _, cookie := range w.Result().Cookies() {
				if cookie.Name == "session" {
					t.Fatal("invalid callback signed in")
				}
			}
			if strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), "private") {
				t.Fatal("provider error leaked")
			}
			if mode == "cookie" || mode == "state" || mode == "expired" || mode == "missing" {
				if w.Code != 400 || f.exchangeCount != 0 {
					t.Fatal("unbound callback reached provider", w.Code)
				}
			}
		})
	}
}

func TestSocialLinking(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			f := newSocialFixture(t, engine, "google")
			user, err := f.a.CreateUser(t.Context(), "person@gmail.com", password)
			if err != nil {
				t.Fatal(err)
			}
			v, c := f.begin(t, false)
			w := f.finish(v, f.issue(v, nil), c)
			for _, cookie := range w.Result().Cookies() {
				if cookie.Name == "session" {
					t.Fatal("unverified password email auto-linked")
				}
			}
			// OTP proves the existing user's email; the next social login auto-links.
			challenge, err := f.a.RequestLoginCode(t.Context(), user.EmailAddress, "ip")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.a.VerifyLoginCode(t.Context(), challenge, f.a.Mail.(*codeMail).code(t), "ip"); err != nil {
				t.Fatal(err)
			}
			v, c = f.begin(t, false)
			w = f.finish(v, f.issue(v, nil), c)
			session := sessionCookie(t, w)
			var owner int64
			f.a.DB.Read.QueryRow(`SELECT user_id FROM auth_identities WHERE provider='google'`).Scan(&owner)
			if owner != user.ID {
				t.Fatal("not linked to existing user", owner, user.ID)
			}
			var n int
			f.a.DB.Read.QueryRow(`SELECT count(*) FROM users`).Scan(&n)
			if n != 1 {
				t.Fatal("duplicate user")
			}
			// A different subject cannot replace an already-linked Google identity.
			v, c = f.begin(t, true, session)
			w = f.finish(v, f.issue(v, map[string]any{"sub": "different", "email": "different@gmail.com"}), c)
			for _, cookie := range w.Result().Cookies() {
				if cookie.Name == "session" {
					t.Fatal("replaced existing identity")
				}
			}
			// Revoking the database session stops linking, including Apple's callback
			// where the ordinary SameSite=Lax session cookie would not be present.
			v, c = f.begin(t, true, session)
			f.a.DB.Write.Exec(`DELETE FROM sessions`)
			w = f.finish(v, f.issue(v, nil), c)
			for _, cookie := range w.Result().Cookies() {
				if cookie.Name == "session" {
					t.Fatal("revoked session linked")
				}
			}
		})
	}
}

func TestSocialSignupPolicyAndRollback(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			f := newSocialFixture(t, engine, "apple")
			f.a.Social.Signup = false
			v, c := f.begin(t, false)
			w := f.finish(v, f.issue(v, map[string]any{"email": "relay@privaterelay.appleid.com", "email_verified": "true"}), c)
			for _, cookie := range w.Result().Cookies() {
				if cookie.Name == "session" {
					t.Fatal("signup disabled")
				}
			}
			f.a.Social.Signup = true
			f.a.Social.CreateUser = func(ctx context.Context, tx *db.Tx, i SocialIdentity) (User, error) {
				_, err := tx.ExecContext(ctx, `INSERT INTO users(email_address,password_digest,created_at,updated_at)VALUES($1,'',$2,$2)`, i.Email, f.a.now())
				if err != nil {
					return User{}, err
				}
				return User{}, errors.New("profile rejected")
			}
			v, c = f.begin(t, false)
			w = f.finish(v, f.issue(v, nil), c)
			if w.Code != 500 {
				t.Fatal("hook failure not surfaced")
			}
			var n int
			f.a.DB.Read.QueryRow(`SELECT count(*) FROM users`).Scan(&n)
			if n != 0 {
				t.Fatal("failed signup wasn't rolled back")
			}
			f.a.Social.CreateUser = nil
			v, c = f.begin(t, false)
			sessionCookie(t, f.finish(v, f.issue(v, map[string]any{"email": "relay@privaterelay.appleid.com", "email_verified": "true"}), c))
		})
	}
}

func TestSocialConcurrentCallback(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			f := newSocialFixture(t, engine, "google")
			v, c := f.begin(t, false)
			code := f.issue(v, nil)
			var wg sync.WaitGroup
			results := make(chan int, 4)
			for n := 0; n < 4; n++ {
				wg.Add(1)
				go func() { defer wg.Done(); results <- f.finish(v, code, c).Code }()
			}
			wg.Wait()
			close(results)
			success := 0
			for status := range results {
				if status == 303 {
					success++
				} else if status != 400 {
					t.Fatal(status)
				}
			}
			if success != 1 || f.exchangeCount != 1 {
				t.Fatal("replay raced", success, f.exchangeCount)
			}
			var sessions int
			f.a.DB.Read.QueryRow(`SELECT count(*) FROM sessions`).Scan(&sessions)
			if sessions != 1 {
				t.Fatal("duplicate session", sessions)
			}
		})
	}
}

func TestSocialExplicitLinkAndFirstSetup(t *testing.T) {
	for _, provider := range []string{"google", "apple"} {
		t.Run(provider, func(t *testing.T) {
			f := newSocialFixture(t, "sqlite", provider)
			u, err := f.a.CreateUser(t.Context(), "existing@example.com", password)
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			r := httptest.NewRequest("POST", "https://app.example/login", nil)
			if _, err := f.a.StartSession(w, r, u); err != nil {
				t.Fatal(err)
			}
			session := sessionCookie(t, w)
			v, c := f.begin(t, true, session)
			w = f.finish(v, f.issue(v, map[string]any{"email": "relay@privaterelay.appleid.com"}), c)
			sessionCookie(t, w)
			var id int64
			f.a.DB.Read.QueryRow(`SELECT user_id FROM auth_identities WHERE provider=$1`, provider).Scan(&id)
			if id != u.ID {
				t.Fatal("explicit link made wrong user")
			}
			*f.now = f.now.Add(socialValidity + time.Second)
			w = f.request("POST", "/login/"+provider+"/link", nil, session)
			if w.Header().Get("Location") != "/login" {
				t.Fatal("old session can link", w.Header())
			}
		})
	}
	f := newSocialFixture(t, "sqlite", "google")
	_, err := f.a.DB.Write.Exec(`CREATE TABLE auth_setup (id integer PRIMARY KEY,completed boolean NOT NULL,code_digest text NOT NULL);INSERT INTO auth_setup VALUES(1,false,'')`)
	if err != nil {
		t.Fatal(err)
	}
	f.a.EmailCodes = nil
	f.a.FirstUser = &FirstUser{CodeFile: filepath.Join(t.TempDir(), "code")}
	w := f.request("POST", "/login/google", nil)
	if w.Header().Get("Location") != "/login" {
		t.Fatal("social bypassed installation setup")
	}
	var n int
	f.a.DB.Read.QueryRow(`SELECT count(*) FROM auth_oauth_states`).Scan(&n)
	if n != 0 {
		t.Fatal("flow created before first install")
	}
}

func TestSocialEnvAndPruning(t *testing.T) {
	for _, env := range []map[string]string{{"GOOGLE_CLIENT_ID": "partial"}, {"APPLE_TEAM_ID": "partial"}, {"AUTH_SOCIAL_SIGNUP": "typo"}} {
		if _, err := SocialFromEnv(func(k string) string { return env[k] }); err == nil {
			t.Fatal("incomplete configuration accepted")
		}
	}
	if s, err := SocialFromEnv(func(string) string { return "" }); err != nil || s != nil {
		t.Fatal("empty config changed auth")
	}
	f := newSocialFixture(t, "sqlite", "google")
	f.begin(t, false)
	*f.now = f.now.Add(socialValidity)
	if err := f.a.PruneSocialFlows(t.Context()); err != nil {
		t.Fatal(err)
	}
	var n int
	f.a.DB.Read.QueryRow(`SELECT count(*) FROM auth_oauth_states`).Scan(&n)
	if n != 0 {
		t.Fatal(n)
	}
}
