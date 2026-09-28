package sign

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scttymn/gantry/db"
)

func TestSigner(t *testing.T) {
	s := Signer{Key: []byte("k")}
	token := s.Sign("session", "42")
	if v, ok := s.Verify("session", token); !ok || v != "42" {
		t.Fatal(v, ok)
	}
	for name, bad := range map[string]string{
		"another purpose's token": s.Sign("lead_form", "42"),
		"a tampered value":        strings.Replace(token, "--", "x--", 1),
		"another key's token":     Signer{Key: []byte("other")}.Sign("session", "42"),
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

func TestKey(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, "sqlite://"+filepath.Join(t.TempDir(), "k.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	first, err := Key(ctx, d, "")
	if err != nil || len(first) != 32 {
		t.Fatal(len(first), err)
	}
	if again, _ := Key(ctx, d, ""); string(again) != string(first) {
		t.Error("a restart made a new key")
	}
	if set, _ := Key(ctx, d, "from-env"); string(set) != "from-env" {
		t.Error("SECRET_KEY wins")
	}
}
