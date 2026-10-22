package api

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// webDirForTest points the server at the repository's real assets instead of depending on the
// process working directory, which during a package test is the package folder.
func webDirForTest(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "web"))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// The shell and its script are served from unversioned URLs. Without an explicit Cache-Control
// a browser may keep using an older copy after an upgrade, which makes a working server look
// broken — the exact symptom of a stale bundle after the login page was introduced.
func TestShellAssetsAreNotCachedStale(t *testing.T) {
	server := newOverviewServer(t)
	server.WebDir = webDirForTest(t)
	for _, path := range []string{"/", "/prototype-v6.html", "/api.js"} {
		response := doRequest(t, server, http.MethodGet, path)
		if response.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", path, response.Code)
		}
		if got := response.Header().Get("Cache-Control"); got != "no-cache" {
			t.Fatalf("%s: Cache-Control = %q, want no-cache", path, got)
		}
	}
}

func TestShellAssetsDeclareUsableContentType(t *testing.T) {
	server := newOverviewServer(t)
	server.WebDir = webDirForTest(t)
	cases := map[string]string{
		"/":       "text/html",
		"/api.js": "javascript",
	}
	for path, want := range cases {
		response := doRequest(t, server, http.MethodGet, path)
		got := response.Header().Get("Content-Type")
		if !strings.Contains(got, want) {
			t.Fatalf("%s: Content-Type = %q, want it to contain %q", path, got, want)
		}
	}
}

// A missing asset directory must be an honest 404 rather than a blank 200.
func TestShellMissingAssetIsNotFound(t *testing.T) {
	server := newOverviewServer(t)
	server.WebDir = t.TempDir()
	response := doRequest(t, server, http.MethodGet, "/")
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when WebDir has no shell", response.Code)
	}
}

// The served shell must be the one this repository ships: it has to contain the login gate and
// must not contain the removed prompt-based flow or prototype wording.
func TestServedShellMatchesRepository(t *testing.T) {
	server := newOverviewServer(t)
	server.WebDir = webDirForTest(t)

	shell := doRequest(t, server, http.MethodGet, "/")
	if shell.Code != http.StatusOK {
		t.Fatalf("status = %d", shell.Code)
	}
	body := shell.Body.String()
	if !strings.Contains(body, `id="loginGate"`) {
		t.Fatal("served shell is missing the login gate")
	}
	if strings.Contains(body, "原型") || strings.Contains(body, "演示") {
		t.Fatal("served shell still claims prototype behaviour")
	}

	script := doRequest(t, server, http.MethodGet, "/api.js")
	scriptBody := script.Body.String()
	if !strings.Contains(scriptBody, "function submitLogin") {
		t.Fatal("served script is missing the login handler")
	}
	if strings.Contains(scriptBody, "promptForToken") {
		t.Fatal("served script still contains the removed prompt flow")
	}
}
