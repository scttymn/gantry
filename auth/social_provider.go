package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"golang.org/x/oauth2"
)

var (
	ErrSocialProof        = errors.New("auth: social identity could not be verified")
	ErrSocialUnavailable  = errors.New("auth: sign-in provider is unavailable; try again")
	ErrSocialLinkRequired = errors.New("auth: sign in with your existing method to confirm this identity")
)

// SocialIdentity is provider proof, not an application session. Subject is the
// stable identifier; Email can change (or be an Apple private relay address).
type SocialIdentity struct {
	Provider, Subject, Email string
	EmailVerified            bool
	// AuthoritativeEmail means the provider establishes mailbox ownership:
	// Apple verified email, or verified Gmail/Google Workspace email.
	AuthoritativeEmail bool
}

type GoogleConfig struct {
	ClientID, ClientSecret, RedirectURL string
	HTTPClient                          *http.Client // optional transport; requests still have a 10s timeout
}

type AppleConfig struct {
	ClientID, TeamID, KeyID, RedirectURL string
	PrivateKey                           []byte // PKCS#8 .p8 key, from private configuration (never source)
	HTTPClient                           *http.Client
}

// SocialProvider is immutable after construction. Its OIDC keys are cached and
// refreshed by go-oidc. Constructors do not contact the provider.
type SocialProvider struct {
	name, label, issuer string
	config              oauth2.Config
	client              *http.Client
	keys                oidc.KeySet
	appleKey            *ecdsa.PrivateKey
	teamID, keyID       string
}

func socialClient(c *http.Client) *http.Client {
	copy := http.Client{}
	if c != nil {
		copy = *c
	}
	copy.Timeout = 10 * time.Second
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("auth: provider redirect refused") }
	return &copy
}

func validSocialRedirect(raw string, apple bool) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !strings.HasPrefix(u.Path, "/") {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	if apple || u.Scheme != "http" {
		return false
	}
	return u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback()
}

func NewGoogle(c GoogleConfig) (*SocialProvider, error) {
	if c.ClientID == "" || c.ClientSecret == "" || !validSocialRedirect(c.RedirectURL, false) {
		return nil, errors.New("auth: Google needs client ID, client secret and an HTTPS callback (HTTP loopback is allowed for development)")
	}
	p := &SocialProvider{name: "google", label: "Google", issuer: "https://accounts.google.com", client: socialClient(c.HTTPClient)}
	p.config = oauth2.Config{ClientID: c.ClientID, ClientSecret: c.ClientSecret, RedirectURL: c.RedirectURL, Scopes: []string{"openid", "email"}, Endpoint: oauth2.Endpoint{AuthURL: "https://accounts.google.com/o/oauth2/v2/auth", TokenURL: "https://oauth2.googleapis.com/token", AuthStyle: oauth2.AuthStyleInParams}}
	p.keys = oidc.NewRemoteKeySet(oidc.ClientContext(context.Background(), p.client), "https://www.googleapis.com/oauth2/v3/certs")
	return p, nil
}

func NewApple(c AppleConfig) (*SocialProvider, error) {
	if c.ClientID == "" || c.TeamID == "" || c.KeyID == "" || !validSocialRedirect(c.RedirectURL, true) {
		return nil, errors.New("auth: Apple needs Services ID, Team ID, Key ID, private key and an HTTPS callback")
	}
	block, _ := pem.Decode(c.PrivateKey)
	if block == nil {
		return nil, errors.New("auth: Apple private key must be PKCS#8 PEM")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("auth: Apple private key must be PKCS#8 PEM")
	}
	ec, ok := key.(*ecdsa.PrivateKey)
	if !ok || ec.Curve != elliptic.P256() {
		return nil, errors.New("auth: Apple private key must be an ES256 P-256 key")
	}
	p := &SocialProvider{name: "apple", label: "Apple", issuer: "https://appleid.apple.com", client: socialClient(c.HTTPClient), appleKey: ec, teamID: c.TeamID, keyID: c.KeyID}
	p.config = oauth2.Config{ClientID: c.ClientID, RedirectURL: c.RedirectURL, Scopes: []string{"openid", "email"}, Endpoint: oauth2.Endpoint{AuthURL: "https://appleid.apple.com/auth/authorize", TokenURL: "https://appleid.apple.com/auth/token", AuthStyle: oauth2.AuthStyleInParams}}
	p.keys = oidc.NewRemoteKeySet(oidc.ClientContext(context.Background(), p.client), "https://appleid.apple.com/auth/keys")
	return p, nil
}

func (p *SocialProvider) clientSecret(now time.Time) (string, error) {
	if p.name != "apple" {
		return p.config.ClientSecret, nil
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: p.appleKey}, (&jose.SignerOptions{}).WithHeader("kid", p.keyID))
	if err != nil {
		return "", err
	}
	return jwt.Signed(signer).Claims(jwt.Claims{Issuer: p.teamID, Subject: p.config.ClientID, Audience: jwt.Audience{p.issuer}, IssuedAt: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(time.Hour))}).Serialize()
}

func (p *SocialProvider) authorization(state, nonce, verifier string) string {
	opts := []oauth2.AuthCodeOption{oidc.Nonce(nonce)}
	if p.name == "apple" {
		opts = append(opts, oauth2.SetAuthURLParam("response_mode", "form_post"))
	} else {
		opts = append(opts, oauth2.S256ChallengeOption(verifier))
	}
	return p.config.AuthCodeURL(state, opts...)
}

// boolClaim supports Apple's JSON string and Google's boolean claim formats.
type boolClaim bool

func (b *boolClaim) UnmarshalJSON(raw []byte) error {
	switch string(raw) {
	case "true", `"true"`:
		*b = true
	case "false", `"false"`, "null":
		*b = false
	default:
		return errors.New("invalid boolean claim")
	}
	return nil
}

func (p *SocialProvider) exchange(ctx context.Context, code, nonce, verifier string, now time.Time) (SocialIdentity, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ctx = oidc.ClientContext(ctx, p.client)
	config := p.config
	secret, err := p.clientSecret(now)
	if err != nil {
		return SocialIdentity{}, ErrSocialUnavailable
	}
	config.ClientSecret = secret
	opts := []oauth2.AuthCodeOption{}
	if p.name == "google" {
		opts = append(opts, oauth2.VerifierOption(verifier))
	}
	tok, err := config.Exchange(ctx, code, opts...)
	if err != nil {
		return SocialIdentity{}, ErrSocialUnavailable
	} // never surface token response bodies
	raw, ok := tok.Extra("id_token").(string)
	if !ok || len(raw) > 32768 {
		return SocialIdentity{}, ErrSocialProof
	}
	checked, err := oidc.NewVerifier(p.issuer, p.keys, &oidc.Config{ClientID: config.ClientID, SupportedSigningAlgs: []string{oidc.RS256}, Now: func() time.Time { return now }}).Verify(ctx, raw)
	if err != nil {
		return SocialIdentity{}, ErrSocialProof
	}
	if checked.Subject == "" || len(checked.Subject) > 255 || checked.Nonce != nonce || nonce == "" || !checked.Expiry.After(now) || checked.IssuedAt.Unix() <= 0 || checked.IssuedAt.After(now.Add(time.Minute)) {
		return SocialIdentity{}, ErrSocialProof
	}
	var claims struct {
		Email        string    `json:"email"`
		Verified     boolClaim `json:"email_verified"`
		AZP          string    `json:"azp"`
		HostedDomain string    `json:"hd"`
	}
	if err := checked.Claims(&claims); err != nil {
		return SocialIdentity{}, ErrSocialProof
	}
	if (claims.AZP != "" && claims.AZP != config.ClientID) || (len(checked.Audience) > 1 && claims.AZP != config.ClientID) {
		return SocialIdentity{}, ErrSocialProof
	}
	email := Normalize(claims.Email)
	authoritative := bool(claims.Verified) && (p.name == "apple" || strings.HasSuffix(email, "@gmail.com") || strings.HasSuffix(email, "@googlemail.com") || claims.HostedDomain != "")
	return SocialIdentity{Provider: p.name, Subject: checked.Subject, Email: email, EmailVerified: bool(claims.Verified), AuthoritativeEmail: authoritative}, nil
}

// Keep the claim parser exercised independently of remote provider IO.
var _ json.Unmarshaler = (*boolClaim)(nil)
