package auth

import "context"

type apiKeyContextKey struct{}

// WithAPIKey stores API key metadata in the context.
func WithAPIKey(ctx context.Context, apiKey APIKeyMetadata) context.Context {
	return context.WithValue(ctx, apiKeyContextKey{}, apiKey)
}

// APIKeyFromContext loads API key metadata from the context.
func APIKeyFromContext(ctx context.Context) (APIKeyMetadata, bool) {
	apiKey, ok := ctx.Value(apiKeyContextKey{}).(APIKeyMetadata)
	return apiKey, ok
}
