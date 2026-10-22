package config

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"mihomo-studio/internal/domain"
)

type Builder struct {
	DB               *sql.DB
	SnapshotDir      string
	ControllerSecret string
	ControllerURL    string
	MihomoBinary     string
	// DashboardOrigins lists the origins allowed to call the controller from a browser.
	// Empty means the loopback defaults, which cover the bundled diagnostics dashboard only
	// when it is reached on this host.
	DashboardOrigins []string
}
type subscriptionRow struct {
	ID, Name, URL string
	Enabled       int
}
type sourceSnapshot struct {
	SubscriptionID string
	SHA256         string
	Path           string
}

type CandidateInput struct {
	SubscriptionID string `json:"subscriptionId"`
	SHA256         string `json:"sha256"`
	SnapshotPath   string `json:"snapshotPath"`
}

type Candidate struct {
	ID                string    `json:"id"`
	SHA256            string    `json:"sha256"`
	SnapshotPath      string    `json:"snapshotPath"`
	Status            string    `json:"status"`
	ValidationMessage string    `json:"validationMessage"`
	SourceFingerprint string    `json:"sourceFingerprint"`
	CreatedAt         time.Time `json:"createdAt"`
}

func (b *Builder) Build(ctx context.Context) (Candidate, error) {
	if b.SnapshotDir == "" {
		return Candidate{}, errors.New("snapshot directory is required")
	}
	if strings.TrimSpace(b.ControllerSecret) == "" {
		return Candidate{}, errors.New("Mihomo controller secret is required")
	}
	var subs []subscriptionRow
	rows, err := b.DB.QueryContext(ctx, `SELECT id,name,url,enabled FROM subscriptions WHERE enabled=1 ORDER BY id`)
	if err != nil {
		return Candidate{}, err
	}
	for rows.Next() {
		var x subscriptionRow
		if err := rows.Scan(&x.ID, &x.Name, &x.URL, &x.Enabled); err != nil {
			rows.Close()
			return Candidate{}, err
		}
		subs = append(subs, x)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Candidate{}, err
	}
	rows.Close()
	proxies := make([]map[string]any, 0)
	fingerprintParts := make([]string, 0, len(subs))
	sourceSnapshots := make([]sourceSnapshot, 0, len(subs))
	for _, sub := range subs {
		parsed, err := url.ParseRequestURI(sub.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return Candidate{}, fmt.Errorf("subscription %q has an invalid HTTP(S) URL", sub.Name)
		}
		body, err := fetchSubscription(ctx, sub.URL)
		fetchedAt := time.Now().UTC().Format(time.RFC3339Nano)
		status := "fetched"
		if err != nil {
			status = "fetch_failed"
		}
		if _, statusErr := b.DB.ExecContext(ctx, `UPDATE subscriptions SET last_fetch_at=?,last_status=?,updated_at=? WHERE id=?`, fetchedAt, status, fetchedAt, sub.ID); statusErr != nil {
			return Candidate{}, fmt.Errorf("record fetch status for subscription %q: %w", sub.Name, statusErr)
		}
		if err != nil {
			return Candidate{}, fmt.Errorf("fetch subscription %q: %w", sub.Name, err)
		}
		parsedProxies, err := parseSubscriptionProxies(body)
		if err != nil {
			return Candidate{}, fmt.Errorf("subscription %q: %w", sub.Name, err)
		}
		for _, p := range parsedProxies {
			name, _ := p["name"].(string)
			p["name"] = sub.Name + " / " + name
			proxies = append(proxies, p)
		}
		sourceDigest := sha256.Sum256(body)
		sourceHash := hex.EncodeToString(sourceDigest[:])
		inputPath := filepath.Join(b.SnapshotDir, "inputs", sub.ID, sourceHash+".yaml")
		if err := writeImmutable(inputPath, body); err != nil {
			return Candidate{}, fmt.Errorf("persist subscription input snapshot %q: %w", sub.Name, err)
		}
		if err := b.recordCurrentSnapshot(ctx, sub.ID, sourceHash, inputPath, fetchedAt, len(parsedProxies)); err != nil {
			return Candidate{}, fmt.Errorf("record current snapshot for subscription %q: %w", sub.Name, err)
		}
		sourceSnapshots = append(sourceSnapshots, sourceSnapshot{SubscriptionID: sub.ID, SHA256: sourceHash, Path: inputPath})
		fingerprintParts = append(fingerprintParts, sub.ID+":"+sourceHash)
	}
	groups, err := loadGroups(ctx, b.DB)
	if err != nil {
		return Candidate{}, err
	}
	terminals, err := loadTerminals(ctx, b.DB)
	if err != nil {
		return Candidate{}, err
	}
	rules, err := loadRules(ctx, b.DB)
	if err != nil {
		return Candidate{}, err
	}
	proxyNames := map[string]bool{}
	nodeNameByID := map[string]string{}
	for _, p := range proxies {
		proxyNames[p["name"].(string)] = true
	}
	for _, sub := range subs {
		var currentPath string
		lookupErr := b.DB.QueryRowContext(ctx, `SELECT snapshot_path FROM subscription_snapshots WHERE subscription_id=? AND is_current=1`, sub.ID).Scan(&currentPath)
		if errors.Is(lookupErr, sql.ErrNoRows) {
			continue
		}
		if lookupErr != nil {
			return Candidate{}, lookupErr
		}
		body, readErr := os.ReadFile(currentPath)
		if readErr != nil {
			return Candidate{}, readErr
		}
		var snap struct {
			Proxies []struct {
				Name string `yaml:"name"`
			} `yaml:"proxies"`
		}
		if yaml.Unmarshal(body, &snap) != nil {
			continue
		}
		for _, item := range snap.Proxies {
			nodeNameByID[stableNodeID(sub.ID, item.Name)] = sub.Name + " / " + item.Name
		}
	}
	groupNameByID := map[string]string{}
	mihomoGroups := make([]map[string]any, 0, len(groups))
	for _, g := range groups {
		groupNameByID[g.ID] = g.Name
		members := []string{}
		if g.SelectionMode == "manual" {
			if len(g.MemberNodeIDs) > 0 {
				for _, nodeID := range g.MemberNodeIDs {
					name, ok := nodeNameByID[nodeID]
					if !ok {
						return Candidate{}, fmt.Errorf("proxy group %q references missing node ID %q; refresh subscriptions and review group members", g.Name, nodeID)
					}
					if !proxyNames[name] {
						return Candidate{}, fmt.Errorf("proxy group %q references node %q that is absent from the current candidate", g.Name, name)
					}
					members = append(members, name)
				}
			} else {
				for _, name := range g.MemberNodeNames {
					full := name
					if !proxyNames[full] {
						matches := []string{}
						for pn := range proxyNames {
							if strings.HasSuffix(pn, " / "+name) {
								matches = append(matches, pn)
							}
						}
						if len(matches) == 1 {
							full = matches[0]
						} else if len(matches) > 1 {
							return Candidate{}, fmt.Errorf("proxy group %q member %q is ambiguous across subscriptions", g.Name, name)
						}
					}
					if !proxyNames[full] {
						return Candidate{}, fmt.Errorf("proxy group %q references missing node %q", g.Name, name)
					}
					members = append(members, full)
				}
			}
		} else {
			for pn := range proxyNames {
				if matchFilter(pn, g.MemberFilter) {
					members = append(members, pn)
				}
			}
			sort.Strings(members)
			// In dynamic mode, explicitly listed members define preferred order (important for fallback groups); remaining matches follow deterministically by name.
			preferredNames := append([]string(nil), g.MemberNodeNames...)
			for _, nodeID := range g.MemberNodeIDs {
				if name, ok := nodeNameByID[nodeID]; ok {
					preferredNames = append(preferredNames, name)
				}
			}
			if len(preferredNames) > 0 {
				ordered := make([]string, 0, len(members))
				used := map[string]bool{}
				for _, preferred := range preferredNames {
					for _, actual := range members {
						if actual == preferred || strings.HasSuffix(actual, " / "+preferred) {
							if !used[actual] {
								ordered = append(ordered, actual)
								used[actual] = true
							}
						}
					}
				}
				for _, actual := range members {
					if !used[actual] {
						ordered = append(ordered, actual)
					}
				}
				members = ordered
			}
		}
		if len(members) == 0 {
			return Candidate{}, fmt.Errorf("proxy group %q has no matching nodes", g.Name)
		}
		entry := map[string]any{"name": g.Name, "type": g.MihomoType, "proxies": members}
		if g.MihomoType == "url-test" || g.MihomoType == "fallback" {
			probeURL, parseErr := url.ParseRequestURI(g.HealthURL)
			if parseErr != nil || (probeURL.Scheme != "http" && probeURL.Scheme != "https") || probeURL.Host == "" {
				return Candidate{}, fmt.Errorf("proxy group %q has an invalid health-check URL", g.Name)
			}
			if g.IntervalSeconds < 10 || g.IntervalSeconds > 86400 {
				return Candidate{}, fmt.Errorf("proxy group %q interval must be between 10 and 86400 seconds", g.Name)
			}
			entry["url"] = g.HealthURL
			entry["interval"] = g.IntervalSeconds
		}
		if g.MihomoType == "url-test" {
			if g.ToleranceMS < 0 || g.ToleranceMS > 10000 {
				return Candidate{}, fmt.Errorf("proxy group %q tolerance must be between 0 and 10000 milliseconds", g.Name)
			}
			entry["tolerance"] = g.ToleranceMS
		}
		mihomoGroups = append(mihomoGroups, entry)
	}
	type terminalRoute struct {
		CIDR    string
		Target  string
		Network *net.IPNet
		Prefix  int
	}
	terminalRoutes := make([]terminalRoute, 0)
	seenTerminalIdentity := map[string]string{}
	terminalWarnings := make([]string, 0)
	addTerminalRoute := func(cidr, target, terminalName string) error {
		if previous, ok := seenTerminalIdentity[cidr]; ok {
			if previous != target {
				return fmt.Errorf("terminal identity %q maps to conflicting proxy groups", cidr)
			}
			return nil
		}
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			return fmt.Errorf("terminal %q has an invalid network: %w", terminalName, err)
		}
		prefix, _ := network.Mask.Size()
		for _, previous := range terminalRoutes {
			if previous.Network.Contains(network.IP) || network.Contains(previous.Network.IP) {
				if previous.Target != target {
					terminalWarnings = append(terminalWarnings, fmt.Sprintf("Terminal routes %s and %s overlap with different proxy groups; the more-specific CIDR is ordered first.", previous.CIDR, cidr))
				}
			}
		}
		seenTerminalIdentity[cidr] = target
		terminalRoutes = append(terminalRoutes, terminalRoute{CIDR: cidr, Target: target, Network: network, Prefix: prefix})
		return nil
	}
	for _, terminal := range terminals {
		if !terminal.Enabled {
			continue
		}
		target, exists := groupNameByID[terminal.ProxyGroupID]
		if !exists {
			return Candidate{}, fmt.Errorf("terminal %q references a missing proxy group", terminal.Name)
		}
		switch terminal.IdentityType {
		case "source_ip":
			ip := net.ParseIP(terminal.IdentityValue)
			if ip == nil {
				return Candidate{}, fmt.Errorf("terminal %q has an invalid source IP", terminal.Name)
			}
			bits := 128
			if ip.To4() != nil {
				bits = 32
			}
			if err := addTerminalRoute(fmt.Sprintf("%s/%d", ip.String(), bits), target, terminal.Name); err != nil {
				return Candidate{}, err
			}
		case "cidr":
			_, network, parseErr := net.ParseCIDR(terminal.IdentityValue)
			if parseErr != nil {
				return Candidate{}, fmt.Errorf("terminal %q has an invalid CIDR", terminal.Name)
			}
			if err := addTerminalRoute(network.String(), target, terminal.Name); err != nil {
				return Candidate{}, err
			}
		default:
			terminalWarnings = append(terminalWarnings, "Terminal "+terminal.Name+" uses "+terminal.IdentityType+" identity, which Mihomo cannot enforce as a source-IP routing rule; it remains inventory-only.")
		}
	}
	sort.Slice(terminalRoutes, func(i, j int) bool {
		if terminalRoutes[i].Prefix == terminalRoutes[j].Prefix {
			return terminalRoutes[i].CIDR < terminalRoutes[j].CIDR
		}
		return terminalRoutes[i].Prefix > terminalRoutes[j].Prefix
	})
	terminalRules := make([]string, 0, len(terminalRoutes))
	for _, route := range terminalRoutes {
		terminalRules = append(terminalRules, "SRC-IP-CIDR,"+route.CIDR+","+route.Target)
	}

	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		if r.Target != "DIRECT" && r.Target != "REJECT" {
			if _, exists := groupNameByID[r.Target]; !exists {
				return Candidate{}, fmt.Errorf("rule %q targets unknown proxy group ID %q", r.Name, r.Target)
			}
		}
	}
	sort.Slice(rules, func(i, j int) bool {
		if rules[i].Priority == rules[j].Priority {
			return rules[i].ID < rules[j].ID
		}
		return rules[i].Priority < rules[j].Priority
	})
	mihomoRules := make([]string, 0, len(terminalRules)+len(rules)+1)
	mihomoRules = append(mihomoRules, terminalRules...)
	for _, r := range rules {
		if r.Enabled {
			target := r.Target
			if target != "DIRECT" && target != "REJECT" {
				target = groupNameByID[target]
			}
			mihomoRules = append(mihomoRules, r.RuleType+","+r.RuleValue+","+target)
		}
	}
	mihomoRules = append(mihomoRules, "MATCH,DIRECT")
	cfg := map[string]any{
		"mixed-port": 7890, "allow-lan": true, "bind-address": "*", "mode": "rule", "log-level": "info",
		"external-controller":      "0.0.0.0:9090",
		"external-controller-cors": map[string]any{"allow-origins": b.corsOrigins(), "allow-private-network": false},
		"secret":                   b.ControllerSecret, "proxies": proxies, "proxy-groups": mihomoGroups, "rules": mihomoRules,
	}
	body, err := yaml.Marshal(cfg)
	if err != nil {
		return Candidate{}, err
	}
	// Parse the serialized document again before it becomes an immutable candidate.
	var parsed map[string]any
	if err := yaml.Unmarshal(body, &parsed); err != nil {
		return Candidate{}, fmt.Errorf("generated YAML failed to parse: %w", err)
	}
	if len(proxies) == 0 {
		return Candidate{}, errors.New("no nodes were produced; add an enabled Mihomo-compatible YAML subscription first")
	}
	validatorFingerprint := "not-configured"
	if b.MihomoBinary != "" {
		binary, readErr := os.ReadFile(b.MihomoBinary)
		if readErr != nil {
			validatorFingerprint = "unavailable:" + readErr.Error()
		} else {
			validatorHash := sha256.Sum256(binary)
			validatorFingerprint = hex.EncodeToString(validatorHash[:])
		}
	}
	fingerprintInputs := map[string]any{"subscriptions": fingerprintParts, "terminals": terminals, "proxyGroups": groups, "rules": rules, "validatorBinarySha256": validatorFingerprint}
	fingerprintBytes, _ := json.Marshal(fingerprintInputs)
	fingerprint := sha256.Sum256(fingerprintBytes)
	digest := sha256.Sum256(body)
	hash := hex.EncodeToString(digest[:])
	candidateIdentity := sha256.Sum256([]byte(hash + hex.EncodeToString(fingerprint[:])))
	id := hex.EncodeToString(candidateIdentity[:8])
	if err := os.MkdirAll(b.SnapshotDir, 0o750); err != nil {
		return Candidate{}, err
	}
	path := filepath.Join(b.SnapshotDir, id+".yaml")
	if err := writeImmutable(path, body); err != nil {
		return Candidate{}, err
	}
	now := time.Now().UTC()
	c := Candidate{ID: id, SHA256: hash, SnapshotPath: path, Status: "built", ValidationMessage: "YAML syntax and platform references checked; Mihomo binary validation has not run.", SourceFingerprint: hex.EncodeToString(fingerprint[:]), CreatedAt: now}
	if b.MihomoBinary != "" {
		output, validationErr := exec.CommandContext(ctx, b.MihomoBinary, "-t", "-f", path).CombinedOutput()
		if validationErr != nil {
			c.Status = "validation_failed"
			// The binary can fail before printing anything: a missing executable, a
			// permission problem, or an exec error surfaces only through validationErr.
			// Reporting an empty reason would tell the operator nothing actionable.
			reason := strings.TrimSpace(string(output))
			if reason == "" {
				reason = "no diagnostic output was produced by " + b.MihomoBinary + ": " + validationErr.Error()
			} else {
				reason += " (" + validationErr.Error() + ")"
			}
			c.ValidationMessage = "Mihomo rejected the candidate: " + reason
		} else {
			c.Status = "validated"
			c.ValidationMessage = "Mihomo binary validation succeeded; proxy references and rule targets were also checked."
		}
	}
	if len(terminalWarnings) > 0 {
		c.ValidationMessage += " Warnings: " + strings.Join(terminalWarnings, " ")
	}
	tx, err := b.DB.BeginTx(ctx, nil)
	if err != nil {
		return Candidate{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO config_candidates(id,sha256,snapshot_path,status,validation_message,source_fingerprint,created_at) VALUES(?,?,?,?,?,?,?)`, c.ID, c.SHA256, c.SnapshotPath, c.Status, c.ValidationMessage, c.SourceFingerprint, c.CreatedAt.Format(time.RFC3339Nano)); err != nil {
		return Candidate{}, err
	}
	for _, input := range sourceSnapshots {
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO config_candidate_inputs(candidate_id,subscription_id,content_sha256,snapshot_path) VALUES(?,?,?,?)`, c.ID, input.SubscriptionID, input.SHA256, input.Path); err != nil {
			return Candidate{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return Candidate{}, err
	}
	// Return the persisted lifecycle status if this exact immutable candidate already existed.
	var persistedCreated string
	if err := b.DB.QueryRowContext(ctx, `SELECT status,validation_message,created_at FROM config_candidates WHERE id=?`, c.ID).Scan(&c.Status, &c.ValidationMessage, &persistedCreated); err != nil {
		return Candidate{}, err
	}
	if parsedTime, err := time.Parse(time.RFC3339Nano, persistedCreated); err == nil {
		c.CreatedAt = parsedTime
	}
	return c, nil
}

// corsOrigins returns the browser origins allowed to call the controller.
//
// The controller compares the Origin header exactly, so a dashboard reached at a LAN address
// is a different origin than loopback. Hard-coding localhost therefore made a LAN-served
// dashboard fail with a CORS error even though the port was open.
func (b *Builder) corsOrigins() []string {
	if len(b.DashboardOrigins) > 0 {
		return b.DashboardOrigins
	}
	return []string{"http://127.0.0.1:8081", "http://localhost:8081"}
}

func writeImmutable(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	if existing, err := os.ReadFile(path); err == nil {
		if bytes.Equal(existing, body) {
			return nil
		}
		return errors.New("immutable snapshot path already exists with different content")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmpFile, err := os.CreateTemp(filepath.Dir(path), ".snapshot-*.tmp")
	if err != nil {
		return err
	}
	tmp := tmpFile.Name()
	defer os.Remove(tmp)
	if err = tmpFile.Chmod(0o640); err != nil {
		tmpFile.Close()
		return err
	}
	if _, err = tmpFile.Write(body); err != nil {
		tmpFile.Close()
		return err
	}
	if err = tmpFile.Sync(); err != nil {
		tmpFile.Close()
		return err
	}
	if err = tmpFile.Close(); err != nil {
		return err
	}
	if err = os.Link(tmp, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			existing, readErr := os.ReadFile(path)
			if readErr == nil && bytes.Equal(existing, body) {
				return nil
			}
			if readErr != nil {
				return readErr
			}
			return errors.New("immutable snapshot path already exists with different content")
		}
		return err
	}
	return nil
}

// RefreshSubscription fetches one source, validates its Mihomo YAML shape, and only then
// atomically promotes its immutable input snapshot as current. A failed refresh preserves
// the previous known-good snapshot.
func (b *Builder) RefreshSubscription(ctx context.Context, subscriptionID string) (map[string]any, error) {
	var name, rawURL string
	var enabled int
	if err := b.DB.QueryRowContext(ctx, `SELECT name,url,enabled FROM subscriptions WHERE id=?`, subscriptionID).Scan(&name, &rawURL, &enabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("subscription not found")
		}
		return nil, err
	}
	if enabled != 1 {
		return nil, errors.New("disabled subscription cannot be refreshed")
	}
	fetchedAt := time.Now().UTC().Format(time.RFC3339Nano)
	body, err := fetchSubscription(ctx, rawURL)
	if err != nil {
		_, _ = b.DB.ExecContext(ctx, `UPDATE subscriptions SET last_fetch_at=?,last_status='fetch_failed',updated_at=? WHERE id=?`, fetchedAt, fetchedAt, subscriptionID)
		return nil, fmt.Errorf("fetch subscription %q: %w", name, err)
	}
	proxies, err := parseSubscriptionProxies(body)
	if err != nil {
		_, _ = b.DB.ExecContext(ctx, `UPDATE subscriptions SET last_fetch_at=?,last_status='invalid_format',updated_at=? WHERE id=?`, fetchedAt, fetchedAt, subscriptionID)
		return nil, fmt.Errorf("subscription %q: %w", name, err)
	}
	digest := sha256.Sum256(body)
	hash := hex.EncodeToString(digest[:])
	path := filepath.Join(b.SnapshotDir, "inputs", subscriptionID, hash+".yaml")
	if err := writeImmutable(path, body); err != nil {
		return nil, fmt.Errorf("persist subscription snapshot: %w", err)
	}
	if err := b.recordCurrentSnapshot(ctx, subscriptionID, hash, path, fetchedAt, len(proxies)); err != nil {
		return nil, err
	}
	if _, err := b.DB.ExecContext(ctx, `UPDATE subscriptions SET last_fetch_at=?,last_status='fetched',updated_at=? WHERE id=?`, fetchedAt, fetchedAt, subscriptionID); err != nil {
		return nil, err
	}
	return map[string]any{"subscriptionId": subscriptionID, "subscription": name, "sha256": hash, "fetchedAt": fetchedAt, "proxyCount": len(proxies), "snapshotPath": path, "status": "current"}, nil
}

func (b *Builder) recordCurrentSnapshot(ctx context.Context, subscriptionID, hash, path, fetchedAt string, proxyCount int) error {
	tx, err := b.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE subscription_snapshots SET is_current=0 WHERE subscription_id=?`, subscriptionID); err != nil {
		return err
	}
	id := subscriptionID + "_" + hash[:min(16, len(hash))]
	if _, err = tx.ExecContext(ctx, `INSERT INTO subscription_snapshots(id,subscription_id,content_sha256,snapshot_path,fetched_at,is_current,proxy_count) VALUES(?,?,?,?,?,1,?) ON CONFLICT(subscription_id,content_sha256) DO UPDATE SET snapshot_path=excluded.snapshot_path,fetched_at=excluded.fetched_at,is_current=1,proxy_count=excluded.proxy_count`, id, subscriptionID, hash, path, fetchedAt, proxyCount); err != nil {
		return err
	}
	return tx.Commit()
}

func fetchSubscription(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mihomo-Studio/0.1")
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		for _, candidate := range ips {
			if isPublicAddress(candidate.IP) {
				return dialer.DialContext(ctx, network, net.JoinHostPort(candidate.IP.String(), port))
			}
		}
		return nil, errors.New("subscription host resolves only to private or reserved addresses")
	}}
	client := &http.Client{Timeout: 15 * time.Second, Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if req.URL.Scheme != "https" && req.URL.Scheme != "http" {
			return errors.New("subscription redirect must use HTTP or HTTPS")
		}
		return nil
	}}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	const max = 5 << 20
	limited := io.LimitReader(resp.Body, max+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(body) > max {
		return nil, errors.New("subscription exceeds 5 MiB limit")
	}
	return body, nil
}

// nonPublicNetworks are rejected as subscription hosts so that a stored URL cannot be
// used to make the control plane reach loopback, RFC 1918, link-local, or documentation
// addresses on the operator's network.
//
// 198.18.0.0/15 (RFC 2544 benchmarking) is deliberately NOT listed. Subscription
// providers in the wild publish that range as the A record for their delivery hosts,
// and Mihomo TUN/DNS-fake-IP setups also allocate from it. Treating it as private
// rejected legitimate subscriptions. This narrows the SSRF guard: a stored subscription
// URL can now reach a host in that range. Every other reserved range still applies.
var nonPublicNetworks = parseNetworks([]string{
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
	"192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
	"::/128", "::1/128", "::ffff:0:0/96", "64:ff9b:1::/48", "100::/64", "2001:10::/28", "2001:db8::/32", "2002::/16", "fc00::/7", "fe80::/10", "ff00::/8",
})

func parseNetworks(values []string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(values))
	for _, value := range values {
		_, network, err := net.ParseCIDR(value)
		if err == nil {
			out = append(out, network)
		}
	}
	return out
}

func isPublicAddress(ip net.IP) bool {
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	isIPv4 := ip.To4() != nil
	for _, network := range nonPublicNetworks {
		if (len(network.Mask) == net.IPv4len) == isIPv4 && network.Contains(ip) {
			return false
		}
	}
	return true
}

func loadGroups(ctx context.Context, db *sql.DB) ([]domain.ProxyGroup, error) {
	rows, err := db.QueryContext(ctx, `SELECT id,name,mihomo_type,selection_mode,member_node_names_json,member_filter_json,health_url,interval_seconds,tolerance_ms,created_at,updated_at,member_node_ids_json FROM proxy_groups ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.ProxyGroup{}
	for rows.Next() {
		var x domain.ProxyGroup
		var names, filter, c, u, nodeIDs string
		if err := rows.Scan(&x.ID, &x.Name, &x.MihomoType, &x.SelectionMode, &names, &filter, &x.HealthURL, &x.IntervalSeconds, &x.ToleranceMS, &c, &u, &nodeIDs); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(names), &x.MemberNodeNames); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(nodeIDs), &x.MemberNodeIDs); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(filter), &x.MemberFilter); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
func loadTerminals(ctx context.Context, db *sql.DB) ([]domain.Terminal, error) {
	rows, err := db.QueryContext(ctx, `SELECT id,name,identity_type,identity_value,proxy_group_id,enabled,notes,created_at,updated_at FROM terminals ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Terminal{}
	for rows.Next() {
		var x domain.Terminal
		var enabled int
		var c, u string
		if err := rows.Scan(&x.ID, &x.Name, &x.IdentityType, &x.IdentityValue, &x.ProxyGroupID, &enabled, &x.Notes, &c, &u); err != nil {
			return nil, err
		}
		x.Enabled = enabled == 1
		out = append(out, x)
	}
	return out, rows.Err()
}

func loadRules(ctx context.Context, db *sql.DB) ([]domain.Rule, error) {
	rows, err := db.QueryContext(ctx, `SELECT id,name,rule_type,rule_value,target,priority,source,enabled,created_at,updated_at FROM rules ORDER BY priority,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Rule{}
	for rows.Next() {
		var x domain.Rule
		var enabled int
		var c, u string
		if err := rows.Scan(&x.ID, &x.Name, &x.RuleType, &x.RuleValue, &x.Target, &x.Priority, &x.Source, &enabled, &c, &u); err != nil {
			return nil, err
		}
		x.Enabled = enabled == 1
		out = append(out, x)
	}
	return out, rows.Err()
}
func matchFilter(name string, filter map[string]any) bool {
	if filter == nil || len(filter) == 0 {
		return true
	}
	lowerName := strings.ToLower(name)
	if v, ok := filter["keyword"].(string); ok && v != "" && !strings.Contains(lowerName, strings.ToLower(v)) {
		return false
	}
	if v, ok := filter["subscription"].(string); ok && v != "" {
		source, _, found := strings.Cut(name, " / ")
		if !found || !strings.EqualFold(source, v) {
			return false
		}
	}
	if v, ok := filter["region"].(string); ok && v != "" && !strings.Contains(lowerName, strings.ToLower(v)) {
		return false
	}
	if v, ok := filter["regex"].(string); ok && v != "" {
		matched, err := regexp.MatchString(v, name)
		if err != nil || !matched {
			return false
		}
	}
	return true
}
