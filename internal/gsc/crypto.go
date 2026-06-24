package gsc

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

// EncryptSecret encrypts one token for database storage.
func (service *Service) EncryptSecret(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	key, err := deriveEncryptionKey(service.encryptionSecret)
	if err != nil {
		return "", fmt.Errorf("derive token encryption key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("build token cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("build token gcm: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate token nonce: %w", err)
	}

	ciphertext := gcm.Seal(nonce, nonce, []byte(value), nil)
	return base64.RawURLEncoding.EncodeToString(ciphertext), nil
}

// DecryptSecret decrypts one token loaded from database storage.
//
// It first attempts decryption with the current HKDF-derived key, then falls
// back to the legacy bare-SHA-256 key so credentials encrypted before the KDF
// hardening (see deriveEncryptionKey) continue to work without re-authorization.
func (service *Service) DecryptSecret(value string) (string, error) {
	if value == "" {
		return "", nil
	}

	key, err := deriveEncryptionKey(service.encryptionSecret)
	if err != nil {
		return "", fmt.Errorf("derive token encryption key: %w", err)
	}
	if plaintext, ok := decryptWithKey(key, value); ok {
		return plaintext, nil
	}

	// Legacy fallback: tokens encrypted with the old unsalted SHA-256 key.
	if plaintext, ok := decryptWithKey(legacyEncryptionKey(service.encryptionSecret), value); ok {
		return plaintext, nil
	}

	return "", &Error{Message: "failed to decrypt stored Google credentials"}
}

// decryptWithKey attempts AES-256-GCM decryption with the given key, returning
// ok=false on any decode/auth failure so the caller can try another key.
func decryptWithKey(key []byte, value string) (string, bool) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", false
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", false
	}

	ciphertext, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return "", false
	}
	if len(ciphertext) < gcm.NonceSize() {
		return "", false
	}

	nonce := ciphertext[:gcm.NonceSize()]
	plaintext, err := gcm.Open(nil, nonce, ciphertext[gcm.NonceSize():], nil)
	if err != nil {
		return "", false
	}
	return string(plaintext), true
}

// legacyEncryptionKey reproduces the old bare-SHA-256 key derivation used before
// the HKDF migration, for decrypting credentials stored under the old scheme.
func legacyEncryptionKey(secret string) []byte {
	digest := sha256.Sum256([]byte(secret))
	return digest[:]
}

// deriveEncryptionKey derives a 32-byte AES-256 key from secret using HKDF-SHA256.
//
// BREAKING CHANGE: This was previously a bare SHA-256 hash of the secret (weak KDF, no salt).
// Switching to HKDF changes the derived key, which means any GSC tokens already encrypted with
// the old key cannot be decrypted with this implementation. Existing GSC tokens in the database
// must be re-encrypted (or cleared and re-authorized) after deploying this change.
func deriveEncryptionKey(secret string) ([]byte, error) {
	reader := hkdf.New(sha256.New, []byte(secret), nil, []byte("revserp-gsc-token-encryption"))
	key := make([]byte, 32)
	if _, err := io.ReadFull(reader, key); err != nil {
		return nil, err
	}
	return key, nil
}
