package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

// Crypter encrypts and decrypts values using AES-256-GCM with an HKDF-derived key.
// The ciphertext format is: base64RawURL(nonce || gcm_ciphertext_with_tag).
// Empty plaintext is preserved as an empty string (no encryption performed).
type Crypter struct {
	key []byte
}

// New builds a Crypter from a secret. Returns an error if the secret is empty.
func New(secret string) (*Crypter, error) {
	if secret == "" {
		return nil, fmt.Errorf("crypto: secret must not be empty")
	}
	key, err := deriveKey(secret)
	if err != nil {
		return nil, fmt.Errorf("crypto: derive key: %w", err)
	}
	return &Crypter{key: key}, nil
}

// Encrypt encrypts plaintext using AES-256-GCM. Empty input returns "".
func (c *Crypter) Encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", fmt.Errorf("crypto: build cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("crypto: build gcm: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("crypto: generate nonce: %w", err)
	}

	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.RawURLEncoding.EncodeToString(ciphertext), nil
}

// Decrypt decrypts a ciphertext produced by Encrypt. Empty input returns "".
func (c *Crypter) Decrypt(ciphertext string) (string, error) {
	if ciphertext == "" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", fmt.Errorf("crypto: decode ciphertext: %w", err)
	}

	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", fmt.Errorf("crypto: build cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("crypto: build gcm: %w", err)
	}

	if len(raw) < gcm.NonceSize() {
		return "", fmt.Errorf("crypto: ciphertext too short")
	}

	nonce := raw[:gcm.NonceSize()]
	plaintext, err := gcm.Open(nil, nonce, raw[gcm.NonceSize():], nil)
	if err != nil {
		return "", fmt.Errorf("crypto: decrypt: %w", err)
	}
	return string(plaintext), nil
}

// deriveKey derives a 32-byte AES key from secret using HKDF-SHA256.
func deriveKey(secret string) ([]byte, error) {
	reader := hkdf.New(sha256.New, []byte(secret), nil, []byte("revserp-session-token-encryption"))
	key := make([]byte, 32)
	if _, err := io.ReadFull(reader, key); err != nil {
		return nil, err
	}
	return key, nil
}
