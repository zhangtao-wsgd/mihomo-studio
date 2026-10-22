package config

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

func stableNodeID(subscriptionID, sourceName string) string {
	identity := sha256.Sum256([]byte(subscriptionID + "\x00" + sourceName))
	return hex.EncodeToString(identity[:12])
}

// Node is a proxy entry from the most recently persisted input snapshot for a subscription.
// Latency and availability are populated only from persisted real Mihomo controller probes.
type Node struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	SourceName     string `json:"sourceName"`
	SubscriptionID string `json:"subscriptionId"`
	Subscription   string `json:"subscription"`
	Type           string `json:"type"`
	Region         string `json:"region,omitempty"`
	RegionInferred bool   `json:"regionInferred"`
	LatencyMS      *int   `json:"latencyMs"`
	Status         string `json:"status"`
	LastCheckedAt  string `json:"lastCheckedAt,omitempty"`
	ProbeMessage   string `json:"probeMessage,omitempty"`
	ProbeStale     bool   `json:"probeStale"`
}

func (b *Builder) Nodes(ctx context.Context) ([]Node, error) {
	// The store serializes access on a single SQLite connection, so this outer query must
	// be fully drained and closed before any per-node probe lookup runs. Interleaving a
	// second query while `rows` is still open deadlocks against that one connection.
	type currentSnapshot struct{ subscriptionID, subscriptionName, path string }
	var snapshots []currentSnapshot
	rows, err := b.DB.QueryContext(ctx, `SELECT s.id,s.name,ss.snapshot_path FROM subscriptions s JOIN subscription_snapshots ss ON ss.subscription_id=s.id AND ss.is_current=1 WHERE s.enabled=1 ORDER BY s.name,s.id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var snapshot currentSnapshot
		if err := rows.Scan(&snapshot.subscriptionID, &snapshot.subscriptionName, &snapshot.path); err != nil {
			rows.Close()
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	// Return an empty slice, not nil: a nil slice serializes to JSON null, and the
	// browser client calls .map() on this response before any subscription exists.
	nodes := []Node{}
	for _, snapshot := range snapshots {
		subID, subName, snapshotPath := snapshot.subscriptionID, snapshot.subscriptionName, snapshot.path
		// The database points to the last successfully validated snapshot; never select
		// a snapshot by filesystem mtime because a failed fetch may leave newer files behind.
		body, err := os.ReadFile(snapshotPath)
		if err != nil {
			return nil, fmt.Errorf("read current snapshot for subscription %q: %w", subName, err)
		}
		var doc struct {
			Proxies []struct {
				Name string `yaml:"name"`
				Type string `yaml:"type"`
			} `yaml:"proxies"`
		}
		if err := yaml.Unmarshal(body, &doc); err != nil {
			return nil, fmt.Errorf("parse current snapshot for subscription %q: %w", subName, err)
		}
		for _, p := range doc.Proxies {
			if strings.TrimSpace(p.Name) == "" {
				continue
			}
			region := inferRegion(p.Name)
			node := Node{ID: stableNodeID(subID, p.Name), Name: subName + " / " + p.Name, SourceName: p.Name, SubscriptionID: subID, Subscription: subName, Type: p.Type, Region: region, RegionInferred: region != "", LatencyMS: nil, Status: "unmeasured"}
			var checked, message string
			var latency sql.NullInt64
			probeErr := b.DB.QueryRowContext(ctx, `SELECT latency_ms,status,checked_at,error_message FROM node_probe_results WHERE node_id=?`, node.ID).Scan(&latency, &node.Status, &checked, &message)
			if probeErr == nil {
				node.LastCheckedAt = checked
				node.ProbeMessage = message
				if checkedAt, parseErr := time.Parse(time.RFC3339Nano, checked); parseErr == nil {
					node.ProbeStale = time.Since(checkedAt) > 5*time.Minute
				} else {
					node.ProbeStale = true
				}
				if latency.Valid {
					v := int(latency.Int64)
					node.LatencyMS = &v
				}
			} else if !errors.Is(probeErr, sql.ErrNoRows) {
				return nil, probeErr
			}
			nodes = append(nodes, node)
		}
	}
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Subscription != nodes[j].Subscription {
			return nodes[i].Subscription < nodes[j].Subscription
		}
		return nodes[i].Name < nodes[j].Name
	})
	return nodes, nil
}

func inferRegion(name string) string {
	n := strings.ToLower(name)
	for _, item := range []struct {
		region string
		keys   []string
	}{
		{"香港", []string{"香港", "hong kong"}},
		{"日本", []string{"日本", "东京", "大阪", "japan", "tokyo", "osaka"}},
		{"新加坡", []string{"新加坡", "singapore"}},
		{"美国", []string{"美国", "洛杉矶", "西雅图", "纽约", "united states", "los angeles"}},
		{"台湾", []string{"台湾", "taiwan"}},
		{"韩国", []string{"韩国", "首尔", "korea", "seoul"}},
		{"英国", []string{"英国", "伦敦", "united kingdom", "london"}},
		{"德国", []string{"德国", "法兰克福", "germany", "frankfurt"}},
	} {
		for _, key := range item.keys {
			if strings.Contains(n, key) {
				return item.region
			}
		}
	}
	return ""
}
