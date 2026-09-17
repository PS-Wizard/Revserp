package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

	internalauth "github.com/ps-wizard/revserp/internal/auth"
)

const mcpIdentityExtraKey = "identity"

func (a *App) mcpEnabled() bool {
	return strings.TrimSpace(a.Config.MCPResourceURL) != ""
}

func (a *App) mountMCP(r chi.Router) {
	if !a.mcpEnabled() {
		return
	}
	metadata := mcpauth.ProtectedResourceMetadataHandler(a.mcpProtectedResourceMetadata())
	r.Handle("/.well-known/oauth-protected-resource", metadata)
	r.Handle("/.well-known/oauth-protected-resource/mcp", metadata)
	r.Handle("/mcp", a.mcpHTTPHandler())
}

func (a *App) mcpProtectedResourceMetadata() *oauthex.ProtectedResourceMetadata {
	meta := &oauthex.ProtectedResourceMetadata{
		Resource:               a.Config.MCPResourceURL,
		ResourceName:           "Revserp",
		BearerMethodsSupported: []string{"header"},
	}
	if issuer := strings.TrimSpace(a.Config.SupabaseJWTIssuer); issuer != "" {
		meta.AuthorizationServers = []string{issuer}
	}
	return meta
}

func (a *App) mcpResourceMetadataURL() string {
	parsed, err := url.Parse(a.Config.MCPResourceURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	parsed.Path = "/.well-known/oauth-protected-resource/mcp"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func (a *App) mcpHTTPHandler() http.Handler {
	server := mcp.NewServer(&mcp.Implementation{Name: "revserp", Version: "0.1.0"}, nil)
	a.registerMCPTools(server)
	stream := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
	})
	bearer := mcpauth.RequireBearerToken(a.verifyMCPToken, &mcpauth.RequireBearerTokenOptions{
		ResourceMetadataURL:    a.mcpResourceMetadataURL(),
		AllowMissingExpiration: true,
	})
	return bearer(a.mcpAttachIdentity(a.requireActiveUser(a.requirePrincipal(stream))))
}

func (a *App) mcpAttachIdentity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := mcpauth.TokenInfoFromContext(r.Context())
		if info == nil {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		identity, ok := info.Extra[mcpIdentityExtraKey].(internalauth.Identity)
		if !ok || strings.TrimSpace(identity.Subject) == "" {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(internalauth.WithIdentity(r.Context(), identity)))
	})
}

func (a *App) verifyMCPToken(ctx context.Context, token string, _ *http.Request) (*mcpauth.TokenInfo, error) {
	if internalauth.IsLiveAPIKey(token) {
		if a.APIKeyManager == nil {
			return nil, fmt.Errorf("%w", mcpauth.ErrInvalidToken)
		}
		identity, meta, err := a.APIKeyManager.Authenticate(ctx, token)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", mcpauth.ErrInvalidToken, err)
		}
		return &mcpauth.TokenInfo{
			UserID: meta.UserID,
			Extra:  map[string]any{mcpIdentityExtraKey: identity},
		}, nil
	}

	if a.AuthVerifier == nil {
		return nil, fmt.Errorf("%w", mcpauth.ErrInvalidToken)
	}
	identity, exp, audience, err := a.AuthVerifier.VerifyIgnoringAudience(token)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", mcpauth.ErrInvalidToken, err)
	}
	if !mcpAudienceOK(audience, a.Config.MCPResourceURL, a.Config.SupabaseJWTAudience) {
		return nil, fmt.Errorf("%w: %w", mcpauth.ErrInvalidToken, errors.New("unexpected audience"))
	}
	return &mcpauth.TokenInfo{
		Expiration: exp,
		UserID:     identity.Subject,
		Extra:      map[string]any{mcpIdentityExtraKey: identity},
	}, nil
}

// ponytail: accept the dashboard `authenticated` audience until a Supabase
// token hook binds aud to MCPResourceURL. Reject any other unexpected aud.
func mcpAudienceOK(claims []string, resource, legacyAud string) bool {
	if len(claims) == 0 {
		return true
	}
	if oauthex.MatchesResource(claims, resource) {
		return true
	}
	if legacyAud == "" {
		return false
	}
	for _, claim := range claims {
		if claim != legacyAud {
			return false
		}
	}
	return true
}
