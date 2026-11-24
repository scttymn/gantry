// Package sign signs values an app hands to a browser and wants back
// unchanged: a session id in a cookie, a form's timestamp, a reset link.
// Each signature is for a purpose, so a token made for one can't be used for
// another. Values are readable by whoever holds the token; they're signed,
// not encrypted.
package sign

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/internal/env"
)

// Signer signs and checks tokens with Key, of at least 32 bytes: a shorter
// one is a mistake, and panics.
type Signer struct{ Key []byte }

const minKey = 32

const generate = "generate one with `bin/gantry secret` and set it as the app's Houston secret env var: `bin/gantry secret | houston secrets set SECRET_KEY --project NAME`"

func (s Signer) checkKey() {
	if len(s.Key) < minKey {
		panic(fmt.Sprintf("sign: a %d-byte key; a Signer needs at least %d: %s", len(s.Key), minKey, generate))
	}
}

func (s Signer) mac(purpose, value string) string {
	m := hmac.New(sha256.New, s.Key)
	m.Write([]byte(purpose + "\x00" + value))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// Sign signs value for purpose ("session", "lead_form", …).
func (s Signer) Sign(purpose, value string) string {
	s.checkKey()
	return base64.RawURLEncoding.EncodeToString([]byte(value)) + "--" + s.mac(purpose, value)
}

var macLen = base64.RawURLEncoding.EncodedLen(sha256.Size)

// Verify is the value signed for purpose, if the token is genuine.
func (s Signer) Verify(purpose, token string) (string, bool) {
	s.checkKey()
	i := len(token) - macLen - len("--")
	if i < 0 || token[i:i+2] != "--" {
		return "", false
	}
	encoded, sig := token[:i], token[i+2:]
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", false
	}
	if !hmac.Equal([]byte(sig), []byte(s.mac(purpose, string(raw)))) {
		return "", false
	}
	return string(raw), true
}

// Key is secret when it's set (SECRET_KEY), at least 32 bytes. Blank, in
// development and test (GANTRY_ENV), it's a key generated once and kept in
// the database (gantry_settings), so tokens survive a restart with no secret
// to set. Anywhere else blank is an error: a copy of the data mustn't be a
// copy of the key that signs reset links, cookies and live streams.
func Key(ctx context.Context, d *db.DB, secret string) ([]byte, error) {
	if secret != "" {
		if len(secret) < minKey {
			return nil, fmt.Errorf("sign: SECRET_KEY is %d bytes and needs at least %d: %s", len(secret), minKey, generate)
		}
		return []byte(secret), nil
	}
	if !env.Development() && !env.Test() {
		return nil, errors.New("sign: SECRET_KEY is blank, and outside development and test the signing key can't be kept in the database: " + generate)
	}
	if _, err := d.Write.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS gantry_settings (name text PRIMARY KEY, value text NOT NULL)`); err != nil {
		return nil, err
	}
	fresh := make([]byte, 32)
	rand.Read(fresh)
	if _, err := d.Write.ExecContext(ctx, `INSERT INTO gantry_settings (name, value) VALUES ('signing_key', $1) ON CONFLICT (name) DO NOTHING`, hex.EncodeToString(fresh)); err != nil {
		return nil, err
	}
	var stored string
	if err := d.Write.QueryRowContext(ctx, `SELECT value FROM gantry_settings WHERE name = 'signing_key'`).Scan(&stored); err != nil {
		return nil, err
	}
	k, err := hex.DecodeString(stored)
	if err != nil || len(k) < minKey {
		return nil, fmt.Errorf("sign: the signing key kept in the database (gantry_settings) isn't a %d-byte key: delete its row (name 'signing_key') for a new one, or set SECRET_KEY", minKey)
	}
	return k, nil
}
