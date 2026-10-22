package config

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mihomo-studio/internal/store"
)

func TestInferRegion(t *testing.T) {
	cases := map[string]string{
		"香港 IEPL 01":      "香港",
		"Tokyo premium":   "日本",
		"美国洛杉矶 02":        "美国",
		"Frankfurt DE":    "德国",
		"random-node-xyz": "",
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			if got := inferRegion(name); got != want {
				t.Fatalf("inferRegion(%q) = %q, want %q", name, got, want)
			}
		})
	}
}

func TestStableNodeID(t *testing.T) {
	first := stableNodeID("sub-a", "Los Angeles 01")
	if first == "" || first != stableNodeID("sub-a", "Los Angeles 01") {
		t.Fatal("node ID must be deterministic")
	}
	if first == stableNodeID("sub-b", "Los Angeles 01") {
		t.Fatal("same source node name from different subscriptions must have distinct IDs")
	}
	if first == stableNodeID("sub-a", "Los Angeles 02") {
		t.Fatal("different source node names must have distinct IDs")
	}
}

// The browser client calls .map() on the /api/v1/nodes response before any
// subscription exists. A nil slice would serialize to JSON null and throw.
func TestNodesReturnsEmptySliceNotNullForEmptyInventory(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/nodes-empty.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	nodes, err := (&Builder{DB: s.DB}).Nodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if nodes == nil {
		t.Fatal("Nodes() returned nil; JSON would encode as null instead of []")
	}
	encoded, err := json.Marshal(nodes)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != "[]" {
		t.Fatalf("empty inventory encoded as %s, want []", encoded)
	}
}

// The store uses a single SQLite connection. An earlier version of Nodes interleaved a
// per-node probe lookup while the outer snapshot query was still open, which blocked
// forever on that one connection and froze every other API request behind it.
func TestNodesDoesNotDeadlockOnSingleConnection(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "nodes-deadlock.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()

	snapshotPath := filepath.Join(dir, "snapshot.yaml")
	snapshot := "proxies:\n  - name: 香港 IEPL 01\n    type: ss\n  - name: 日本东京 01\n    type: ss\n"
	if err := os.WriteFile(snapshotPath, []byte(snapshot), 0o600); err != nil {
		t.Fatal(err)
	}
	const now = "2026-01-01T00:00:00Z"
	if _, err := s.DB.Exec(`INSERT INTO subscriptions(id,name,url,enabled,last_status,created_at,updated_at) VALUES('sub-a','主订阅','https://example.invalid/a.yaml',1,'fetched',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO subscription_snapshots(id,subscription_id,content_sha256,snapshot_path,fetched_at,is_current,proxy_count) VALUES('snap-a','sub-a','hash-a',?,?,1,2)`, snapshotPath, now); err != nil {
		t.Fatal(err)
	}
	// A persisted probe result forces the per-node lookup that previously deadlocked.
	nodeID := stableNodeID("sub-a", "香港 IEPL 01")
	if _, err := s.DB.Exec(`INSERT INTO node_probe_results(node_id,subscription_id,node_name,status,latency_ms,checked_at,error_message) VALUES(?,'sub-a','香港 IEPL 01','available',42,?,'')`, nodeID, now); err != nil {
		t.Fatal(err)
	}

	type outcome struct {
		nodes []Node
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		nodes, err := (&Builder{DB: s.DB}).Nodes(context.Background())
		done <- outcome{nodes, err}
	}()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("Nodes() failed: %v", got.err)
		}
		if len(got.nodes) != 2 {
			t.Fatalf("expected 2 nodes, got %d", len(got.nodes))
		}
		// The persisted probe must be surfaced, proving the per-node lookup actually ran.
		// Results are sorted by subscription then rendered name, so locate by node ID.
		var probed *Node
		for i := range got.nodes {
			if got.nodes[i].ID == nodeID {
				probed = &got.nodes[i]
			}
		}
		if probed == nil {
			t.Fatal("node with a persisted probe result is missing from the inventory")
		}
		if probed.Status != "available" || probed.LatencyMS == nil || *probed.LatencyMS != 42 {
			t.Fatalf("persisted probe result was not applied: %+v", *probed)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Nodes() deadlocked: single-connection store was blocked by an open row iterator")
	}
}
