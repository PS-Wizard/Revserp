package gsc

import (
	"strings"
	"testing"
)

// TestEncryptSecretEmptyRoundTrip guards the no-auth representation: empty
// plaintext must encrypt to a nonempty opaque value (NOT NULL credential
// columns stay satisfied without a placeholder token) and decrypt back to
// empty.
func TestEncryptSecretEmptyRoundTrip(t *testing.T) {
	service := NewService("id", "secret", "https://x.test/cb", "test-encryption-secret-not-real", 1024)
	enc, err := service.EncryptSecret("")
	if err != nil {
		t.Fatalf("encrypt empty: %v", err)
	}
	if enc == "" {
		t.Fatal("empty plaintext encrypted to empty ciphertext, which violates NOT NULL credential columns")
	}
	if strings.Contains(enc, " ") || strings.Contains(enc, "\n") {
		t.Fatalf("ciphertext %q is not opaque", enc)
	}
	dec, err := service.DecryptSecret(enc)
	if err != nil {
		t.Fatalf("decrypt empty: %v", err)
	}
	if dec != "" {
		t.Fatalf("decrypted = %q, want empty plaintext", dec)
	}
	again, err := service.EncryptSecret("")
	if err != nil {
		t.Fatalf("encrypt empty again: %v", err)
	}
	if again == enc {
		t.Fatal("empty plaintext encrypted deterministically; nonces must differ")
	}
	// A legacy empty stored value still decrypts to empty.
	legacy, err := service.DecryptSecret("")
	if err != nil || legacy != "" {
		t.Fatalf("decrypt legacy empty = %q, %v, want empty, nil", legacy, err)
	}
}

func TestEncryptSecretNonemptyRoundTripUnchanged(t *testing.T) {
	service := NewService("id", "secret", "https://x.test/cb", "test-encryption-secret-not-real", 1024)
	enc, err := service.EncryptSecret("real-token")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	dec, err := service.DecryptSecret(enc)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if dec != "real-token" {
		t.Fatalf("decrypted = %q, want the original token", dec)
	}
}
