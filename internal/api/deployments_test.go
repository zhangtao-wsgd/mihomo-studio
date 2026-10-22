package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"mihomo-studio/internal/store"
)

func newDeploymentTestServer(t *testing.T) *Server {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DB.Close() })
	return &Server{Store: s, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func doRequest(t *testing.T, server *Server, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, nil)
	request.Host = "127.0.0.1:8080"
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}

// The history endpoint is the root of the release center, so an empty database must
// return an empty array and a total, not null and not an error.
func TestListDeploymentsEmpty(t *testing.T) {
	server := newDeploymentTestServer(t)
	response := doRequest(t, server, http.MethodGet, "/api/v1/deployments")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var body struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
		Limit int              `json:"limit"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Items == nil {
		t.Fatalf("items encoded as null: %s", response.Body.String())
	}
	if len(body.Items) != 0 || body.Total != 0 {
		t.Fatalf("unexpected body: %s", response.Body.String())
	}
	if body.Limit != defaultDeploymentPageSize {
		t.Fatalf("default limit = %d, want %d", body.Limit, defaultDeploymentPageSize)
	}
}

func TestListDeploymentsRejectsBadQueryParameters(t *testing.T) {
	server := newDeploymentTestServer(t)
	cases := []string{
		"/api/v1/deployments?limit=abc",
		"/api/v1/deployments?limit=-1",
		"/api/v1/deployments?offset=-5",
		"/api/v1/deployments?limit=9999",
		"/api/v1/deployments?status=bogus",
		"/api/v1/deployments?operation=delete",
	}
	for _, target := range cases {
		response := doRequest(t, server, http.MethodGet, target)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400 (body %s)", target, response.Code, response.Body.String())
		}
	}
}

func TestGetDeploymentUnknownIsNotFound(t *testing.T) {
	server := newDeploymentTestServer(t)
	response := doRequest(t, server, http.MethodGet, "/api/v1/deployments/does-not-exist")
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

// An unknown deployment must be 404 rather than an empty event list, so a typo is
// distinguishable from a deployment that recorded nothing.
func TestGetDeploymentEventsUnknownIsNotFound(t *testing.T) {
	server := newDeploymentTestServer(t)
	response := doRequest(t, server, http.MethodGet, "/api/v1/deployments/nope/events")
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestAuditEventsRejectsBadPagination(t *testing.T) {
	server := newDeploymentTestServer(t)
	for _, target := range []string{"/api/v1/audit-events?limit=0", "/api/v1/audit-events?offset=-1", "/api/v1/audit-events?limit=x"} {
		response := doRequest(t, server, http.MethodGet, target)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", target, response.Code)
		}
	}
}

func TestAuditEventsEmptyIsArray(t *testing.T) {
	server := newDeploymentTestServer(t)
	response := doRequest(t, server, http.MethodGet, "/api/v1/audit-events")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Items == nil {
		t.Fatalf("items encoded as null: %s", response.Body.String())
	}
}
