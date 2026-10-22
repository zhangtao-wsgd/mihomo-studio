package config

import (
	"net"
	"testing"
)

func TestIsPublicAddress(t *testing.T) {
	cases := []struct {
		address string
		want    bool
	}{
		{"1.1.1.1", true},
		{"192.168.1.1", false},
		{"127.0.0.1", false},
		{"169.254.10.20", false},
		{"100.64.0.1", false},
		{"192.0.2.1", false},
		{"::1", false},
		{"2606:4700:4700::1111", true},
		// RFC 2544 benchmarking space is intentionally allowed: real subscription hosts
		// publish it, and so do Mihomo TUN fake-IP pools. See nonPublicNetworks.
		{"198.18.0.82", true},
		{"198.19.255.1", true},
		// The neighbouring documentation range stays blocked.
		{"198.51.100.1", false},
	}
	for _, tc := range cases {
		t.Run(tc.address, func(t *testing.T) {
			if got := isPublicAddress(net.ParseIP(tc.address)); got != tc.want {
				t.Fatalf("isPublicAddress(%s)=%v, want %v", tc.address, got, tc.want)
			}
		})
	}
}

func TestMatchFilter(t *testing.T) {
	if !matchFilter("US-West / LA-01", map[string]any{"keyword": "la-01"}) {
		t.Fatal("keyword filter should be case-insensitive")
	}
	if matchFilter("US-West / LA-01", map[string]any{"keyword": "Tokyo"}) {
		t.Fatal("keyword filter unexpectedly matched")
	}
	if !matchFilter("US-West / LA-01", map[string]any{"regex": "^US-.*LA-01$"}) {
		t.Fatal("regex filter should match")
	}
	if matchFilter("US-West / LA-01", map[string]any{"regex": "["}) {
		t.Fatal("invalid regex must not match")
	}
}

func TestWriteImmutableRefusesDifferentContent(t *testing.T) {
	path := t.TempDir() + "/candidate.yaml"
	if err := writeImmutable(path, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := writeImmutable(path, []byte("first")); err != nil {
		t.Fatalf("identical snapshot should be idempotent: %v", err)
	}
	if err := writeImmutable(path, []byte("second")); err == nil {
		t.Fatal("different bytes must not overwrite an immutable snapshot")
	}
}
