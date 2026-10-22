package config

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestParseSubscriptionProxiesMihomoYAML(t *testing.T) {
	body := []byte("proxies:\n  - name: 香港 01\n    type: ss\n    server: 1.1.1.1\n    port: 8388\n    cipher: aes-128-gcm\n    password: x\n")
	proxies, err := parseSubscriptionProxies(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(proxies) != 1 || proxies[0]["name"] != "香港 01" || proxies[0]["type"] != "ss" {
		t.Fatalf("unexpected result: %#v", proxies)
	}
}

// Providers commonly serve a Base64 blob containing newline-separated vmess URIs. This is
// the shape of the real subscription used during development, reproduced here in full.
func TestParseSubscriptionProxiesBase64VmessList(t *testing.T) {
	node := map[string]any{
		"v": "2", "ps": "🇭🇰 Hong Kong 01", "add": "a365ca950433.334194.xyz", "port": "48026",
		"id": "75e68e75-8e75-4de5-a20b-a211d9938d8d", "aid": "0", "net": "tcp",
		"type": "none", "host": "", "path": "", "tls": "",
	}
	raw, err := json.Marshal(node)
	if err != nil {
		t.Fatal(err)
	}
	uri := "vmess://" + base64.StdEncoding.EncodeToString(raw)
	body := []byte(base64.StdEncoding.EncodeToString([]byte(uri + "\n")))

	proxies, err := parseSubscriptionProxies(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(proxies) != 1 {
		t.Fatalf("expected 1 proxy, got %d", len(proxies))
	}
	got := proxies[0]
	if got["name"] != "🇭🇰 Hong Kong 01" {
		t.Fatalf("name = %v", got["name"])
	}
	if got["type"] != "vmess" || got["server"] != "a365ca950433.334194.xyz" || got["port"] != 48026 {
		t.Fatalf("unexpected node: %#v", got)
	}
	if got["uuid"] != node["id"] {
		t.Fatalf("uuid = %v", got["uuid"])
	}
}

// Base64 without padding and with URL-safe characters must decode identically.
func TestParseSubscriptionProxiesUnpaddedBase64(t *testing.T) {
	uri := "vless://11111111-2222-3333-4444-555555555555@example.com:443?security=tls&sni=example.com&type=ws&path=%2Fws#Tokyo"
	encoded := base64.RawStdEncoding.EncodeToString([]byte(uri))
	proxies, err := parseSubscriptionProxies([]byte(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if len(proxies) != 1 || proxies[0]["type"] != "vless" {
		t.Fatalf("unexpected result: %#v", proxies)
	}
	if proxies[0]["tls"] != true || proxies[0]["servername"] != "example.com" {
		t.Fatalf("TLS fields not mapped: %#v", proxies[0])
	}
}

// A plain (unencoded) URI list is the third common shape.
func TestParseSubscriptionProxiesPlainURIList(t *testing.T) {
	body := []byte("ss://YWVzLTEyOC1nY206cGFzc3dvcmQ=@1.2.3.4:8388#NodeA\n" +
		"trojan://secret@5.6.7.8:443?sni=example.org#NodeB\n")
	proxies, err := parseSubscriptionProxies(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(proxies) != 2 {
		t.Fatalf("expected 2 proxies, got %d: %#v", len(proxies), proxies)
	}
	if proxies[0]["type"] != "ss" || proxies[0]["cipher"] != "aes-128-gcm" || proxies[0]["password"] != "password" {
		t.Fatalf("shadowsocks decode failed: %#v", proxies[0])
	}
	if proxies[0]["name"] != "NodeA" {
		t.Fatalf("fragment was not used as the name: %#v", proxies[0])
	}
	if proxies[1]["type"] != "trojan" || proxies[1]["password"] != "secret" {
		t.Fatalf("trojan decode failed: %#v", proxies[1])
	}
}

func TestParseSubscriptionProxiesRejectsUnknownPayload(t *testing.T) {
	for _, body := range []string{
		"",
		"   ",
		"<html><body>403 Forbidden</body></html>",
		"proxies: []",
	} {
		if _, err := parseSubscriptionProxies([]byte(body)); err == nil {
			t.Fatalf("expected an error for payload %q", body)
		}
	}
}

// YAML consisting only of base64-safe characters must not be mistaken for base64.
func TestParseSubscriptionProxiesPrefersYAMLWhenAmbiguous(t *testing.T) {
	body := []byte("proxies:\n- name: node1\n  type: ss\n")
	proxies, err := parseSubscriptionProxies(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(proxies) != 1 || proxies[0]["name"] != "node1" {
		t.Fatalf("YAML was not parsed first: %#v", proxies)
	}
}

func TestParseSubscriptionProxiesReportsUnparseableEntries(t *testing.T) {
	body := []byte("vmess://bm90LWpzb24=\n")
	_, err := parseSubscriptionProxies(body)
	if err == nil {
		t.Fatal("expected an error when no entry can be parsed")
	}
	if !strings.Contains(err.Error(), "none could be parsed") {
		t.Fatalf("error should explain the failure: %v", err)
	}
}

func TestCoercePortAcceptsNumberAndString(t *testing.T) {
	for _, value := range []any{float64(443), "443", 443} {
		port, err := coercePort(value)
		if err != nil || port != 443 {
			t.Fatalf("coercePort(%#v) = %d, %v", value, port, err)
		}
	}
	for _, value := range []any{float64(0), "0", float64(70000), "", nil} {
		if _, err := coercePort(value); err == nil {
			t.Fatalf("coercePort(%#v) should fail", value)
		}
	}
}
