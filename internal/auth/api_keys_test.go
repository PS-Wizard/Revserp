package auth

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestGenerateAPIKey_FormatAndEntropy(t *testing.T) {
	m := NewAPIKeyManager(nil)

	raw, prefix, hash, err := m.GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey error: %v", err)
	}
	if !strings.HasPrefix(raw, apiKeyPrefix) {
		t.Fatalf("raw %q missing prefix %q", raw, apiKeyPrefix)
	}
	if prefix != raw[:displayLength] {
		t.Fatalf("prefix %q != raw[:%d] %q", prefix, displayLength, raw[:displayLength])
	}
	if len(prefix) != displayLength {
		t.Fatalf("prefix length %d != %d", len(prefix), displayLength)
	}
	if hash != HashCredential(raw) {
		t.Fatalf("hash mismatch")
	}
	if len(hash) != 64 {
		t.Fatalf("hash length %d != 64", len(hash))
	}
	if _, err := hex.DecodeString(hash); err != nil {
		t.Fatalf("hash not hex: %v", err)
	}
	encoded := strings.TrimPrefix(raw, apiKeyPrefix)
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if len(decoded) != secretBytes {
		t.Fatalf("decoded length %d != %d", len(decoded), secretBytes)
	}
	raw2, _, _, err := m.GenerateAPIKey()
	if err != nil {
		t.Fatalf("second GenerateAPIKey error: %v", err)
	}
	if raw == raw2 {
		t.Fatalf("two generated keys equal, expected random")
	}
}

func TestGenerateSetupCode_FormatAndEntropy(t *testing.T) {
	m := NewAPIKeyManager(nil)

	raw, hash, err := m.GenerateSetupCode()
	if err != nil {
		t.Fatalf("GenerateSetupCode error: %v", err)
	}
	if !strings.HasPrefix(raw, setupCodePrefix) {
		t.Fatalf("raw %q missing prefix %q", raw, setupCodePrefix)
	}
	if hash != HashCredential(raw) {
		t.Fatalf("hash mismatch")
	}
	if len(hash) != 64 {
		t.Fatalf("hash length %d != 64", len(hash))
	}
	encoded := strings.TrimPrefix(raw, setupCodePrefix)
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if len(decoded) != secretBytes {
		t.Fatalf("decoded length %d != %d", len(decoded), secretBytes)
	}
	raw2, _, err := m.GenerateSetupCode()
	if err != nil {
		t.Fatalf("second GenerateSetupCode error: %v", err)
	}
	if raw == raw2 {
		t.Fatalf("two setup codes equal, expected random")
	}
}

func TestGenerate_DeterministicReader(t *testing.T) {
	secret := bytes.Repeat([]byte{0xAB}, secretBytes)
	m := &APIKeyManager{random: bytes.NewReader(secret)}
	raw, prefix, hash, err := m.GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey error: %v", err)
	}
	expectedRaw := apiKeyPrefix + base64.RawURLEncoding.EncodeToString(secret)
	if raw != expectedRaw {
		t.Fatalf("raw %q != expected %q", raw, expectedRaw)
	}
	if prefix != expectedRaw[:displayLength] {
		t.Fatalf("prefix %q != expected %q", prefix, expectedRaw[:displayLength])
	}
	if hash != HashCredential(expectedRaw) {
		t.Fatalf("hash mismatch")
	}
	m2 := &APIKeyManager{random: bytes.NewReader(secret)}
	raw2, hash2, err := m2.GenerateSetupCode()
	if err != nil {
		t.Fatalf("GenerateSetupCode error: %v", err)
	}
	expectedRaw2 := setupCodePrefix + base64.RawURLEncoding.EncodeToString(secret)
	if raw2 != expectedRaw2 {
		t.Fatalf("setup raw %q != expected %q", raw2, expectedRaw2)
	}
	if hash2 != HashCredential(expectedRaw2) {
		t.Fatalf("setup hash mismatch")
	}
}

func TestHashCredential_Stability(t *testing.T) {
	cases := []string{
		"rvs_live_example",
		"rvs_setup_example",
		"",
		"hello world",
	}
	for _, raw := range cases {
		h1 := HashCredential(raw)
		h2 := HashCredential(raw)
		if h1 != h2 {
			t.Fatalf("hash not stable for %q: %q != %q", raw, h1, h2)
		}
		if len(h1) != 64 {
			t.Fatalf("hash length %d != 64 for %q", len(h1), raw)
		}
		decoded, err := hex.DecodeString(h1)
		if err != nil {
			t.Fatalf("hash not hex for %q: %v", raw, err)
		}
		if len(decoded) != 32 {
			t.Fatalf("decoded hash length %d != 32", len(decoded))
		}
		digest := sha256.Sum256([]byte(raw))
		expected := hex.EncodeToString(digest[:])
		if h1 != expected {
			t.Fatalf("hash %q != expected %q", h1, expected)
		}
	}
	if HashCredential("a") == HashCredential("b") {
		t.Fatal("different inputs produced same hash")
	}
}

type errReader struct{ err error }

func (r errReader) Read(_ []byte) (int, error) { return 0, r.err }

type truncatedReader struct{ n int }

func (r *truncatedReader) Read(p []byte) (int, error) {
	if r.n <= 0 {
		return 0, io.EOF
	}
	p[0] = 0x01
	r.n--
	if r.n == 0 {
		return 1, io.EOF
	}
	return 1, nil
}

func TestGenerate_RandomReaderErrors(t *testing.T) {
	wantErr := errors.New("rand failure")

	tests := []struct {
		name string
		newR func() io.Reader
	}{
		{"error reader", func() io.Reader { return errReader{wantErr} }},
		{"truncated", func() io.Reader { return &truncatedReader{n: 1} }},
		{"eof reader", func() io.Reader { return errReader{io.EOF} }},
		{"unexpected eof", func() io.Reader { return errReader{io.ErrUnexpectedEOF} }},
	}
	for _, tc := range tests {
		t.Run(tc.name+"/GenerateAPIKey", func(t *testing.T) {
			m := &APIKeyManager{random: tc.newR()}
			_, _, _, err := m.GenerateAPIKey()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if tc.name == "error reader" && !errors.Is(err, wantErr) {
				t.Fatalf("expected %v, got %v", wantErr, err)
			}
		})
		t.Run(tc.name+"/GenerateSetupCode", func(t *testing.T) {
			m := &APIKeyManager{random: tc.newR()}
			_, _, err := m.GenerateSetupCode()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if tc.name == "error reader" && !errors.Is(err, wantErr) {
				t.Fatalf("expected %v, got %v", wantErr, err)
			}
		})
	}
}

func TestParseBearer(t *testing.T) {
	valid := "rvs_live_abc123"
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "valid Bearer", input: "Bearer " + valid, want: valid},
		{name: "valid lowercase bearer", input: "bearer " + valid, want: valid},
		{name: "valid uppercase BEARER", input: "BEARER " + valid, want: valid},
		{name: "valid mixed case BeArEr", input: "BeArEr " + valid, want: valid},
		{name: "empty", input: "", wantErr: true},
		{name: "only scheme no space", input: "Bearer", wantErr: true},
		{name: "only scheme with space no credential", input: "Bearer ", wantErr: true},
		{name: "extra whitespace leading", input: " Bearer " + valid, wantErr: true},
		{name: "extra whitespace double space", input: "Bearer  " + valid, wantErr: true},
		{name: "credential trailing space", input: "Bearer " + valid + " ", wantErr: true},
		{name: "credential with space", input: "Bearer " + valid + " extra", wantErr: true},
		{name: "tab separator", input: "Bearer\t" + valid, wantErr: true},
		{name: "credential with tab", input: "Bearer " + valid + "\tmore", wantErr: true},
		{name: "credential with comma", input: "Bearer " + valid + ",other", wantErr: true},
		{name: "comma in credential", input: "Bearer a,b", wantErr: true},
		{name: "multiple credentials space", input: "Bearer token1 token2", wantErr: true},
		{name: "multiple credentials comma", input: "Bearer token1,token2", wantErr: true},
		{name: "wrong scheme Basic", input: "Basic " + valid, wantErr: true},
		{name: "wrong scheme Bear", input: "Bear " + valid, wantErr: true},
		{name: "wrong scheme empty", input: " " + valid, wantErr: true},
		{name: "credential with newline", input: "Bearer " + valid + "\n", wantErr: true},
		{name: "credential with carriage return", input: "Bearer " + valid + "\r", wantErr: true},
		{name: "only spaces", input: "   ", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseBearer(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseBearer(%q) expected error, got %q", tc.input, got)
				}
				if !errors.Is(err, ErrInvalidCredential) {
					t.Fatalf("ParseBearer(%q) error = %v, want ErrInvalidCredential", tc.input, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseBearer(%q) unexpected error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("ParseBearer(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
