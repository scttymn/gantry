package token

import (
	"regexp"
	"testing"
)

func TestNew(t *testing.T) {
	tok, digest := New("hou_")
	if !regexp.MustCompile(`\Ahou_[A-Za-z0-9_-]{43}\z`).MatchString(tok) {
		t.Errorf("token %q: the prefix and 32 random bytes, base64url", tok)
	}
	if digest != Digest(tok) || !regexp.MustCompile(`\A[0-9a-f]{64}\z`).MatchString(digest) {
		t.Errorf("digest %q isn't the token's SHA-256", digest)
	}
	if again, _ := New("hou_"); again == tok {
		t.Error("two tokens alike")
	}
	if plain, _ := New(""); len(plain) != 43 {
		t.Errorf("no prefix: %q", plain)
	}
	// auth's sessions were made this way: a digest is the same as before.
	if got := Digest("abc"); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Errorf("Digest(abc) = %s", got)
	}
}

func TestEqual(t *testing.T) {
	for _, tc := range []struct {
		given, expected string
		want            bool
	}{
		{"s3cret-runner-token-of-32-bytes!", "s3cret-runner-token-of-32-bytes!", true},
		{"s3cret-runner-token-of-32-bytes?", "s3cret-runner-token-of-32-bytes!", false},
		{"short", "s3cret-runner-token-of-32-bytes!", false},
		{"", "s3cret-runner-token-of-32-bytes!", false},
		{"", "", false}, // no secret set isn't a match
	} {
		if got := Equal(tc.given, tc.expected); got != tc.want {
			t.Errorf("Equal(%q, %q) = %v", tc.given, tc.expected, got)
		}
	}
}
