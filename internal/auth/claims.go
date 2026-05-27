package auth

import "github.com/golang-jwt/jwt/v5"

// Identity is the authenticated user identity from the auth provider.
type Identity struct {
	Provider string
	Subject  string
	Email    string
	Name     string
}

// SupabaseClaims are the JWT claims used by Supabase Auth.
type SupabaseClaims struct {
	Email        string `json:"email"`
	UserMetadata struct {
		Name string `json:"name"`
	} `json:"user_metadata"`
	jwt.RegisteredClaims
}
