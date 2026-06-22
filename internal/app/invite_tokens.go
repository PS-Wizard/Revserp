package app

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

func generateOrganizationInviteToken() (string, error) {
	rawTokenBytes := make([]byte, 32)
	if _, err := rand.Read(rawTokenBytes); err != nil {
		return "", fmt.Errorf("generate organization invite token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(rawTokenBytes), nil
}

func hashOrganizationInviteToken(rawToken string) string {
	tokenHash := sha256.Sum256([]byte(rawToken))
	return hex.EncodeToString(tokenHash[:])
}
