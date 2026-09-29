// Package token makes the keys an app hands out and keeps only a digest of
// (a session's cookie, an API token, a one-time code in a link), and
// compares a secret without its timing giving it away: the building blocks
// sign-in recipes are made from (Rails: has_secure_token, secure_compare).
//
//	tok, digest := token.New("hou_") // hand out tok, once; store digest
//	row := find(token.Digest(given)) // look a presented one up by its digest
//	token.Equal(given, os.Getenv("RUNNER_TOKEN"))
//
// A copy of the database gives nobody a working key: it holds digests.
package token

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
)

// New is a new key, prefix and 32 random bytes in base64url (a prefix, such
// as "hou_", lets secret scanners recognise a leaked one), and its digest,
// to store.
func New(prefix string) (tok, digest string) {
	raw := make([]byte, 32)
	rand.Read(raw) // crypto/rand doesn't fail on supported platforms
	tok = prefix + base64.RawURLEncoding.EncodeToString(raw)
	return tok, Digest(tok)
}

// Digest is a key's SHA-256, in hex: what's stored, and looked up by.
func Digest(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// Equal reports whether a presented secret is the expected one, in time
// that doesn't depend on where they differ, or on their lengths: both are
// hashed first. An empty expected secret matches nothing.
func Equal(given, expected string) bool {
	if expected == "" {
		return false
	}
	g, e := sha256.Sum256([]byte(given)), sha256.Sum256([]byte(expected))
	return subtle.ConstantTimeCompare(g[:], e[:]) == 1
}
