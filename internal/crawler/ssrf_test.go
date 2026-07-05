package crawler

import (
	"net"
	"testing"
)

func TestIsDisallowedIP(t *testing.T) {
	tests := []struct {
		name string
		ip   string
		want bool
	}{
		{"loopback v4", "127.0.0.1", true},
		{"cloud metadata", "169.254.169.254", true},
		{"rfc1918 10.x", "10.0.0.5", true},
		{"rfc1918 192.168.x", "192.168.1.1", true},
		{"loopback v6", "::1", true},
		{"link-local v6", "fe80::1", true},
		{"unique-local v6", "fc00::1", true},
		{"public v4", "8.8.8.8", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			if ip == nil {
				t.Fatalf("failed to parse test IP %q", tt.ip)
			}
			if got := isDisallowedIP(ip); got != tt.want {
				t.Errorf("isDisallowedIP(%q) = %v, want %v", tt.ip, got, tt.want)
			}
		})
	}
}

func TestValidatePublicHost_IPLiterals(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		wantErr bool
	}{
		{"loopback v4", "127.0.0.1", true},
		{"cloud metadata", "169.254.169.254", true},
		{"rfc1918 10.x", "10.0.0.5", true},
		{"rfc1918 192.168.x", "192.168.1.1", true},
		{"loopback v6", "::1", true},
		{"link-local v6", "fe80::1", true},
		{"unique-local v6", "fc00::1", true},
		{"public v4", "8.8.8.8", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePublicHost(t.Context(), tt.host)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidatePublicHost(%q) err = %v, wantErr %v", tt.host, err, tt.wantErr)
			}
		})
	}
}
