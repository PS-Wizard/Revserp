package auth

import (
	"context"
	"errors"
	"strings"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

// Verifier verifies Supabase access tokens using JWKS.
type Verifier struct {
	provider string
	issuer   string
	audience string
	keyfunc  keyfunc.Keyfunc
}

// NewVerifier creates a JWT verifier backed by a cached JWKS.
func NewVerifier(ctx context.Context, provider string, issuer string, jwksURL string, audience string) (*Verifier, error) {
	jwks, err := keyfunc.NewDefaultCtx(ctx, []string{jwksURL})
	if err != nil {
		return nil, err
	}

	return &Verifier{
		provider: provider,
		issuer:   issuer,
		audience: audience,
		keyfunc:  jwks,
	}, nil
}

// Verify verifies a raw bearer token and returns the mapped identity.
func (v *Verifier) Verify(token string) (Identity, error) {
	claims := new(SupabaseClaims)
	parsedToken, err := jwt.ParseWithClaims(token, claims, v.keyfunc.Keyfunc,
		jwt.WithIssuer(v.issuer),
		jwt.WithAudience(v.audience),
		jwt.WithValidMethods([]string{jwt.SigningMethodES256.Alg()}),
	)
	if err != nil {
		return Identity{}, err
	}
	if !parsedToken.Valid {
		return Identity{}, errors.New("invalid token")
	}
	if strings.TrimSpace(claims.Subject) == "" {
		return Identity{}, errors.New("missing subject")
	}

	return Identity{
		Provider: v.provider,
		Subject:  claims.Subject,
		Email:    claims.Email,
		Name:     claims.UserMetadata.Name,
	}, nil
}
