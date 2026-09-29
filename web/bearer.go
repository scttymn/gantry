package web

import (
	"net/http"
	"strings"
)

// BearerToken is the token in a request's Authorization: Bearer header
// (RFC 6750), and whether there is one: the scheme's case doesn't matter,
// and an empty token is none (Rails: authenticate_with_http_token's
// parsing).
func BearerToken(r *http.Request) (string, bool) {
	scheme, tok, ok := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	tok = strings.TrimSpace(tok)
	return tok, tok != ""
}
