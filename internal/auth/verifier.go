package auth

import (
	"context"
	"errors"
	"strings"
	"time"

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
	identity, _, _, err := v.verify(token, true)
	return identity, err
}

// VerifyIgnoringAudience verifies issuer and signature but leaves audience
// checking to the caller. Used for MCP, where tokens may still carry the
// dashboard audience until a resource-bound token hook exists.
func (v *Verifier) VerifyIgnoringAudience(token string) (Identity, time.Time, []string, error) {
	return v.verify(token, false)
}

func (v *Verifier) verify(token string, requireAudience bool) (Identity, time.Time, []string, error) {
	claims := new(SupabaseClaims)
	opts := []jwt.ParserOption{
		jwt.WithIssuer(v.issuer),
		jwt.WithValidMethods([]string{jwt.SigningMethodES256.Alg()}),
	}
	if requireAudience {
		opts = append(opts, jwt.WithAudience(v.audience))
	}
	parsedToken, err := jwt.ParseWithClaims(token, claims, v.keyfunc.Keyfunc, opts...)
	if err != nil {
		return Identity{}, time.Time{}, nil, err
	}
	if !parsedToken.Valid {
		return Identity{}, time.Time{}, nil, errors.New("invalid token")
	}
	identity, err := v.identityFromClaims(claims)
	if err != nil {
		return Identity{}, time.Time{}, nil, err
	}
	var exp time.Time
	if claims.ExpiresAt != nil {
		exp = claims.ExpiresAt.Time
	}
	return identity, exp, []string(claims.Audience), nil
}

func (v *Verifier) identityFromClaims(claims *SupabaseClaims) (Identity, error) {
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
