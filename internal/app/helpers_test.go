package app

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadJSONOrRespond(t *testing.T) {
	t.Run("valid json returns true", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"name":"ok"}`))

		var body struct {
			Name string `json:"name"`
		}
		if !readJSONOrRespond(rec, req, &body) {
			t.Fatalf("expected true, got false")
		}
		if body.Name != "ok" {
			t.Fatalf("expected decoded name %q, got %q", "ok", body.Name)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("expected no response written, got status %d", rec.Code)
		}
	})

	t.Run("oversized body writes 413", func(t *testing.T) {
		rec := httptest.NewRecorder()
		oversized := `{"name":"` + strings.Repeat("a", maxRequestBodySize) + `"}`
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(oversized))

		var body struct {
			Name string `json:"name"`
		}
		if readJSONOrRespond(rec, req, &body) {
			t.Fatalf("expected false for oversized body")
		}
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("expected status %d, got %d", http.StatusRequestEntityTooLarge, rec.Code)
		}
	})

	t.Run("malformed json writes 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":`))

		var body struct {
			Name string `json:"name"`
		}
		if readJSONOrRespond(rec, req, &body) {
			t.Fatalf("expected false for malformed json")
		}
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
		}
	})
}
