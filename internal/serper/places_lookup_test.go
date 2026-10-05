package serper

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPlacesPreservesChargedFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"charged provider error", 503, `{"credits":1,"message":"provider failed"}`},
		{"charged decode error", 200, `{"credits":1,"places":"invalid"}`},
		{"charged malformed success", 200, `{"credits":1,"message":"provider failed"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client := NewClient("test-key", "", server.URL, "")
			response, err := client.Places(context.Background(), "target business")
			if err == nil || response.Credits != 1 || calls != 1 {
				t.Fatalf("response=%+v error=%v calls=%d", response, err, calls)
			}
		})
	}
}

func TestPlacesEmptySuccessIsNotProviderFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"credits":1,"places":[]}`)) }))
	defer server.Close()
	response, err := NewClient("test-key", "", server.URL, "").Places(context.Background(), "target business")
	if err != nil || response.Credits != 1 || len(response.Places) != 0 {
		t.Fatalf("response=%+v error=%v", response, err)
	}
}
