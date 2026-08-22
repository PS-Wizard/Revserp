package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"strings"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

const (
	apiKeyPrefix    = "rvs_live_"
	setupCodePrefix = "rvs_setup_"
	secretBytes     = 32
	displayLength   = 12
)

// ErrInvalidCredential is returned for credentials that cannot authenticate.
var ErrInvalidCredential = errors.New("invalid credential")

// APIKeyManager generates and authenticates API credentials.
type APIKeyManager struct {
	queries *sqlc.Queries
	random  io.Reader
}

// APIKeyMetadata identifies the API key used for a request without exposing it.
type APIKeyMetadata struct {
	ID     string
	Prefix string
	UserID string
}

// NewAPIKeyManager creates an API key manager.
func NewAPIKeyManager(queries *sqlc.Queries) *APIKeyManager {
	return &APIKeyManager{queries: queries, random: rand.Reader}
}

// GenerateAPIKey creates a new raw API key, its safe prefix, and its hash.
func (m *APIKeyManager) GenerateAPIKey() (raw, prefix, hash string, err error) {
	return m.generate(apiKeyPrefix)
}

// GenerateSetupCode creates a new raw setup code and its hash.
func (m *APIKeyManager) GenerateSetupCode() (raw, hash string, err error) {
	raw, _, hash, err = m.generate(setupCodePrefix)
	return raw, hash, err
}

func (m *APIKeyManager) generate(prefix string) (raw, display, hash string, err error) {
	secret := make([]byte, secretBytes)
	if _, err = io.ReadFull(m.random, secret); err != nil {
		return "", "", "", err
	}
	raw = prefix + base64.RawURLEncoding.EncodeToString(secret)
	return raw, raw[:displayLength], HashCredential(raw), nil
}

// HashCredential returns the storage and lookup hash for a raw credential.
func HashCredential(raw string) string {
	digest := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(digest[:])
}

// ParseBearer strictly parses one Authorization header value.
func ParseBearer(value string) (string, error) {
	scheme, credential, ok := strings.Cut(value, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || credential == "" || strings.ContainsAny(credential, " \t\r\n,") {
		return "", ErrInvalidCredential
	}
	return credential, nil
}

// Authenticate loads an active API key and builds its local identity.
func (m *APIKeyManager) Authenticate(ctx context.Context, raw string) (Identity, APIKeyMetadata, error) {
	if !strings.HasPrefix(raw, apiKeyPrefix) {
		return Identity{}, APIKeyMetadata{}, ErrInvalidCredential
	}
	row, err := m.queries.GetActiveAPIKeyWithUserByTokenHash(ctx, HashCredential(raw))
	if err != nil {
		return Identity{}, APIKeyMetadata{}, err
	}
	if _, err := m.queries.TouchAPIKeyLastUsedAt(ctx, row.ApiKeyID); err != nil {
		log.Printf("auth: update API key last use failed: key_id=%s error=%v", row.ApiKeyID.String(), err)
	}
	name := ""
	if row.Name.Valid {
		name = row.Name.String
	}
	return Identity{
			Provider: row.AuthProvider,
			Subject:  row.AuthSubject,
			Email:    row.Email,
			Name:     name,
		}, APIKeyMetadata{
			ID:     row.ApiKeyID.String(),
			Prefix: row.TokenPrefix,
			UserID: row.UserID.String(),
		}, nil
}
