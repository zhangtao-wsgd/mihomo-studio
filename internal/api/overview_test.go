package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"mihomo-studio/internal/config"
	"mihomo-studio/internal/domain"
	"mihomo-studio/internal/store"
	"mihomo-studio/internal/workflow"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func containsSubstring(haystack, needle string) bool { return strings.Contains(haystack, needle) }

// domainSubscription builds a minimal valid subscription record.
func domainSubscription(url string) domain.Subscription {
	return domain.Subscription{ID: "sub-test", Name: "测试订阅", URL: url, Enabled: true}
}

// newOverviewServer builds a Server backed by a real migrated database and a real builder,
// with no controller configured so the runtime status path is exercised deterministically.
func newOverviewServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "overview.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DB.Close() })
	builder := &config.Builder{DB: s.DB, SnapshotDir: filepath.Join(dir, "snapshots")}
	service := &workflow.Service{DB: s.DB, Builder: builder, ActivePath: filepath.Join(dir, "active", "config.yaml")}
	return &Server{
		Store:            s,
		Workflow:         service,
		Logger:           discardLogger(),
		ActiveConfigPath: filepath.Join(dir, "active", "config.yaml"),
	}
}

func TestOverviewEmptyDatabase(t *testing.T) {
	server := newOverviewServer(t)
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/v1/overview", nil)
	request.Host = "127.0.0.1:8080"
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	// Arrays must serialize as [] so the client can iterate without a null check.
	for _, key := range []string{"deployments", "subscriptions"} {
		items, ok := body[key].([]any)
		if !ok {
			t.Fatalf("%s encoded as %T, want []", key, body[key])
		}
		if len(items) != 0 {
			t.Fatalf("%s should be empty, got %d", key, len(items))
		}
	}
	// With no inputs the health check must already be blocking, not silently healthy.
	health := body["health"].(map[string]any)
	if health["grade"] != "blocked" {
		t.Fatalf("an empty installation should report blocked, got %v", health["grade"])
	}
	// The runtime was never configured, so nothing may be claimed about it.
	runtime := body["runtime"].(map[string]any)
	if runtime["configured"] != false || runtime["reachable"] != false {
		t.Fatalf("runtime must not claim reachability: %#v", runtime)
	}
	if runtime["error"] == "" {
		t.Fatal("an unconfigured runtime should explain why")
	}
	// The settings page reads these, so they must reflect the real process, not a sample.
	cfg := body["config"].(map[string]any)
	if cfg["activeConfigPath"] != server.ActiveConfigPath {
		t.Fatalf("activeConfigPath = %v, want %v", cfg["activeConfigPath"], server.ActiveConfigPath)
	}
}

// The overview must never expose a subscription URL, because providers embed access tokens.
func TestOverviewDoesNotLeakSubscriptionURL(t *testing.T) {
	server := newOverviewServer(t)
	const secretURL = "https://provider.invalid/sub/SECRET-TOKEN-VALUE"
	if err := server.Store.SaveSubscription(context.Background(), domainSubscription(secretURL)); err != nil {
		t.Fatal(err)
	}
	response := doRequest(t, server, http.MethodGet, "/api/v1/overview")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if containsSubstring(response.Body.String(), "SECRET-TOKEN-VALUE") {
		t.Fatal("overview leaked the subscription URL, which may embed an access token")
	}
	if !containsSubstring(response.Body.String(), "测试订阅") {
		t.Fatal("overview should still report the subscription by name")
	}
}

func TestRuntimeEndpointReportsUnconfigured(t *testing.T) {
	server := newOverviewServer(t)
	response := doRequest(t, server, http.MethodGet, "/api/v1/runtime")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	var status config.RuntimeStatus
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Configured || status.Reachable {
		t.Fatalf("unexpected status: %+v", status)
	}
	if status.CheckedAt == "" {
		t.Fatal("CheckedAt should be populated so the UI can show freshness")
	}
}

func TestOverviewRequiresAuthWhenEnabled(t *testing.T) {
	server := newOverviewServer(t)
	server.Auth = AuthConfig{Token: "s3cret"}
	response := doRequest(t, server, http.MethodGet, "/api/v1/overview")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.Code)
	}
}
