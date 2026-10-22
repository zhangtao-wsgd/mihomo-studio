package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sha256Hex matches the digest the store records for an immutable candidate.
func sha256Hex(t *testing.T, body string) string {
	t.Helper()
	digest := sha256.Sum256([]byte(body))
	return hex.EncodeToString(digest[:])
}

// The YAML viewer is how an operator inspects a candidate before confirming a publish, so it
// must return the exact recorded bytes and must refuse to present a snapshot whose hash no
// longer matches the record.
func TestCandidateContentReturnsRecordedBytes(t *testing.T) {
	server := newOverviewServer(t)
	dir := t.TempDir()
	body := "mode: rule\nproxies: []\nproxy-groups: []\nrules:\n  - MATCH,DIRECT\n"
	path := filepath.Join(dir, "candidate.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	sha := sha256Hex(t, body)
	const candidateID = "cand-content"
	if _, err := server.Store.DB.Exec(`INSERT INTO config_candidates(id,sha256,snapshot_path,status,validation_message,source_fingerprint,created_at) VALUES(?,?,?,?,?,?,?)`,
		candidateID, sha, path, "validated", "ok", "fp", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}

	response := doRequest(t, server, http.MethodGet, "/api/v1/config-candidates/"+candidateID+"/content")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		ID      string `json:"id"`
		SHA256  string `json:"sha256"`
		Status  string `json:"status"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Content != body {
		t.Fatalf("content = %q, want %q", payload.Content, body)
	}
	if payload.SHA256 != sha || payload.Status != "validated" {
		t.Fatalf("unexpected metadata: %+v", payload)
	}
}

// A snapshot that no longer matches its recorded hash must not be shown as authoritative.
func TestCandidateContentRejectsHashMismatch(t *testing.T) {
	server := newOverviewServer(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "tampered.yaml")
	if err := os.WriteFile(path, []byte("mode: rule\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const candidateID = "cand-tampered"
	if _, err := server.Store.DB.Exec(`INSERT INTO config_candidates(id,sha256,snapshot_path,status,validation_message,source_fingerprint,created_at) VALUES(?,?,?,?,?,?,?)`,
		candidateID, "0000000000000000000000000000000000000000000000000000000000000000", path, "validated", "", "fp", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	response := doRequest(t, server, http.MethodGet, "/api/v1/config-candidates/"+candidateID+"/content")
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 on a hash mismatch", response.Code)
	}
	if !strings.Contains(response.Body.String(), "hash mismatch") {
		t.Fatalf("error should name the mismatch: %s", response.Body.String())
	}
}

func TestCandidateContentUnknownIsNotFound(t *testing.T) {
	server := newOverviewServer(t)
	response := doRequest(t, server, http.MethodGet, "/api/v1/config-candidates/missing/content")
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestCandidateContentRequiresAuth(t *testing.T) {
	server := newOverviewServer(t)
	server.Auth = AuthConfig{Token: "s3cret"}
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/v1/config-candidates/x/content", nil)
	request.Host = "127.0.0.1:8080"
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.Code)
	}
}
