package auth_test

import (
	"strings"
	"testing"

	"github.com/tonis/c3pkg/internal/auth"
)

func TestPassword(t *testing.T) {
	h, err := auth.HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$") {
		t.Fatalf("unexpected format %q", h)
	}
	if !auth.VerifyPassword("correct horse", h) {
		t.Error("right password rejected")
	}
	if auth.VerifyPassword("wrong", h) || auth.VerifyPassword("x", "garbage") || auth.VerifyPassword("x", "") {
		t.Error("bad password or malformed hash accepted")
	}
	h2, _ := auth.HashPassword("correct horse")
	if h == h2 {
		t.Error("hashes must be salted")
	}
}

func TestTokens(t *testing.T) {
	raw, hash, err := auth.NewToken("c3p_")
	if err != nil || !strings.HasPrefix(raw, "c3p_") {
		t.Fatalf("raw=%q err=%v", raw, err)
	}
	if string(auth.HashToken(raw)) != string(hash) {
		t.Error("hash mismatch")
	}
	raw2, _, _ := auth.NewToken("c3p_")
	if raw == raw2 {
		t.Error("tokens must be unique")
	}
}

func TestCSRF(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	tok, err := auth.NewCSRF(secret)
	if err != nil || !auth.VerifyCSRF(secret, tok) {
		t.Fatalf("valid token rejected: %v", err)
	}
	if auth.VerifyCSRF([]byte("another secret value 000000000000"), tok) || auth.VerifyCSRF(secret, "abc") || auth.VerifyCSRF(secret, tok+"x") {
		t.Error("forged token accepted")
	}
}
