package crypt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scttymn/gantry/db"
)

var (
	oldKey = bytes.Repeat([]byte{1}, 32)
	newKey = bytes.Repeat([]byte{2}, 32)
)

func use(t *testing.T, keys ...[]byte) {
	t.Helper()
	if err := Use(keys...); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { Use() })
}

func sqlite(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(context.Background(), "sqlite://"+filepath.Join(t.TempDir(), "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	d.Write.Exec(`CREATE TABLE secrets (id integer PRIMARY KEY, token text NOT NULL DEFAULT '')`)
	return d
}

func TestRoundTrip(t *testing.T) {
	use(t, newKey)
	d := sqlite(t)
	if _, err := d.Write.Exec(`INSERT INTO secrets (id, token) VALUES (1, $1), (2, $2)`, Of("cf-api-token-123"), Of("")); err != nil {
		t.Fatal(err)
	}
	var stored string
	d.Read.QueryRow(`SELECT token FROM secrets WHERE id = 1`).Scan(&stored)
	if !strings.HasPrefix(stored, "gantry:v1:") || strings.Contains(stored, "cf-api-token") {
		t.Fatalf("stored as %q", stored)
	}
	var got String
	if err := d.Read.QueryRow(`SELECT token FROM secrets WHERE id = 1`).Scan(&got); err != nil || got.Reveal() != "cf-api-token-123" {
		t.Fatalf("read back %q, %v", got.Reveal(), err)
	}
	var empty String
	d.Read.QueryRow(`SELECT token FROM secrets WHERE id = 2`).Scan(&stored)
	if err := d.Read.QueryRow(`SELECT token FROM secrets WHERE id = 2`).Scan(&empty); err != nil || empty.Reveal() != "" || stored != "" {
		t.Errorf("an empty value: stored %q, read %q, %v", stored, empty.Reveal(), err)
	}
	// The same value twice isn't stored the same: each has its own nonce.
	a, _ := Of("x").Value()
	b, _ := Of("x").Value()
	if a == b {
		t.Error("the same ciphertext twice")
	}
}

// The newest key encrypts, every key decrypts: rotate by putting the new
// one first, then re-saving rows.
func TestRotation(t *testing.T) {
	use(t, oldKey)
	written, _ := Of("secret").Value()
	use(t, newKey, oldKey)
	var s String
	if err := s.Scan(written); err != nil || s.Reveal() != "secret" {
		t.Fatalf("an old key's value: %q %v", s.Reveal(), err)
	}
	rewritten, _ := s.Value()
	if strings.Split(rewritten.(string), ":")[2] == strings.Split(written.(string), ":")[2] {
		t.Error("re-saving didn't move it to the newest key")
	}
	use(t, newKey) // the old key retired
	if err := s.Scan(written); err == nil || !strings.Contains(err.Error(), "no key") {
		t.Errorf("a retired key's value: %v", err)
	}
}

func TestRefusals(t *testing.T) {
	use(t, newKey)
	written, _ := Of("secret").Value()
	tampered := written.(string)
	tampered = tampered[:len(tampered)-2] + "AA"
	var s String
	for name, stored := range map[string]any{
		"tampered":        tampered,
		"not encrypted":   "plain text",
		"another version": strings.Replace(written.(string), "gantry:v1:", "gantry:v9:", 1),
	} {
		if err := s.Scan(stored); err == nil {
			t.Errorf("%s: read", name)
		}
	}
	for _, n := range []int{5, 16, 24} { // 16 and 24 would be AES-128 and -192
		if err := Use(make([]byte, n)); err == nil {
			t.Errorf("a %d-byte key", n)
		}
	}
	Use()
	if _, err := Of("x").Value(); err == nil || !strings.Contains(err.Error(), "crypt.Use") {
		t.Errorf("with no keys: %v", err)
	}
}

// A secret never shows where it could leak: printed, logged, or as JSON.
func TestFiltered(t *testing.T) {
	s := Of("cf-api-token-123")
	var logs bytes.Buffer
	slog.New(slog.NewTextHandler(&logs, nil)).Info("saved", "token", s)
	j, _ := json.Marshal(map[string]String{"token": s})
	for _, shown := range []string{fmt.Sprint(s), fmt.Sprintf("%v %+v %#v %s %q", s, s, s, s, s), logs.String(), string(j)} {
		if strings.Contains(shown, "cf-api-token") || !strings.Contains(shown, "[FILTERED]") {
			t.Errorf("shown as %q", shown)
		}
	}
	if s.Reveal() != "cf-api-token-123" {
		t.Error("Reveal")
	}
}

func TestParseKeys(t *testing.T) {
	k := NewKey()
	keys, err := ParseKeys(k + ", " + NewKey())
	if err != nil || len(keys) != 2 || len(keys[0]) != 32 {
		t.Fatalf("%d keys, %v", len(keys), err)
	}
	if _, err := ParseKeys("not-base64!"); err == nil {
		t.Error("a bad key")
	}
}

func TestRoundTripPostgres(t *testing.T) {
	url := os.Getenv("GANTRY_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("GANTRY_TEST_POSTGRES_URL isn't set")
	}
	use(t, newKey)
	d, err := db.Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.Write.Exec(`DROP TABLE IF EXISTS crypt_secrets`)
	d.Write.Exec(`CREATE TABLE crypt_secrets (id bigint PRIMARY KEY, token text NOT NULL DEFAULT '')`)
	if _, err := d.Write.Exec(`INSERT INTO crypt_secrets (id, token) VALUES (1, $1)`, Of("cf-api-token-123")); err != nil {
		t.Fatal(err)
	}
	var got String
	if err := d.Read.QueryRow(`SELECT token FROM crypt_secrets WHERE id = 1`).Scan(&got); err != nil || got.Reveal() != "cf-api-token-123" {
		t.Fatalf("read back %q, %v", got.Reveal(), err)
	}
}
