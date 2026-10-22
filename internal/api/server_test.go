package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSameOriginBlocksCrossOriginRequests(t *testing.T) {
	handler := sameOrigin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/v1/rules", nil)
	request.Host = "127.0.0.1:8080"
	request.Header.Set("Origin", "https://attacker.example")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want %d", response.Code, http.StatusForbidden)
	}
}

func TestSameOriginAllowsMatchingOrigin(t *testing.T) {
	handler := sameOrigin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/v1/rules", nil)
	request.Host = "127.0.0.1:8080"
	request.Header.Set("Origin", "http://127.0.0.1:8080")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status=%d, want %d", response.Code, http.StatusNoContent)
	}
}
