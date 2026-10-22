package config

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Subscription sources come in three shapes in practice, and an operator rarely controls
// which one a provider serves:
//
//  1. Mihomo-compatible YAML with a top-level `proxies` list.
//  2. Base64 of a newline-separated proxy URI list (`vmess://`, `vless://`, ...).
//  3. A plain newline-separated proxy URI list, not encoded.
//
// All three are normalized to Mihomo proxy maps so that candidate composition, node
// inventory, and snapshot diffing only ever deal with one representation.

// parseSubscriptionProxies converts a raw subscription body into Mihomo proxy entries.
// Each entry keeps its provider-supplied name; callers prefix it with the subscription
// name. An error means the payload is not a shape we can interpret, never a silent
// partial parse — a subscription that yields zero usable nodes is reported as such.
func parseSubscriptionProxies(body []byte) ([]map[string]any, error) {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return nil, errors.New("subscription body is empty")
	}

	// YAML first: a URI list is not valid YAML for `proxies`, and Mihomo YAML never
	// starts with a scheme. Trying YAML first avoids misreading YAML as base64, since
	// YAML often consists only of base64-safe characters.
	if proxies, ok, err := parseYAMLProxies([]byte(trimmed)); err != nil {
		return nil, err
	} else if ok {
		return proxies, nil
	}

	// Base64: providers commonly wrap the URI list without padding or line breaks.
	if decoded, err := decodeSubscriptionBase64(trimmed); err == nil {
		if proxies, ok, parseErr := parseURIProxies(decoded); parseErr != nil {
			return nil, parseErr
		} else if ok {
			return proxies, nil
		}
	}

	if proxies, ok, err := parseURIProxies(trimmed); err != nil {
		return nil, err
	} else if ok {
		return proxies, nil
	}

	return nil, errors.New("unsupported subscription format: expected Mihomo YAML with a proxies list, a Base64-encoded proxy URI list, or a plain proxy URI list")
}

// parseYAMLProxies reports ok=false when the document is YAML but carries no `proxies`
// key, so the caller can fall through to the URI formats instead of failing outright.
func parseYAMLProxies(body []byte) ([]map[string]any, bool, error) {
	var doc struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil, false, nil
	}
	if doc.Proxies == nil {
		return nil, false, nil
	}
	proxies := make([]map[string]any, 0, len(doc.Proxies))
	for _, entry := range doc.Proxies {
		name, _ := entry["name"].(string)
		if strings.TrimSpace(name) == "" {
			return nil, false, errors.New("subscription contains a proxy without a name")
		}
		proxies = append(proxies, entry)
	}
	if len(proxies) == 0 {
		return nil, false, errors.New("subscription has an empty proxies list")
	}
	return proxies, true, nil
}

// decodeSubscriptionBase64 accepts both standard and URL-safe alphabets, with or without
// padding, and tolerates embedded newlines that providers insert for readability.
func decodeSubscriptionBase64(body string) (string, error) {
	compact := strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', ' ', '\t':
			return -1
		}
		return r
	}, body)
	if compact == "" {
		return "", errors.New("empty base64 payload")
	}
	// Anything outside the alphabets means this is not base64 at all.
	for _, r := range compact {
		if !(r >= 'A' && r <= 'Z') && !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') &&
			r != '+' && r != '/' && r != '=' && r != '-' && r != '_' {
			return "", errors.New("not a base64 payload")
		}
	}
	if decoded, err := base64.StdEncoding.DecodeString(compact); err == nil {
		return string(decoded), nil
	}
	if decoded, err := base64.RawStdEncoding.DecodeString(compact); err == nil {
		return string(decoded), nil
	}
	if decoded, err := base64.URLEncoding.DecodeString(compact); err == nil {
		return string(decoded), nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(compact)
	if err != nil {
		return "", errors.New("not a base64 payload")
	}
	return string(decoded), nil
}

// parseURIProxies reports ok=false when no line carries a recognized proxy scheme, so a
// non-URI payload is not mistaken for a URI list.
func parseURIProxies(body string) ([]map[string]any, bool, error) {
	var proxies []map[string]any
	var failed []string
	schemeSeen := false
	for _, line := range strings.Split(body, "\n") {
		// Providers on Windows-hosted panels emit CRLF; a trailing \r would otherwise end
		// up inside the fragment or query and break both the name and the parse.
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		scheme, _, found := strings.Cut(line, "://")
		if !found {
			continue
		}
		scheme = strings.ToLower(scheme)
		if !supportedProxySchemes[scheme] {
			continue
		}
		schemeSeen = true
		entry, err := parseProxyURI(scheme, line)
		if err != nil {
			// Keep the provider's node name when we can recover it, so the operator can
			// identify which entry was dropped.
			failed = append(failed, fmt.Sprintf("%s: %v", scheme, err))
			continue
		}
		proxies = append(proxies, entry)
	}
	if !schemeSeen {
		return nil, false, nil
	}
	if len(proxies) == 0 {
		return nil, false, fmt.Errorf("subscription contains %d proxy URI(s) but none could be parsed: %s", len(failed), strings.Join(failed, "; "))
	}
	return proxies, true, nil
}

var supportedProxySchemes = map[string]bool{
	"vmess":  true,
	"vless":  true,
	"ss":     true,
	"trojan": true,
}

func parseProxyURI(scheme, raw string) (map[string]any, error) {
	switch scheme {
	case "vmess":
		return parseVmessURI(raw)
	case "vless":
		return parseVlessURI(raw)
	case "ss":
		return parseShadowsocksURI(raw)
	case "trojan":
		return parseTrojanURI(raw)
	}
	return nil, fmt.Errorf("unsupported scheme %q", scheme)
}

// vmessURI is the base64-of-JSON form used by most providers.
type vmessURI struct {
	Version string `json:"v"`
	Name    string `json:"ps"`
	Address string `json:"add"`
	Port    any    `json:"port"`
	ID      string `json:"id"`
	AID     any    `json:"aid"`
	Network string `json:"net"`
	Type    string `json:"type"`
	Host    string `json:"host"`
	Path    string `json:"path"`
	TLS     string `json:"tls"`
	SNI     string `json:"sni"`
}

func parseVmessURI(raw string) (map[string]any, error) {
	payload := strings.TrimPrefix(raw, "vmess://")
	decoded, err := decodeSubscriptionBase64(payload)
	if err != nil {
		return nil, errors.New("vmess payload is not valid base64")
	}
	var node vmessURI
	if err := json.Unmarshal([]byte(decoded), &node); err != nil {
		return nil, errors.New("vmess payload is not valid JSON")
	}
	if strings.TrimSpace(node.Address) == "" || strings.TrimSpace(node.ID) == "" {
		return nil, errors.New("vmess node is missing an address or id")
	}
	port, err := coercePort(node.Port)
	if err != nil {
		return nil, fmt.Errorf("vmess node has an invalid port: %w", err)
	}
	name := strings.TrimSpace(node.Name)
	if name == "" {
		name = net.JoinHostPort(node.Address, strconv.Itoa(port))
	}
	entry := map[string]any{
		"name":    name,
		"type":    "vmess",
		"server":  node.Address,
		"port":    port,
		"uuid":    node.ID,
		"alterId": coerceAlterID(node.AID),
		"cipher":  "auto",
		"udp":     true,
	}
	applyTransport(entry, node.Network, node.Type, node.Host, node.Path, node.TLS, node.SNI)
	return entry, nil
}

func parseVlessURI(raw string) (map[string]any, error) {
	// Parse the URI as-is: stripping the scheme first would make url.Parse treat the
	// remainder as a relative reference and lose the userinfo and port.
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("vless URI is malformed")
	}
	uuid := u.User.Username()
	address := u.Hostname()
	port, err := strconv.Atoi(u.Port())
	if err != nil || port <= 0 || port > 65535 {
		return nil, errors.New("vless node has an invalid port")
	}
	if uuid == "" || address == "" {
		return nil, errors.New("vless node is missing a uuid or address")
	}
	query := u.Query()
	name := u.Fragment
	if name == "" {
		name = net.JoinHostPort(address, strconv.Itoa(port))
	}
	entry := map[string]any{
		"name":   name,
		"type":   "vless",
		"server": address,
		"port":   port,
		"uuid":   uuid,
		"udp":    true,
	}
	if flow := query.Get("flow"); flow != "" {
		entry["flow"] = flow
	}
	security := query.Get("security")
	if security == "reality" {
		entry["tls"] = true
		entry["reality-opts"] = map[string]any{
			"public-key": query.Get("pbk"),
			"short-id":   query.Get("sid"),
		}
		if sni := query.Get("sni"); sni != "" {
			entry["servername"] = sni
		}
	} else if security == "tls" {
		entry["tls"] = true
		if sni := query.Get("sni"); sni != "" {
			entry["servername"] = sni
		}
	}
	applyTransport(entry, query.Get("type"), query.Get("headerType"), query.Get("host"), query.Get("path"), "", query.Get("sni"))
	return entry, nil
}

func parseShadowsocksURI(raw string) (map[string]any, error) {
	// Two forms exist: ss://base64(method:password)@host:port#name and
	// ss://base64(method:password@host:port)#name.
	trimmed := strings.TrimPrefix(raw, "ss://")
	name := ""
	if index := strings.Index(trimmed, "#"); index >= 0 {
		if decodedName, err := url.QueryUnescape(trimmed[index+1:]); err == nil {
			name = decodedName
		}
		trimmed = trimmed[:index]
	}
	authority := trimmed
	if index := strings.Index(authority, "?"); index >= 0 {
		authority = authority[:index]
	}
	var methodPassword, hostPort string
	if index := strings.LastIndex(authority, "@"); index >= 0 {
		credential, err := decodeSubscriptionBase64(authority[:index])
		if err != nil {
			return nil, errors.New("shadowsocks credential is not valid base64")
		}
		methodPassword = credential
		hostPort = authority[index+1:]
	} else {
		decoded, err := decodeSubscriptionBase64(authority)
		if err != nil {
			return nil, errors.New("shadowsocks payload is not valid base64")
		}
		split := strings.LastIndex(decoded, "@")
		if split < 0 {
			return nil, errors.New("shadowsocks payload is missing a host")
		}
		methodPassword = decoded[:split]
		hostPort = decoded[split+1:]
	}
	method, password, found := strings.Cut(methodPassword, ":")
	if !found || method == "" {
		return nil, errors.New("shadowsocks payload is missing a cipher or password")
	}
	host, portText, err := net.SplitHostPort(hostPort)
	if err != nil {
		return nil, errors.New("shadowsocks payload has an invalid host:port")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 || port > 65535 {
		return nil, errors.New("shadowsocks payload has an invalid port")
	}
	if name == "" {
		name = net.JoinHostPort(host, portText)
	}
	return map[string]any{
		"name":     name,
		"type":     "ss",
		"server":   host,
		"port":     port,
		"cipher":   method,
		"password": password,
		"udp":      true,
	}, nil
}

func parseTrojanURI(raw string) (map[string]any, error) {
	// As with VLESS, the scheme must stay in place for the authority to be parsed.
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("trojan URI is malformed")
	}
	password := u.User.Username()
	address := u.Hostname()
	port, err := strconv.Atoi(u.Port())
	if err != nil || port <= 0 || port > 65535 {
		return nil, errors.New("trojan node has an invalid port")
	}
	if password == "" || address == "" {
		return nil, errors.New("trojan node is missing a password or address")
	}
	query := u.Query()
	name := u.Fragment
	if name == "" {
		name = net.JoinHostPort(address, strconv.Itoa(port))
	}
	entry := map[string]any{
		"name":     name,
		"type":     "trojan",
		"server":   address,
		"port":     port,
		"password": password,
		"udp":      true,
	}
	if sni := query.Get("sni"); sni != "" {
		entry["sni"] = sni
	}
	applyTransport(entry, query.Get("type"), query.Get("headerType"), query.Get("host"), query.Get("path"), "", query.Get("sni"))
	return entry, nil
}

// applyTransport maps the share-link transport fields onto Mihomo's ws/grpc/http options.
func applyTransport(entry map[string]any, network, headerType, host, path, tls, sni string) {
	switch strings.ToLower(strings.TrimSpace(network)) {
	case "ws":
		options := map[string]any{"path": path}
		if host != "" {
			options["headers"] = map[string]any{"Host": host}
		}
		entry["network"] = "ws"
		entry["ws-opts"] = options
	case "grpc":
		serviceName := path
		if serviceName == "" {
			serviceName = headerType
		}
		entry["network"] = "grpc"
		entry["grpc-opts"] = map[string]any{"grpc-service-name": serviceName}
	case "h2", "http":
		options := map[string]any{"path": path}
		if host != "" {
			options["host"] = []string{host}
		}
		entry["network"] = "http"
		entry["http-opts"] = options
	case "tcp", "":
		// Mihomo defaults to tcp; only emit transport options when they carry meaning.
	}
	switch strings.ToLower(strings.TrimSpace(tls)) {
	case "tls", "reality":
		entry["tls"] = true
		if sni != "" {
			entry["servername"] = sni
		}
		if host != "" {
			if _, exists := entry["servername"]; !exists {
				entry["servername"] = host
			}
		}
	}
}

// coercePort accepts the numeric port as either a JSON number or a string, because
// providers in the wild emit both.
func coercePort(value any) (int, error) {
	var port int
	switch typed := value.(type) {
	case float64:
		port = int(typed)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0, err
		}
		port = parsed
	case int:
		port = typed
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, err
		}
		port = int(parsed)
	default:
		return 0, errors.New("port is missing")
	}
	if port <= 0 || port > 65535 {
		return 0, fmt.Errorf("port %d is out of range", port)
	}
	return port, nil
}

// coerceAlterID accepts the vmess alterId as a number or a string and defaults to 0,
// which is what current Mihomo clients use.
func coerceAlterID(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0
		}
		return parsed
	case int:
		return typed
	}
	return 0
}
