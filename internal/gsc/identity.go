package gsc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// googleUserinfoURL is the Google-verified identity endpoint. Override
// userinfoBaseURL in tests; never verify identity tokens locally.
const googleUserinfoURL = "https://openidconnect.googleapis.com/v1/userinfo"

// GoogleVerifiedIdentity is one Google-verified account identity: the stable
// subject plus the human-readable email, both asserted by Google.
type GoogleVerifiedIdentity struct {
	Subject string
	Email   string
}

// FetchVerifiedGoogleIdentity returns the Google-verified subject and email for
// one access token via Google's userinfo endpoint. It fails closed: any
// non-2xx response, missing subject, or unverified email is an error.
func (service *Service) FetchVerifiedGoogleIdentity(ctx context.Context, accessToken string) (GoogleVerifiedIdentity, error) {
	if strings.TrimSpace(accessToken) == "" {
		return GoogleVerifiedIdentity{}, &Error{Message: "missing Google access token"}
	}
	baseURL := service.userinfoBaseURL
	if strings.TrimSpace(baseURL) == "" {
		baseURL = googleUserinfoURL
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL, nil)
	if err != nil {
		return GoogleVerifiedIdentity{}, fmt.Errorf("build Google userinfo request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)

	response, err := service.httpClient.Do(request)
	if err != nil {
		return GoogleVerifiedIdentity{}, fmt.Errorf("send Google userinfo request: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := service.readLimitedBody(response)
	if err != nil {
		return GoogleVerifiedIdentity{}, fmt.Errorf("read Google userinfo response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return GoogleVerifiedIdentity{}, decodeGoogleAPIError(responseBody, "Failed to verify Google account identity")
	}

	var payload struct {
		Subject       string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified any    `json:"email_verified"`
	}
	if err := json.Unmarshal(responseBody, &payload); err != nil {
		return GoogleVerifiedIdentity{}, &Error{Message: "Google returned an invalid identity response"}
	}
	subject := strings.TrimSpace(payload.Subject)
	email := strings.TrimSpace(payload.Email)
	if subject == "" {
		return GoogleVerifiedIdentity{}, &Error{Message: "Google identity response is missing an account subject"}
	}
	if email == "" {
		return GoogleVerifiedIdentity{}, &Error{Message: "Google identity response is missing an account email"}
	}
	if verified, present := parseEmailVerified(payload.EmailVerified); present && !verified {
		return GoogleVerifiedIdentity{}, &Error{Message: "Google account email is not verified"}
	}
	return GoogleVerifiedIdentity{Subject: subject, Email: email}, nil
}

func parseEmailVerified(value any) (bool, bool) {
	switch typed := value.(type) {
	case nil:
		return false, false
	case bool:
		return typed, true
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "1", "yes":
			return true, true
		case "false", "0", "no":
			return false, true
		}
	}
	return false, false
}
