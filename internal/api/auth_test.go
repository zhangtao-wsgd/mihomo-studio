package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsLoopbackListener(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8080", true},
		{"127.0.0.1:80", true},
		{"[::1]:8080", true},
		{"localhost:8080", true},
		{":8080", false},        // every interface
		{"0.0.0.0:8080", false}, // every interface
		{"192.168.152.130:8080", false},
		{"garbage", false}, // unparseable must not be treated as safe
		{"", false},
	}
	for _, tc := range cases {
		if got := IsLoopbackListener(tc.addr); got != tc.want {
			t.Fatalf("IsLoopbackListener(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

// With no token configured the middleware must be a pass-through, so a loopback-only
// development run keeps working without a credential.
func TestRequireAuthDisabledAllowsEverything(t *testing.T) {
	handler := requireAuth(AuthConfig{}, discardLogger(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/v1/rules", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 when auth is disabled", response.Code)
	}
}

func TestRequireAuthRejectsMissingToken(t *testing.T) {
	handler := requireAuth(AuthConfig{Token: "s3cret"}, discardLogger(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("the protected handler must not run without a credential")
	}))
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/v1/rules", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.Code)
	}
	if response.Header().Get("WWW-Authenticate") == "" {
		t.Fatal("a 401 should advertise the expected scheme")
	}
}

func TestRequireAuthRejectsWrongToken(t *testing.T) {
	handler := requireAuth(AuthConfig{Token: "s3cret"}, discardLogger(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("the protected handler must not run with a wrong credential")
	}))
	for _, supplied := range []string{"wrong", "s3cre", "s3crett", ""} {
		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/v1/rules", nil)
		if supplied != "" {
			request.Header.Set(adminTokenHeader, supplied)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("token %q: status = %d, want 401", supplied, response.Code)
		}
	}
}

func TestRequireAuthAcceptsCorrectToken(t *testing.T) {
	var reached bool
	handler := requireAuth(AuthConfig{Token: "s3cret"}, discardLogger(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/v1/rules", nil)
	request.Header.Set(adminTokenHeader, "s3cret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if !reached || response.Code != http.StatusNoContent {
		t.Fatalf("reached=%v status=%d", reached, response.Code)
	}
}

// The credential must never be accepted in the query string: that would place it in
// browser history, access logs, and Referer headers.
func TestRequireAuthIgnoresQueryToken(t *testing.T) {
	handler := requireAuth(AuthConfig{Token: "s3cret"}, discardLogger(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("a query-string token must not authenticate")
	}))
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/v1/rules?token=s3cret", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for a query-string credential", response.Code)
	}
}

// Static assets and the health probe must stay reachable so the shell can load and the
// container health check keeps working; they carry no desired state.
func TestRequireAuthAllowsShellAndHealth(t *testing.T) {
	handler := requireAuth(AuthConfig{Token: "s3cret"}, discardLogger(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, path := range []string{"/healthz", "/api.js", "/", "/prototype-v6.html", "/api/v1/auth/status"} {
		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080"+path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent {
			t.Fatalf("path %s: status = %d, want 204", path, response.Code)
		}
	}
}

// Every state-changing API route must be protected. This guards against a future route
// being added outside the reach of the middleware.
func TestProtectedRoutesRejectAnonymousWrites(t *testing.T) {
	server := &Server{Auth: AuthConfig{Token: "s3cret"}}
	routes := []struct{ method, path string }{
		{http.MethodPost, "/api/v1/subscriptions"},
		{http.MethodPut, "/api/v1/subscriptions/x"},
		{http.MethodDelete, "/api/v1/subscriptions/x"},
		{http.MethodPost, "/api/v1/subscriptions/x/refresh"},
		{http.MethodPost, "/api/v1/terminals"},
		{http.MethodPost, "/api/v1/proxy-groups"},
		{http.MethodPost, "/api/v1/rules"},
		{http.MethodPost, "/api/v1/config-candidates"},
		{http.MethodPost, "/api/v1/config-candidates/x/publish"},
		{http.MethodPost, "/api/v1/deployments/x/verify"},
		{http.MethodPost, "/api/v1/deployments/x/rollback"},
		{http.MethodGet, "/api/v1/nodes"},
		{http.MethodGet, "/api/v1/deployments"},
		{http.MethodGet, "/api/v1/audit-events"},
	}
	for _, route := range routes {
		request := httptest.NewRequest(route.method, "http://127.0.0.1:8080"+route.path, nil)
		request.Host = "127.0.0.1:8080"
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s: status = %d, want 401", route.method, route.path, response.Code)
		}
	}
}

func TestAuthStatusReportsRequirement(t *testing.T) {
	for _, tc := range []struct {
		config   AuthConfig
		required bool
	}{
		{AuthConfig{Token: "s3cret"}, true},
		{AuthConfig{AllowUnauthenticated: true}, false},
	} {
		server := &Server{Auth: tc.config}
		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/v1/auth/status", nil)
		request.Host = "127.0.0.1:8080"
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d", response.Code)
		}
		body := response.Body.String()
		want := `"required":false`
		if tc.required {
			want = `"required":true`
		}
		if !strings.Contains(body, want) {
			t.Fatalf("body %s does not contain %s", body, want)
		}
		// The status endpoint must never echo the secret itself.
		if tc.required && strings.Contains(body, "s3cret") {
			t.Fatal("auth status leaked the configured secret")
		}
	}
}

// The login form uses this endpoint to confirm a password before loading the console, so it
// must be protected: reaching a 200 proves the token was accepted.
func TestAuthVerifyRequiresValidToken(t *testing.T) {
	server := &Server{Auth: AuthConfig{Token: "s3cret"}}

	anonymous := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/v1/auth/verify", nil)
	anonymous.Host = "127.0.0.1:8080"
	anonymousResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(anonymousResponse, anonymous)
	if anonymousResponse.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous verify: status = %d, want 401", anonymousResponse.Code)
	}

	authorized := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/v1/auth/verify", nil)
	authorized.Host = "127.0.0.1:8080"
	authorized.Header.Set(adminTokenHeader, "s3cret")
	authorizedResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(authorizedResponse, authorized)
	if authorizedResponse.Code != http.StatusOK {
		t.Fatalf("authorized verify: status = %d, want 200", authorizedResponse.Code)
	}
	if strings.Contains(authorizedResponse.Body.String(), "s3cret") {
		t.Fatal("verify response must not echo the secret")
	}
}

// A panel with no credential configured must let the console load without a password.
func TestAuthVerifyOpenWhenDisabled(t *testing.T) {
	server := &Server{Auth: AuthConfig{AllowUnauthenticated: true}}
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/v1/auth/verify", nil)
	request.Host = "127.0.0.1:8080"
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 when authentication is disabled", response.Code)
	}
}
