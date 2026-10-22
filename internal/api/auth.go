package api

import (
	"crypto/subtle"
	"log/slog"
	"net"
	"net/http"
	"strconv"
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
// Comparison is constant time so that repeated guesses cannot be distinguished by response
// timing. The response does not reveal the expected value. It does distinguish a missing
// header from a mismatched one, because an operator staring at "wrong password" needs to know
// whether the browser sent anything at all; that distinction tells an attacker nothing useful
// about the secret.
//
// Failures are logged with the provided length but never the value, so a truncated paste or a
// quoted secret is identifiable from the server side.
func requireAuth(config AuthConfig, logger *slog.Logger, next http.Handler) http.Handler {
	if !config.Enabled() {
		return next
	}
	expected := []byte(strings.TrimSpace(config.Token))
	logAuthFailure := func(r *http.Request, reason string, providedLen int) {
		if logger == nil {
			return
		}
		logger.Warn("admin credential rejected",
			"reason", reason,
			"path", r.URL.Path,
			"method", r.Method,
			"remote", r.RemoteAddr,
			"header", adminTokenHeader,
			"providedLength", providedLen,
			"expectedLength", len(expected),
		)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Static assets and the health probe cannot carry a custom header from a browser
		// load, so they are served without a credential. They expose no desired state.
		if isUnauthenticatedPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		raw := r.Header.Get(adminTokenHeader)
		if strings.TrimSpace(raw) == "" {
			logAuthFailure(r, "missing", 0)
			w.Header().Set("WWW-Authenticate", `Token realm="mihomo-studio"`)
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "missing " + adminTokenHeader + " header; the panel requires the configured secret"})
			return
		}
		provided := []byte(strings.TrimSpace(raw))
		if len(provided) != len(expected) || subtle.ConstantTimeCompare(provided, expected) != 1 {
			logAuthFailure(r, "mismatch", len(provided))
			w.Header().Set("WWW-Authenticate", `Token realm="mihomo-studio"`)
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "the supplied secret does not match; " + strconv.Itoa(len(provided)) + " characters received, the expected secret has " + strconv.Itoa(len(expected))})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isUnauthenticatedPath lists the only resources reachable without a credential.
//
// These carry no desired state: the browser must load the shell and its script in order to
// present the login form at all, and the container health check must keep working. The favicon
// is included because a 401 on every page load only adds noise to the audit log.
func isUnauthenticatedPath(path string) bool {
	switch path {
	case "/healthz", "/api.js", "/", "/prototype-v6.html", "/favicon.ico", "/api/v1/auth/status":
		return true
	}
	return false
}
