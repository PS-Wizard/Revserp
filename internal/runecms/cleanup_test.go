package runecms

import (
	"net/http"
	"testing"
)

type cleanupTransport struct{ closed int }

func (t *cleanupTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, nil }
func (t *cleanupTransport) CloseIdleConnections()                           { t.closed++ }

func TestSessionClosesIdleConnectionsOnce(t *testing.T) {
	base := &cleanupTransport{}
	client := &http.Client{Transport: &bearerTransport{base: base}}
	session := &Session{closeHTTP: client.CloseIdleConnections}
	for range 2 {
		if err := session.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if base.closed != 1 {
		t.Fatalf("HTTP cleanup count = %d, want 1", base.closed)
	}
}
