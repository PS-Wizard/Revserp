package serper

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestMapsListingIdentityEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, body           string
		status               int
		wantErr, coordinates bool
	}{
		{"identity and coordinates", `{"credits":3,"ll":"@27.700000,85.300000,14z","places":[{"placeId":"test-place","cid":"123","latitude":27.7,"longitude":85.3}]}`, 200, false, true},
		{"missing coordinate remains missing", `{"credits":3,"ll":"@27.700000,85.300000,14z","places":[{"placeId":"test-place","longitude":85.3}]}`, 200, false, false},
		{"explicit zero coordinates are present", `{"credits":3,"ll":"@27.700000,85.300000,14z","places":[{"placeId":"test-place","latitude":0,"longitude":0}]}`, 200, false, true},
		{"charged failure", `{"credits":3,"message":"provider failed"}`, 503, true, false},
		{"charged missing array", `{"credits":3,"ll":"@27.700000,85.300000,14z"}`, 200, true, false},
		{"charged wrong viewport", `{"credits":3,"ll":"@20.000000,80.000000,14z","places":[]}`, 200, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodPost || r.Header.Get("X-API-KEY") != "offline-test" {
					t.Error("unexpected Maps request")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			result, err := NewClient("offline-test", server.URL, "", "").LookupMapsListing(context.Background(), "test business", 27.7, 85.3)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error=%v wantError=%v", err, tc.wantErr)
			}
			if result.Credits != 3 {
				t.Fatalf("lost decoded charge: %d", result.Credits)
			}
			if calls.Load() != 1 {
				t.Fatalf("paid request retried: %d", calls.Load())
			}
			if len(result.Places) > 0 && result.Places[0].HasMapCoordinates() != tc.coordinates {
				t.Fatal("coordinate presence changed")
			}
		})
	}
}
