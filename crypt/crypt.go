// Package crypt keeps a column's value encrypted in the database (Rails'
// encrypts): a leaked backup or a copied file gives away no secret.
// A column of type String is encrypted when it's written and decrypted when
// it's read, so the rest of the code sees plain values; declare it once, in
// sqlc.yaml's overrides:
//
//	overrides:
//	  - column: installations.cloudflare_token
//	    go_type: github.com/scttymn/gantry/crypt.String
//
// The keys are set once, at start (as sql.Register), from the environment:
//
//	keys, err := crypt.ParseKeys(os.Getenv("ENCRYPTION_KEYS"))
//	crypt.Use(keys...)
//
// The first key encrypts, and every key decrypts: each stored value names
// the key it was made with, so a new key goes first, the old ones stay
// until every row is saved again (an app's task), and then they go.
// Values are AES-256-GCM, stored as text ("gantry:v1:<key>:<data>") on
// either engine. An empty string is stored as it is.
package crypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql/driver"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
)

const prefix = "gantry:v1:"

type key struct {
	id   string // the first 8 hex of the key's SHA-256: which key, not the key
	aead cipher.AEAD
}

var keys atomic.Pointer[[]key]

// Use sets the keys: the first encrypts, all decrypt. Each is 32 bytes
// (NewKey makes one). Use() with none takes them away.
func Use(raw ...[]byte) error {
	ks := make([]key, 0, len(raw))
	for _, r := range raw {
		if len(r) != 32 {
			return fmt.Errorf("crypt: a key is 32 bytes, not %d", len(r))
		}
		block, err := aes.NewCipher(r)
		if err != nil {
			return err
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(r)
		ks = append(ks, key{id: hex.EncodeToString(sum[:4]), aead: aead})
	}
	keys.Store(&ks)
	return nil
}

// NewKey is a new random key, in base64, for ENCRYPTION_KEYS.
func NewKey() string {
	raw := make([]byte, 32)
	rand.Read(raw)
	return base64.StdEncoding.EncodeToString(raw)
}

// ParseKeys reads keys written as NewKey writes them, separated by commas,
// the newest first.
func ParseKeys(s string) ([][]byte, error) {
	var out [][]byte
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part == "" {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(part)
		if err != nil {
			return nil, errors.New("crypt: a key isn't base64 (crypt.NewKey makes one)")
		}
		out = append(out, raw)
	}
	return out, nil
}

// String is a secret kept encrypted in its column. Printed, logged or
// encoded as JSON it's [FILTERED]; Reveal gives the value.
type String struct{ value string }

// Of is a String holding v.
func Of(v string) String { return String{value: v} }

// Reveal is the secret itself: call it only where it's needed (the request
// to the service it's for), so each use is easy to find.
func (s String) Reveal() string { return s.value }

const filtered = "[FILTERED]"

func (s String) String() string                { return filtered }
func (s String) GoString() string              { return filtered }
func (s String) Format(f fmt.State, verb rune) { fmt.Fprint(f, filtered) }
func (s String) LogValue() slog.Value          { return slog.StringValue(filtered) }
func (s String) MarshalJSON() ([]byte, error)  { return []byte(`"` + filtered + `"`), nil }
func (s String) MarshalText() ([]byte, error)  { return []byte(filtered), nil }

// Value is the value encrypted with the first key, for the database.
func (s String) Value() (driver.Value, error) {
	if s.value == "" {
		return "", nil
	}
	ks := keys.Load()
	if ks == nil || len(*ks) == 0 {
		return nil, errors.New("crypt: no keys to encrypt with: call crypt.Use at start")
	}
	k := (*ks)[0]
	nonce := make([]byte, k.aead.NonceSize())
	rand.Read(nonce)
	sealed := k.aead.Seal(nonce, nonce, []byte(s.value), nil)
	return prefix + k.id + ":" + base64.StdEncoding.EncodeToString(sealed), nil
}

// Scan decrypts the column with the key that encrypted it.
func (s *String) Scan(src any) error {
	var stored string
	switch v := src.(type) {
	case nil:
		s.value = ""
		return nil
	case string:
		stored = v
	case []byte:
		stored = string(v)
	default:
		return fmt.Errorf("crypt: can't read %T", src)
	}
	if stored == "" {
		s.value = ""
		return nil
	}
	rest, ok := strings.CutPrefix(stored, prefix)
	if !ok {
		return errors.New("crypt: the column isn't encrypted by gantry (or by another version of it)")
	}
	id, data, ok := strings.Cut(rest, ":")
	if !ok {
		return errors.New("crypt: the column's value is malformed")
	}
	sealed, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return errors.New("crypt: the column's value is malformed")
	}
	ks := keys.Load()
	if ks != nil {
		for _, k := range *ks {
			if k.id != id {
				continue
			}
			n := k.aead.NonceSize()
			if len(sealed) < n {
				return errors.New("crypt: the column's value is malformed")
			}
			plain, err := k.aead.Open(nil, sealed[:n], sealed[n:], nil)
			if err != nil {
				return errors.New("crypt: the column's value doesn't decrypt: it was changed, or the key is wrong")
			}
			s.value = string(plain)
			return nil
		}
	}
	return fmt.Errorf("crypt: no key %s to decrypt with (was it retired before every row was saved again?)", id)
}
