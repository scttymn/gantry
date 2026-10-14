// Package bearer is who an API request is from: the user its bearer token
// names, a JWT signed with the app's secret.
package bearer

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/scttymn/gantry/web"
)

// User is who a token says the request is from. Name is the token's, as it
// is: it isn't looked up.
type User struct {
	ID   int64
	Name string
}

// Current is the request's user, once Require has passed it.
var Current = web.NewKey[User]("user")

var (
	errMissing = web.Status(http.StatusUnauthorized, errors.New("missing bearer token"))
	errInvalid = web.Status(http.StatusUnauthorized, errors.New("invalid or expired token"))
	errPayload = web.Status(http.StatusUnauthorized, errors.New("invalid token payload"))
)

// Tokens checks tokens signed with Secret.
type Tokens struct {
	Secret []byte
}

// Require is the filter in front of the routes that write: it checks the
// request's token (HS256 alone, its signature, and its exp and nbf when it
// has them, every time) and leaves its user in Current.
func (t Tokens) Require(w http.ResponseWriter, r *http.Request) error {
	tok, ok := web.BearerToken(r)
	if !ok {
		return errMissing
	}
	u, err := t.verify(tok, time.Now())
	if err != nil {
		return err
	}
	web.Set(r, Current, u)
	return nil
}

func (t Tokens) verify(tok string, now time.Time) (User, error) {
	i := strings.LastIndexByte(tok, '.')
	if i < 0 {
		return User{}, errInvalid
	}
	signed, signature := tok[:i], tok[i+1:]
	head, payload, ok := strings.Cut(signed, ".")
	if !ok {
		return User{}, errInvalid
	}
	var h struct {
		Alg string `json:"alg"`
	}
	if !decode(head, &h) || h.Alg != "HS256" {
		return User{}, errInvalid
	}
	given, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return User{}, errInvalid
	}
	mac := hmac.New(sha256.New, t.Secret)
	mac.Write([]byte(signed))
	if !hmac.Equal(given, mac.Sum(nil)) {
		return User{}, errInvalid
	}
	var claims struct {
		Sub      any      `json:"sub"`
		Username any      `json:"username"`
		Exp      *float64 `json:"exp"`
		Nbf      *float64 `json:"nbf"`
	}
	at := float64(now.Unix())
	if !decode(payload, &claims) || (claims.Exp != nil && at >= *claims.Exp) || (claims.Nbf != nil && at < *claims.Nbf) {
		return User{}, errInvalid
	}
	sub, _ := claims.Sub.(string)
	id, ok := ID(sub)
	name, named := claims.Username.(string)
	if !ok || !named {
		return User{}, errPayload
	}
	return User{ID: id, Name: name}, nil
}

// decode reads a token's part, JSON in base64url, into v.
func decode(part string, v any) bool {
	b, err := base64.RawURLEncoding.DecodeString(part)
	return err == nil && json.Unmarshal(b, v) == nil
}

// ID is s as an id, and whether it is one: a positive integer, digits alone.
func ID(s string) (int64, bool) {
	if s == "" || len(s) > 18 {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil && n > 0
}
