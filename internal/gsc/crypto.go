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
func (service *Service) DecryptSecret(value string) (string, error) {
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

	ciphertext, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return "", &Error{Message: "failed to decode stored Google credentials"}
	}
	if len(ciphertext) < gcm.NonceSize() {
		return "", &Error{Message: "failed to decode stored Google credentials"}
	}

	nonce := ciphertext[:gcm.NonceSize()]
	plaintext, err := gcm.Open(nil, nonce, ciphertext[gcm.NonceSize():], nil)
	if err != nil {
		return "", &Error{Message: "failed to decrypt stored Google credentials"}
	}
	return string(plaintext), nil
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
