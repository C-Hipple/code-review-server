package ghauth

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

const testKeyHex = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

func testKey(t *testing.T, s string) Key {
	t.Helper()
	k, err := ParseKey(s)
	if err != nil {
		t.Fatalf("ParseKey(%q): %v", s, err)
	}
	return k
}

func TestParseKey(t *testing.T) {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i)
	}
	fromHex := testKey(t, testKeyHex)
	fromBase64 := testKey(t, " "+base64.StdEncoding.EncodeToString(raw)+"\n")
	if fromHex != fromBase64 {
		t.Error("the same 32 bytes as hex and as base64 parse to different keys")
	}
	if upper := testKey(t, strings.ToUpper(testKeyHex)); upper != fromHex {
		t.Error("upper-case hex parses to another key")
	}

	for _, bad := range []string{
		"",
		"   ",
		testKeyHex[:62],       // 31 bytes of hex: base64 of the wrong length
		testKeyHex[:63] + "g", // not hex, and base64 of 48 bytes
		base64.StdEncoding.EncodeToString(raw[:16]),
		base64.URLEncoding.EncodeToString(bytes.Repeat([]byte{0xff}, 32)), // "___…": URL-safe, not standard base64
		"correct horse battery staple",
	} {
		if _, err := ParseKey(bad); err == nil {
			t.Errorf("ParseKey(%q) accepted it", bad)
		}
	}
	if s := fromHex.String(); strings.Contains(s, "0001") {
		t.Errorf("a key prints as %q", s)
	}
}

func TestSealAndOpen(t *testing.T) {
	key := testKey(t, testKeyHex)
	linked := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	cred := Credential{
		Token: Token{AccessToken: "gho_alice", TokenType: "bearer", Scope: "repo", RefreshToken: "ghr_alice",
			ExpiresAt: linked.Add(8 * time.Hour), RefreshTokenExpiresAt: linked.Add(4000 * time.Hour)},
		GitHubLogin: "Alice",
		LinkedAt:    linked,
	}

	sealed, err := key.Seal("Alice", cred)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if !strings.HasPrefix(sealed, "v1:") || strings.Contains(sealed, "gho_alice") || strings.Contains(sealed, "ghr_alice") {
		t.Errorf("sealed = %q", sealed)
	}
	again, _ := key.Seal("Alice", cred)
	if again == sealed {
		t.Error("sealing twice gave the same ciphertext; the nonce is not random")
	}

	// Logins match case-insensitively, so the row's login opens it in any case.
	for _, login := range []string{"Alice", "alice", "ALICE"} {
		got, err := key.Open(login, sealed)
		if err != nil {
			t.Fatalf("Open(%q): %v", login, err)
		}
		if got.AccessToken != "gho_alice" || got.RefreshToken != "ghr_alice" || got.GitHubLogin != "Alice" ||
			!got.LinkedAt.Equal(linked) || !got.ExpiresAt.Equal(cred.ExpiresAt) || !got.RefreshTokenExpiresAt.Equal(cred.RefreshTokenExpiresAt) {
			t.Errorf("Open(%q) = %+v", login, got.Token.AccessToken)
		}
	}

	other := testKey(t, strings.Repeat("ab", 32))
	raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(sealed, "v1:"))
	raw[len(raw)-1] ^= 1
	for name, open := range map[string]func() (Credential, error){
		"another login":   func() (Credential, error) { return key.Open("bob", sealed) },
		"another key":     func() (Credential, error) { return other.Open("Alice", sealed) },
		"tampered":        func() (Credential, error) { return key.Open("Alice", "v1:"+base64.StdEncoding.EncodeToString(raw)) },
		"another version": func() (Credential, error) { return key.Open("Alice", "v2:"+strings.TrimPrefix(sealed, "v1:")) },
		"plaintext":       func() (Credential, error) { return key.Open("Alice", "gho_alice") },
		"truncated":       func() (Credential, error) { return key.Open("Alice", "v1:AAAA") },
		"not base64":      func() (Credential, error) { return key.Open("Alice", "v1:!!!") },
	} {
		if _, err := open(); !errors.Is(err, ErrUnreadable) {
			t.Errorf("%s: err = %v, want ErrUnreadable", name, err)
		}
	}
}
