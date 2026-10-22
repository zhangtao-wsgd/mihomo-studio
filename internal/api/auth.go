package api

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
)

// Access control follows the deployment's single-secret model.
//
// MIHOMO_SECRET already protects the Mihomo controller, and it is also what the manager
// writes into the generated `config.yaml` as `secret:`. Reusing that same value as the
// panel credential means an operator only ever remembers one password, and rotating it in
// one place keeps the controller and the panel in step.
//
// The threat this targets is accidental open access on a trusted LAN: a neighbour's device
// opening the panel and changing routing. It is not a hardened multi-user auth system.
// What it does guarantee is that a request without the correct secret cannot change
// desired state, and that a wrong guess is compared in constant time.

// adminTokenHeader carries the panel credential. A header is used rather than a query
// parameter so the secret never lands in browser history, access logs, or Referer headers.
const adminTokenHeader = "X-Admin-Token"

// AuthConfig describes how the control API is protected.
type AuthConfig struct {
	// Token is the required credential. Empty means the panel is unauthenticated.
	Token string
	// AllowUnauthenticated records that the operator explicitly accepted running without
	// a credential because the listener is loopback-only.
	AllowUnauthenticated bool
}

// Enabled reports whether requests must present a credential.
func (c AuthConfig) Enabled() bool { return strings.TrimSpace(c.Token) != "" }

// IsLoopbackListener reports whether a listen address can only be reached from this host.
// An empty host (":8080") listens on every interface and is NOT loopback. An address
// without a port is not a valid listen address and is treated as non-loopback, so callers
// err toward requiring a credential.
func IsLoopbackListener(listenAddr string) bool {
	host, _, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return false
	}
	host = strings.Trim(host, "[]")
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// requireAuth rejects requests that do not carry the configured credential.
//
// Comparison is constant time so that repeated guesses cannot be distinguished by
// response timing. The response deliberately does not say whether the token was missing
// or wrong.
func requireAuth(config AuthConfig, next http.Handler) http.Handler {
	if !config.Enabled() {
		return next
	}
	expected := []byte(strings.TrimSpace(config.Token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Static assets and the health probe cannot carry a custom header from a browser
		// load, so they are served without a credential. They expose no desired state.
		if isUnauthenticatedPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		provided := []byte(strings.TrimSpace(r.Header.Get(adminTokenHeader)))
		if len(provided) != len(expected) || subtle.ConstantTimeCompare(provided, expected) != 1 {
			w.Header().Set("WWW-Authenticate", `Token realm="mihomo-studio"`)
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "a valid " + adminTokenHeader + " header is required"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isUnauthenticatedPath lists the only resources reachable without a credential.
func isUnauthenticatedPath(path string) bool {
	switch path {
	case "/healthz", "/api.js", "/", "/prototype-v6.html", "/api/v1/auth/status":
		return true
	}
	return false
}
