package ghauth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Sealing: a linked user's token is stored encrypted, never in plaintext, so
// a copy of the database alone — a backup, a stray file — writes to GitHub as
// nobody. The key comes from the server's environment (CRS_SECRET_KEY), not
// the database or the config file.
//
// A sealed credential is "v1:" and the standard base64 of a random 12-byte
// nonce followed by the AES-256-GCM ciphertext of the credential's JSON. The
// lower-cased login it belongs to is the additional authenticated data, so a
// ciphertext copied onto another user's row does not open there.

// sealedPrefix marks the format, so another one can follow it.
const sealedPrefix = "v1:"

// ErrUnreadable means a sealed credential does not open: another key sealed
// it, it belongs to another login, or it is not one at all.
var ErrUnreadable = errors.New("the stored GitHub credential cannot be opened with this key for this login")

// Credential is what is sealed for a linked user: their token, the GitHub
// login it belongs to, and when it was linked.
type Credential struct {
	Token
	GitHubLogin string    `json:"github_login"`
	LinkedAt    time.Time `json:"linked_at"`
}

func (c Credential) String() string       { return "ghauth.Credential(redacted)" }
func (c Credential) GoString() string     { return c.String() }
func (c Credential) LogValue() slog.Value { return slog.StringValue(c.String()) }

// Key is the AES-256 key credentials are sealed with.
type Key struct {
	b [32]byte
}

// ParseKey reads a key written as 64 hex digits or as the standard base64 of
// 32 bytes — what `openssl rand -hex 32` or `openssl rand -base64 32` print.
func ParseKey(s string) (Key, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Key{}, errors.New("the key is empty")
	}
	var raw []byte
	if len(s) == 64 {
		if decoded, err := hex.DecodeString(s); err == nil {
			raw = decoded
		}
	}
	if raw == nil {
		decoded, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return Key{}, errors.New("the key is neither 64 hex digits nor base64")
		}
		raw = decoded
	}
	if len(raw) != 32 {
		return Key{}, fmt.Errorf("the key is %d bytes; it must be 32", len(raw))
	}
	var k Key
	copy(k.b[:], raw)
	return k, nil
}

// String keeps a key out of logs.
func (k Key) String() string { return "ghauth.Key(redacted)" }

func (k Key) aead() (cipher.AEAD, error) {
	block, err := aes.NewCipher(k.b[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// loginAAD is the additional authenticated data that ties a sealed credential
// to its user's row. Logins are matched case-insensitively, so it is the
// lower-cased login.
func loginAAD(login string) []byte {
	return []byte(strings.ToLower(login))
}

// Seal encrypts c for login's row.
func (k Key) Seal(login string, c Credential) (string, error) {
	plaintext, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	aead, err := k.aead()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	// crypto/rand.Read never fails; it crashes the program rather than return
	// short.
	_, _ = rand.Read(nonce)
	sealed := aead.Seal(nonce, nonce, plaintext, loginAAD(login))
	return sealedPrefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// Open decrypts a credential Seal sealed for login, or fails with
// ErrUnreadable.
func (k Key) Open(login, sealed string) (Credential, error) {
	encoded, ok := strings.CutPrefix(sealed, sealedPrefix)
	if !ok {
		return Credential{}, ErrUnreadable
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return Credential{}, ErrUnreadable
	}
	aead, err := k.aead()
	if err != nil {
		return Credential{}, err
	}
	if len(raw) < aead.NonceSize()+aead.Overhead() {
		return Credential{}, ErrUnreadable
	}
	nonce, ciphertext := raw[:aead.NonceSize()], raw[aead.NonceSize():]
	plaintext, err := aead.Open(nil, nonce, ciphertext, loginAAD(login))
	if err != nil {
		return Credential{}, ErrUnreadable
	}
	var c Credential
	if err := json.Unmarshal(plaintext, &c); err != nil {
		return Credential{}, ErrUnreadable
	}
	return c, nil
}
