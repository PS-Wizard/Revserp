package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

func (client *SupabaseClient) doAuthJSON(ctx context.Context, method string, path string, payload any) (supabaseAuthResponse, error) {
	if client.baseURL == "" || client.anonKey == "" {
		return supabaseAuthResponse{}, fmt.Errorf("supabase auth client is not configured")
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return supabaseAuthResponse{}, fmt.Errorf("marshal supabase auth payload: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, method, client.baseURL+path, bytes.NewReader(payloadBytes))
	if err != nil {
		return supabaseAuthResponse{}, fmt.Errorf("build supabase auth request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("apikey", client.anonKey)
	request.Header.Set("Authorization", "Bearer "+client.anonKey)

	response, err := client.httpClient.Do(request)
	if err != nil {
		return supabaseAuthResponse{}, fmt.Errorf("send supabase auth request: %w", err)
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return supabaseAuthResponse{}, fmt.Errorf("read supabase auth response: %w", err)
	}

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return supabaseAuthResponse{}, decodeSupabaseAuthError(response.StatusCode, responseBody)
	}

	var authResponse supabaseAuthResponse
	if err := json.Unmarshal(responseBody, &authResponse); err != nil {
		return supabaseAuthResponse{}, fmt.Errorf("decode supabase auth response: %w", err)
	}

	return authResponse, nil
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
