package sign

import (
	"strings"
	"testing"
)

func FuzzSignVerify(f *testing.F) {
	f.Add("session", "user-42", "other", "garbage--token")
	f.Add("reset", "", "reset", "--")
	f.Add("flash", "hello\x00world", "flash", "")
	s := Signer{Key: []byte(strings.Repeat("k", 32))}
	f.Fuzz(func(t *testing.T, purpose, value, otherPurpose, raw string) {
		tok := s.Sign(purpose, value)
		got, ok := s.Verify(purpose, tok)
		if !ok || got != value {
			t.Fatalf("Verify(%q, Sign(%q, %q)) = (%q, %v)", purpose, purpose, value, got, ok)
		}
		if otherPurpose != purpose {
			if _, ok := s.Verify(otherPurpose, tok); ok {
				t.Fatalf("token for %q verified under %q", purpose, otherPurpose)
			}
		}
		_, _ = s.Verify(purpose, raw)
	})
}
