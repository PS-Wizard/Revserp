package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// SupabaseClient exchanges credentials and refresh tokens with Supabase Auth.
type SupabaseClient struct {
	httpClient *http.Client
	baseURL    string
	anonKey    string
}

// SupabaseSession holds the session tokens returned by Supabase Auth.
type SupabaseSession struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

// SupabaseSignUpResult captures one signup response, which may or may not include a session.
type SupabaseSignUpResult struct {
	Session *SupabaseSession
}

// SupabaseAuthError reports one non-success response from Supabase Auth.
type SupabaseAuthError struct {
	StatusCode int
	Message    string
}

func (e *SupabaseAuthError) Error() string {
	return e.Message
}

type supabaseAuthResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

type supabaseAuthErrorResponse struct {
	Msg              string `json:"msg"`
	ErrorDescription string `json:"error_description"`
	Error            string `json:"error"`
}

// SupabaseOAuthClient describes the OAuth client that asks for consent.
type SupabaseOAuthClient struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	URI     string `json:"uri"`
	LogoURI string `json:"logo_uri"`
}

// SupabaseOAuthUser describes the user Supabase tied to one authorization.
type SupabaseOAuthUser struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

// SupabaseOAuthAuthorization is one authorization request. Supabase returns
// either the request details or, when the user already consented to the same
// scopes, an auto-approved RedirectURL with no AuthorizationID.
type SupabaseOAuthAuthorization struct {
	AuthorizationID string              `json:"authorization_id"`
	RedirectURI     string              `json:"redirect_uri"`
	Scope           string              `json:"scope"`
	Client          SupabaseOAuthClient `json:"client"`
	User            SupabaseOAuthUser   `json:"user"`
	RedirectURL     string              `json:"redirect_url"`
}

// SupabaseOAuthConsent reports where to send the browser after a decision.
type SupabaseOAuthConsent struct {
	RedirectURL string `json:"redirect_url"`
}

// NewSupabaseClient builds a small Supabase Auth API client.
func NewSupabaseClient(baseURL string, anonKey string) *SupabaseClient {
	return &SupabaseClient{
		httpClient: &http.Client{Timeout: 10 * time.Second},
		baseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		anonKey:    strings.TrimSpace(anonKey),
	}
}

// SignUp creates a Supabase user account with email/password credentials.
func (client *SupabaseClient) SignUp(ctx context.Context, email string, password string, name string) (SupabaseSignUpResult, error) {
	authResponse, err := client.doAuthJSON(ctx, http.MethodPost, "/signup", map[string]any{
		"email":    email,
		"password": password,
		"data":     map[string]string{"name": name},
	})
	if err != nil {
		return SupabaseSignUpResult{}, err
	}

	session, ok := authResponse.session()
	if !ok {
		return SupabaseSignUpResult{}, nil
	}

	return SupabaseSignUpResult{Session: &session}, nil
}

// Login exchanges email/password credentials for a Supabase session.
func (client *SupabaseClient) Login(ctx context.Context, email string, password string) (SupabaseSession, error) {
	authResponse, err := client.doAuthJSON(ctx, http.MethodPost, "/token?grant_type=password", map[string]string{
		"email":    email,
		"password": password,
	})
	if err != nil {
		return SupabaseSession{}, err
	}

	session, ok := authResponse.session()
	if !ok {
		return SupabaseSession{}, &SupabaseAuthError{StatusCode: http.StatusUnauthorized, Message: "supabase auth response did not include a session"}
	}

	return session, nil
}

// Refresh exchanges one refresh token for a new Supabase session.
func (client *SupabaseClient) Refresh(ctx context.Context, refreshToken string) (SupabaseSession, error) {
	authResponse, err := client.doAuthJSON(ctx, http.MethodPost, "/token?grant_type=refresh_token", map[string]string{
		"refresh_token": refreshToken,
	})
	if err != nil {
		return SupabaseSession{}, err
	}

	session, ok := authResponse.session()
	if !ok {
		return SupabaseSession{}, &SupabaseAuthError{StatusCode: http.StatusUnauthorized, Message: "supabase auth response did not include a session"}
	}

	return session, nil
}

// OAuth consent actions accepted by Supabase Auth.
const (
	OAuthConsentApprove = "approve"
	OAuthConsentDeny    = "deny"
)

// GetOAuthAuthorization loads one pending authorization request as the user.
// Supabase requires the user access token, not the anon key, for this call.
func (client *SupabaseClient) GetOAuthAuthorization(ctx context.Context, accessToken string, authorizationID string) (SupabaseOAuthAuthorization, error) {
	var authorization SupabaseOAuthAuthorization
	if err := client.doUserJSON(ctx, http.MethodGet, "/oauth/authorizations/"+url.PathEscape(authorizationID), accessToken, nil, &authorization); err != nil {
		return SupabaseOAuthAuthorization{}, err
	}
	return authorization, nil
}

// SubmitOAuthConsent approves or denies one authorization request and returns
// the URL the browser must follow next.
func (client *SupabaseClient) SubmitOAuthConsent(ctx context.Context, accessToken string, authorizationID string, action string) (SupabaseOAuthConsent, error) {
	var consent SupabaseOAuthConsent
	payload := map[string]string{"action": action}
	if err := client.doUserJSON(ctx, http.MethodPost, "/oauth/authorizations/"+url.PathEscape(authorizationID)+"/consent", accessToken, payload, &consent); err != nil {
		return SupabaseOAuthConsent{}, err
	}
	return consent, nil
}

// doUserJSON sends one request authorized by a user access token. Supabase
// accepts a request with no Origin header, so we send none rather than
// forwarding a browser origin that may not be an allowed redirect URL.
func (client *SupabaseClient) doUserJSON(ctx context.Context, method string, path string, accessToken string, payload any, out any) error {
	if strings.TrimSpace(accessToken) == "" {
		return errors.New("missing supabase access token")
	}
	return client.doJSON(ctx, method, path, accessToken, payload, out)
}

func (client *SupabaseClient) doAuthJSON(ctx context.Context, method string, path string, payload any) (supabaseAuthResponse, error) {
	var authResponse supabaseAuthResponse
	if err := client.doJSON(ctx, method, path, client.anonKey, payload, &authResponse); err != nil {
		return supabaseAuthResponse{}, err
	}
	return authResponse, nil
}

// doJSON performs one Supabase Auth request. The apikey header always carries
// the anon key; the bearer credential is either that key or a user access
// token. A nil payload sends no body, and a nil out skips decoding.
func (client *SupabaseClient) doJSON(ctx context.Context, method string, path string, bearer string, payload any, out any) error {
	if client.baseURL == "" || client.anonKey == "" {
		return fmt.Errorf("supabase auth client is not configured")
	}

	var body io.Reader
	if payload != nil {
		payloadBytes, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal supabase auth payload: %w", err)
		}
		body = bytes.NewReader(payloadBytes)
	}

	request, err := http.NewRequestWithContext(ctx, method, client.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("build supabase auth request: %w", err)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("apikey", client.anonKey)
	request.Header.Set("Authorization", "Bearer "+bearer)

	response, err := client.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("send supabase auth request: %w", err)
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return fmt.Errorf("read supabase auth response: %w", err)
	}

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return decodeSupabaseAuthError(response.StatusCode, responseBody)
	}

	if out == nil {
		return nil
	}

	if err := json.Unmarshal(responseBody, out); err != nil {
		return fmt.Errorf("decode supabase auth response: %w", err)
	}

	return nil
}

func (response supabaseAuthResponse) session() (SupabaseSession, bool) {
	if strings.TrimSpace(response.AccessToken) == "" || strings.TrimSpace(response.RefreshToken) == "" || response.ExpiresIn <= 0 {
		return SupabaseSession{}, false
	}

	return SupabaseSession{
		AccessToken:  response.AccessToken,
		RefreshToken: response.RefreshToken,
		ExpiresAt:    time.Now().UTC().Add(time.Duration(response.ExpiresIn) * time.Second),
	}, true
}

func decodeSupabaseAuthError(statusCode int, responseBody []byte) error {
	var authError supabaseAuthErrorResponse
	if err := json.Unmarshal(responseBody, &authError); err == nil {
		for _, message := range []string{authError.ErrorDescription, authError.Msg, authError.Error} {
			message = strings.TrimSpace(message)
			if message != "" {
				return &SupabaseAuthError{StatusCode: statusCode, Message: message}
			}
		}
	}

	message := strings.TrimSpace(string(responseBody))
	if message == "" {
		message = http.StatusText(statusCode)
	}
	return &SupabaseAuthError{StatusCode: statusCode, Message: message}
}
