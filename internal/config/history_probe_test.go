package config

import (
	"net/url"
	"testing"
)

func TestDiffNames(t *testing.T) {
	added, removed, unchanged := diffNames(
		map[string]string{"A": "ss", "C": "vmess"},
		map[string]string{"A": "ss", "B": "ss"},
	)
	if len(added) != 1 || added[0] != "C" {
		t.Fatalf("added=%v", added)
	}
	if len(removed) != 1 || removed[0] != "B" {
		t.Fatalf("removed=%v", removed)
	}
	if unchanged != 1 {
		t.Fatalf("unchanged=%d, want 1", unchanged)
	}
}

func TestPossibleRenamesOnlySuggestsUnambiguousSameTypePair(t *testing.T) {
	added := []string{"new-node"}
	removed := []string{"old-node"}
	got := possibleRenames(added, removed, map[string]string{"new-node": "ss"}, map[string]string{"old-node": "ss"})
	if len(got) != 1 || got[0]["from"] != "old-node" || got[0]["to"] != "new-node" {
		t.Fatalf("unexpected rename hints: %#v", got)
	}
	got = possibleRenames([]string{"new-1", "new-2"}, removed, map[string]string{"new-1": "ss", "new-2": "ss"}, map[string]string{"old-node": "ss"})
	if len(got) != 0 {
		t.Fatalf("ambiguous rename should not be suggested: %#v", got)
	}
}

func TestEqualStringsIsOrderSensitive(t *testing.T) {
	if !equalStrings([]string{"a", "b"}, []string{"a", "b"}) {
		t.Fatal("identical slices should match")
	}
	if equalStrings([]string{"a", "b"}, []string{"b", "a"}) {
		t.Fatal("proxy member order must be preserved")
	}
	if equalStrings([]string{"a"}, []string{"a", "b"}) {
		t.Fatal("different lengths must not match")
	}
}

func TestProbeEndpointEscapesProxyName(t *testing.T) {
	got := probeEndpoint("http://127.0.0.1:9090/", "LA / Premium")
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse endpoint: %v", err)
	}
	if u.EscapedPath() != "/proxies/LA%20%2F%20Premium/delay" {
		t.Fatalf("unexpected escaped path: %q", u.EscapedPath())
	}
	if u.Query().Get("url") != "https://www.gstatic.com/generate_204" {
		t.Fatalf("unexpected probe URL: %q", u.Query().Get("url"))
	}
	if u.Query().Get("timeout") != "2000" {
		t.Fatalf("unexpected timeout: %q", u.Query().Get("timeout"))
	}
}

// Candidate composition renames every proxy to "<subscription> / <source name>" before it
// reaches Mihomo, so the runtime only knows the rendered name. Probing with the bare
// source name returns 404 and used to be misreported as not_loaded for loaded nodes.
func TestRuntimeProxyNameUsesRenderedNameNotSourceName(t *testing.T) {
	node := Node{Name: "主力机场 / 香港 IEPL 01", SourceName: "香港 IEPL 01"}
	if got := runtimeProxyName(node); got != "主力机场 / 香港 IEPL 01" {
		t.Fatalf("runtimeProxyName() = %q, want the rendered name", got)
	}
	if runtimeProxyName(node) == node.SourceName {
		t.Fatal("probing with the source name 404s against a live Mihomo runtime")
	}
	// The rendered name contains a separator and spaces, which must be escaped.
	u, err := url.Parse(probeEndpoint("http://127.0.0.1:9090", runtimeProxyName(node)))
	if err != nil {
		t.Fatal(err)
	}
	if u.EscapedPath() != "/proxies/%E4%B8%BB%E5%8A%9B%E6%9C%BA%E5%9C%BA%20%2F%20%E9%A6%99%E6%B8%AF%20IEPL%2001/delay" {
		t.Fatalf("unexpected escaped path: %q", u.EscapedPath())
	}
}
