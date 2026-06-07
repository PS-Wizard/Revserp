package gsc

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
)

// EncryptSecret encrypts one token for database storage.
func (service *Service) EncryptSecret(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	key := deriveEncryptionKey(service.encryptionSecret)
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
	key := deriveEncryptionKey(service.encryptionSecret)
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

func deriveEncryptionKey(secret string) []byte {
	digest := sha256.Sum256([]byte(secret))
	return digest[:]
}
