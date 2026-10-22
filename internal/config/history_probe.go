package config

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// SubscriptionSnapshotHistory returns immutable snapshots newest-first and a node-level
// diff against the immediately preceding successful snapshot. Possible renames are only
// heuristics and are labelled explicitly; they never rewrite node identities automatically.
func (b *Builder) SubscriptionSnapshotHistory(ctx context.Context, subscriptionID string) ([]map[string]any, error) {
	var exists int
	if err := b.DB.QueryRowContext(ctx, `SELECT count(*) FROM subscriptions WHERE id=?`, subscriptionID).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, errors.New("subscription not found")
	}
	rows, err := b.DB.QueryContext(ctx, `SELECT content_sha256,snapshot_path,fetched_at,is_current,proxy_count FROM subscription_snapshots WHERE subscription_id=? ORDER BY fetched_at DESC,content_sha256 DESC`, subscriptionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type snap struct {
		Hash, Path, FetchedAt string
		Current               int
		Count                 int
	}
	var all []snap
	for rows.Next() {
		var x snap
		if err := rows.Scan(&x.Hash, &x.Path, &x.FetchedAt, &x.Current, &x.Count); err != nil {
			return nil, err
		}
		all = append(all, x)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(all))
	for i, x := range all {
		body, err := osReadFile(x.Path)
		if err != nil {
			return nil, fmt.Errorf("read snapshot %s: %w", x.Hash, err)
		}
		current, err := snapshotNodeTypes(body)
		if err != nil {
			return nil, fmt.Errorf("parse snapshot %s: %w", x.Hash, err)
		}
		item := map[string]any{"sha256": x.Hash, "fetchedAt": x.FetchedAt, "current": x.Current == 1, "proxyCount": x.Count}
		// Diff each snapshot against the next older snapshot (history is newest first).
		if i+1 < len(all) {
			olderBody, readErr := osReadFile(all[i+1].Path)
			if readErr != nil {
				return nil, readErr
			}
			older, parseErr := snapshotNodeTypes(olderBody)
			if parseErr != nil {
				return nil, parseErr
			}
			added, removed, unchanged := diffNames(current, older)
			item["diff"] = map[string]any{"added": added, "removed": removed, "unchanged": unchanged, "possibleRenames": possibleRenames(added, removed, current, older)}
		} else {
			added, removed, unchanged := diffNames(current, map[string]string{})
			item["diff"] = map[string]any{"baseline": true, "added": added, "removed": removed, "unchanged": unchanged, "possibleRenames": []map[string]string{}}
		}
		out = append(out, item)
	}
	return out, nil
}

// snapshotNodeTypes indexes a stored raw snapshot by node name. Snapshots keep whatever
// the provider sent, which may be Mihomo YAML or a Base64/plain URI list, so both are
// accepted here rather than assuming YAML.
func snapshotNodeTypes(body []byte) (map[string]string, error) {
	proxies, err := parseSubscriptionProxies(body)
	if err != nil {
		return nil, err
	}
	types := make(map[string]string, len(proxies))
	for _, entry := range proxies {
		name, _ := entry["name"].(string)
		proxyType, _ := entry["type"].(string)
		if strings.TrimSpace(name) != "" {
			types[name] = proxyType
		}
	}
	return types, nil
}

func diffNames(current, older map[string]string) (added, removed []string, unchanged int) {
	for name := range current {
		if _, ok := older[name]; ok {
			unchanged++
		} else {
			added = append(added, name)
		}
	}
	for name := range older {
		if _, ok := current[name]; !ok {
			removed = append(removed, name)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return
}
func possibleRenames(added, removed []string, current, older map[string]string) []map[string]string {
	// Only suggest a rename when exactly one removed and one added node share a type.
	// The source format has no stable identity, so the suggestion remains low-confidence.
	addedByType := map[string][]string{}
	removedByType := map[string][]string{}
	for _, name := range added {
		if current[name] != "" {
			addedByType[current[name]] = append(addedByType[current[name]], name)
		}
	}
	for _, name := range removed {
		if older[name] != "" {
			removedByType[older[name]] = append(removedByType[older[name]], name)
		}
	}
	var out []map[string]string
	for typ, oldNames := range removedByType {
		newNames := addedByType[typ]
		if len(oldNames) == 1 && len(newNames) == 1 {
			out = append(out, map[string]string{"from": oldNames[0], "to": newNames[0], "proxyType": typ, "confidence": "low"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["from"] < out[j]["from"] })
	return out
}

// runtimeProxyName is the proxy name as it appears in the generated Mihomo config.
//
// Candidate composition rewrites every proxy name to "<subscription> / <source name>"
// before it is written into the config, so the running runtime keys proxies by that
// rendered name — not by the bare source name from the subscription snapshot. Probing
// with the source name therefore returns 404 "Resource not found", which is a missing
// target rather than an unreachable node, and must not be reported as not_loaded.
func runtimeProxyName(n Node) string { return n.Name }

// probeEndpoint addresses the proxy name as it exists in the live Mihomo instance.
func probeEndpoint(controllerURL, proxyName string) string {
	return strings.TrimRight(controllerURL, "/") + "/proxies/" + url.PathEscape(proxyName) + "/delay?url=" + url.QueryEscape("https://www.gstatic.com/generate_204") + "&timeout=2000"
}

// ProbeNodes checks nodes currently loaded by the running Mihomo instance via its
// controller API. A refreshed-but-not-published node is reported as not_loaded rather
// than being misrepresented as a failed network probe.
func (b *Builder) ProbeNodes(ctx context.Context) (map[string]any, error) {
	if strings.TrimSpace(b.ControllerURL) == "" {
		return nil, errors.New("Mihomo controller URL is not configured; set MIHOMO_CONTROLLER_URL")
	}
	nodes, err := b.Nodes(ctx)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 2500 * time.Millisecond}
	type result struct {
		Node      Node   `json:"node"`
		Status    string `json:"status"`
		LatencyMS *int   `json:"latencyMs"`
		Message   string `json:"message,omitempty"`
		CheckedAt string `json:"checkedAt"`
	}
	results := make([]result, len(nodes))
	sem := make(chan struct{}, 24)
	var wg sync.WaitGroup
	for i, node := range nodes {
		i, node := i, node
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			r := result{Node: node, Status: "error", CheckedAt: time.Now().UTC().Format(time.RFC3339Nano)}
			endpoint := probeEndpoint(b.ControllerURL, runtimeProxyName(node))
			req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
			if reqErr != nil {
				r.Message = reqErr.Error()
				results[i] = r
				return
			}
			if b.ControllerSecret != "" {
				req.Header.Set("Authorization", "Bearer "+b.ControllerSecret)
			}
			resp, doErr := client.Do(req)
			if doErr != nil {
				r.Status = "error"
				r.Message = doErr.Error()
				results[i] = r
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusBadRequest {
				r.Status = "not_loaded"
				r.Message = "node is not present in the running Mihomo configuration"
				results[i] = r
				return
			}
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				r.Status = "unavailable"
				r.Message = fmt.Sprintf("Mihomo delay probe returned HTTP %d", resp.StatusCode)
				results[i] = r
				return
			}
			var payload struct {
				Delay int `json:"delay"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
				r.Status = "error"
				r.Message = "invalid delay response: " + err.Error()
				results[i] = r
				return
			}
			if payload.Delay <= 0 {
				r.Status = "unavailable"
				r.Message = "Mihomo returned a non-positive delay"
			} else {
				r.Status = "available"
				r.LatencyMS = &payload.Delay
			}
			results[i] = r
		}()
	}
	wg.Wait()
	checkedAt := time.Now().UTC().Format(time.RFC3339Nano)
	available, unavailable, notLoaded, failed := 0, 0, 0, 0
	tx, err := b.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for i := range results {
		results[i].Node.Status = results[i].Status
		results[i].Node.LatencyMS = results[i].LatencyMS
		results[i].Node.LastCheckedAt = results[i].CheckedAt
		results[i].Node.ProbeMessage = results[i].Message
		results[i].Node.ProbeStale = false
	}
	for _, r := range results {
		if _, err := tx.ExecContext(ctx, `INSERT INTO node_probe_results(node_id,subscription_id,node_name,status,latency_ms,checked_at,error_message) VALUES(?,?,?,?,?,?,?) ON CONFLICT(node_id) DO UPDATE SET subscription_id=excluded.subscription_id,node_name=excluded.node_name,status=excluded.status,latency_ms=excluded.latency_ms,checked_at=excluded.checked_at,error_message=excluded.error_message`, r.Node.ID, r.Node.SubscriptionID, r.Node.SourceName, r.Status, r.LatencyMS, r.CheckedAt, r.Message); err != nil {
			return nil, err
		}
		switch r.Status {
		case "available":
			available++
		case "unavailable":
			unavailable++
		case "not_loaded":
			notLoaded++
		default:
			failed++
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{"checkedAt": checkedAt, "total": len(results), "available": available, "unavailable": unavailable, "notLoaded": notLoaded, "errors": failed, "results": results}, nil
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

var osReadFile = func(path string) ([]byte, error) { return os.ReadFile(path) }

// VerifyRuntimeProxyGroups compares configured proxy groups with Mihomo's live controller
// view. It observes the actual selected proxy and member health without forcing failures.
func (b *Builder) VerifyRuntimeProxyGroups(ctx context.Context) (map[string]any, error) {
	if strings.TrimSpace(b.ControllerURL) == "" {
		return nil, errors.New("Mihomo controller URL is not configured; set MIHOMO_CONTROLLER_URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(b.ControllerURL, "/")+"/proxies", nil)
	if err != nil {
		return nil, err
	}
	if b.ControllerSecret != "" {
		req.Header.Set("Authorization", "Bearer "+b.ControllerSecret)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("query Mihomo runtime proxy groups: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Mihomo /proxies returned HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Proxies map[string]struct {
			Type  string   `json:"type"`
			Now   string   `json:"now"`
			All   []string `json:"all"`
			Alive bool     `json:"alive"`
		} `json:"proxies"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode Mihomo runtime proxy groups: %w", err)
	}
	groups, err := loadGroups(ctx, b.DB)
	if err != nil {
		return nil, err
	}
	deployedGroups := map[string]map[string]any{}
	var deployedPath string
	pathErr := b.DB.QueryRowContext(ctx, `SELECT c.snapshot_path FROM deployments d JOIN config_candidates c ON c.id=d.candidate_id WHERE d.status='verified' ORDER BY d.finished_at DESC LIMIT 1`).Scan(&deployedPath)
	if pathErr != nil && !errors.Is(pathErr, sql.ErrNoRows) {
		return nil, pathErr
	}
	if pathErr == nil {
		body, readErr := os.ReadFile(deployedPath)
		if readErr != nil {
			return nil, fmt.Errorf("read last verified candidate: %w", readErr)
		}
		var cfg struct {
			ProxyGroups []map[string]any `yaml:"proxy-groups"`
		}
		if err := yaml.Unmarshal(body, &cfg); err != nil {
			return nil, fmt.Errorf("parse last verified candidate: %w", err)
		}
		for _, entry := range cfg.ProxyGroups {
			name, _ := entry["name"].(string)
			if name != "" {
				deployedGroups[name] = entry
			}
		}
	}
	items := make([]map[string]any, 0, len(groups))
	okCount := 0
	for _, g := range groups {
		live, exists := payload.Proxies[g.Name]
		if !exists {
			items = append(items, map[string]any{"id": g.ID, "name": g.Name, "expectedType": g.MihomoType, "status": "missing", "message": "group is not present in the running Mihomo configuration"})
			continue
		}
		expected := map[string]string{"select": "Selector", "url-test": "URLTest", "fallback": "Fallback", "load-balance": "LoadBalance"}[g.MihomoType]
		typeOK := strings.EqualFold(live.Type, expected)
		status := "verified"
		message := "runtime group type matches the saved definition"
		if !typeOK {
			status = "type_mismatch"
			message = fmt.Sprintf("saved type %s does not match runtime type %s", expected, live.Type)
		}
		var expectedMembers []string
		if entry, ok := deployedGroups[g.Name]; ok && typeOK {
			if raw, ok := entry["proxies"].([]any); ok {
				for _, v := range raw {
					if name, ok := v.(string); ok {
						expectedMembers = append(expectedMembers, name)
					}
				}
			}
			if !equalStrings(expectedMembers, live.All) {
				status = "members_mismatch"
				message = "runtime member list/order differs from the last verified candidate"
			} else if typeOK {
				message = "runtime type and ordered members match the last verified candidate"
			}
		}
		if status == "verified" {
			okCount++
		}
		item := map[string]any{"id": g.ID, "name": g.Name, "expectedType": g.MihomoType, "runtimeType": live.Type, "status": status, "message": message, "current": live.Now, "members": live.All, "alive": live.Alive}
		if len(expectedMembers) > 0 {
			item["expectedMembers"] = expectedMembers
		}
		if g.MihomoType == "fallback" {
			item["semantics"] = "Mihomo fallback uses member order and moves to the next usable member when the current member is unavailable; this check observes current state but does not inject an outage."
		}
		if g.MihomoType == "url-test" {
			item["semantics"] = "Mihomo periodically probes members and selects according to measured delay and tolerance; this check observes current state but does not wait for a complete probe cycle."
		}
		items = append(items, item)
	}
	return map[string]any{"checkedAt": time.Now().UTC().Format(time.RFC3339Nano), "configuredGroups": len(groups), "runtimeGroups": len(payload.Proxies), "typeMatched": okCount, "groups": items, "limitations": "This is a read-only runtime check. It does not deliberately break a node or prove failover under a controlled outage."}, nil
}
