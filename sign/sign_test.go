package sign

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scttymn/gantry/db"
)

// key is a test's key, 32 bytes and more: a Signer needs at least 32.
func key(s string) []byte { return []byte(s + strings.Repeat("k", 32)) }

func TestSigner(t *testing.T) {
	s := Signer{Key: key("k")}
	token := s.Sign("session", "42")
	if v, ok := s.Verify("session", token); !ok || v != "42" {
		t.Fatal(v, ok)
	}
	for name, bad := range map[string]string{
		"another purpose's token": s.Sign("lead_form", "42"),
		"a tampered value":        strings.Replace(token, "--", "x--", 1),
		"another key's token":     Signer{Key: key("other")}.Sign("session", "42"),
		"no signature":            "NDI",
		"garbage":                 "!!--!!",
	} {
		if _, ok := s.Verify("session", bad); ok {
			t.Errorf("%s passed", name)
		}
	}
	if v, ok := s.Verify("x", s.Sign("x", "")); !ok || v != "" {
		t.Error("an empty value")
	}
}

// A key under 32 bytes is a mistake, not a key: signing with one panics
// rather than hand out tokens anyone could forge.
func TestShortKey(t *testing.T) {
	for _, k := range [][]byte{nil, []byte("k"), key("")[1:]} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("a %d-byte key signed", len(k))
				}
			}()
			Signer{Key: k}.Sign("p", "v")
		}()
	}
}

func openDB(t *testing.T) *db.DB {
	d, err := db.Open(context.Background(), "sqlite://"+filepath.Join(t.TempDir(), "k.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestKey(t *testing.T) {
	ctx := context.Background()
	d := openDB(t)
	for _, env := range []string{"development", "test"} {
		t.Setenv("GANTRY_ENV", env)
		first, err := Key(ctx, d, "")
		if err != nil || len(first) != 32 {
			t.Fatal(env, len(first), err)
		}
		if again, _ := Key(ctx, d, ""); string(again) != string(first) {
			t.Error(env, ": a restart made a new key")
		}
	}
	secret := strings.Repeat("s", 32)
	for _, env := range []string{"development", "test", "production", ""} {
		t.Setenv("GANTRY_ENV", env)
		if set, err := Key(ctx, d, secret); err != nil || string(set) != secret {
			t.Errorf("GANTRY_ENV=%q: SECRET_KEY didn't win: %v", env, err)
		}
		if _, err := Key(ctx, d, secret[1:]); err == nil || !strings.Contains(err.Error(), "openssl rand -hex 32") {
			t.Errorf("GANTRY_ENV=%q: a 31-byte SECRET_KEY: %v", env, err)
		}
	}
}

// Outside development and test (and GANTRY_ENV unset is production), the
// key can't be the database's, where a copy of the data is a copy of it.
func TestKeyInProduction(t *testing.T) {
	d := openDB(t)
	for _, env := range []string{"production", "", "staging"} {
		t.Setenv("GANTRY_ENV", env)
		k, err := Key(context.Background(), d, "")
		if err == nil {
			t.Fatalf("GANTRY_ENV=%q: a blank SECRET_KEY gave a key of %d bytes", env, len(k))
		}
		for _, says := range []string{"SECRET_KEY", "openssl rand -hex 32", "Houston"} {
			if !strings.Contains(err.Error(), says) {
				t.Errorf("GANTRY_ENV=%q: %q doesn't say %q", env, err, says)
			}
		}
	}
}

// A value whose encoding has "--" in it, as the separator does, still
// verifies: "\xfb\xef\xbe" is "----" in base64url.
func TestSeparatorInValue(t *testing.T) {
	s := Signer{Key: key("k")}
	for _, v := range []string{"\xfb\xef\xbe", "a\xfb\xef\xbeb", `["é>?"]`, ""} {
		if got, ok := s.Verify("p", s.Sign("p", v)); !ok || got != v {
			t.Errorf("%q: %q %v", v, got, ok)
		}
	}
	tok := s.Sign("p", "x")
	for _, bad := range []string{tok[:len(tok)-1], "--" + tok, tok + "-", strings.Replace(tok, "--", "-_", 1), "--"} {
		if _, ok := s.Verify("p", bad); ok {
			t.Errorf("%q verified", bad)
		}
	}
}
