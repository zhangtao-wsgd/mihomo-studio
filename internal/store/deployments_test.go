package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// seedDeployment inserts a candidate and a deployment so history queries have data.
func seedDeployment(t *testing.T, s *Store, id, candidateID, status, startedAt string) {
	t.Helper()
	if _, err := s.DB.Exec(`INSERT OR IGNORE INTO config_candidates(id,sha256,snapshot_path,status,validation_message,source_fingerprint,created_at) VALUES(?,?,?,?,?,?,?)`,
		candidateID, "sha-"+candidateID, "/snapshots/"+candidateID+".yaml", "validated", "", "fp", startedAt); err != nil {
		t.Fatalf("seed candidate %s: %v", candidateID, err)
	}
	if _, err := s.DB.Exec(`INSERT INTO deployments(id,operation,candidate_id,status,runtime_message,started_at) VALUES(?,?,?,?,?,?)`,
		id, "publish", candidateID, status, "message "+id, startedAt); err != nil {
		t.Fatalf("seed deployment %s: %v", id, err)
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "deployments.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DB.Close() })
	return s
}

// History must be an empty array, never null: the browser client iterates it directly and
// a null would throw before any deployment exists.
func TestListDeploymentsEmptyReturnsArrayNotNull(t *testing.T) {
	s := openTestStore(t)
	items, total, err := s.ListDeployments(context.Background(), DeploymentFilter{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if items == nil {
		t.Fatal("ListDeployments returned nil; JSON would encode as null")
	}
	if len(items) != 0 || total != 0 {
		t.Fatalf("expected empty history, got %d items, total %d", len(items), total)
	}
}

func TestListDeploymentsOrdersNewestFirst(t *testing.T) {
	s := openTestStore(t)
	seedDeployment(t, s, "d-old", "c1", "verified", "2026-01-01T00:00:00Z")
	seedDeployment(t, s, "d-mid", "c2", "failed", "2026-02-01T00:00:00Z")
	seedDeployment(t, s, "d-new", "c3", "verified", "2026-03-01T00:00:00Z")

	items, total, err := s.ListDeployments(context.Background(), DeploymentFilter{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 {
		t.Fatalf("total = %d, want 3", total)
	}
	want := []string{"d-new", "d-mid", "d-old"}
	for i, id := range want {
		if items[i].ID != id {
			t.Fatalf("position %d = %s, want %s", i, items[i].ID, id)
		}
	}
	// The joined candidate hash must be present so the UI can show a version identifier.
	if items[0].CandidateSHA256 != "sha-c3" {
		t.Fatalf("candidateSha256 = %q", items[0].CandidateSHA256)
	}
}

func TestListDeploymentsPaginatesWithoutOverlap(t *testing.T) {
	s := openTestStore(t)
	for i := 0; i < 5; i++ {
		seedDeployment(t, s, "d"+string(rune('0'+i)), "c"+string(rune('0'+i)), "verified",
			"2026-01-0"+string(rune('1'+i))+"T00:00:00Z")
	}
	first, total, err := s.ListDeployments(context.Background(), DeploymentFilter{Limit: 2, Offset: 0})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := s.ListDeployments(context.Background(), DeploymentFilter{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatal(err)
	}
	if total != 5 {
		t.Fatalf("total = %d, want 5", total)
	}
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("page sizes = %d, %d", len(first), len(second))
	}
	seen := map[string]bool{}
	for _, record := range append(first, second...) {
		if seen[record.ID] {
			t.Fatalf("deployment %s appeared on both pages", record.ID)
		}
		seen[record.ID] = true
	}
}

func TestListDeploymentsFilters(t *testing.T) {
	s := openTestStore(t)
	seedDeployment(t, s, "d-ok", "c1", "verified", "2026-01-01T00:00:00Z")
	seedDeployment(t, s, "d-bad", "c2", "failed", "2026-01-02T00:00:00Z")

	verified, total, err := s.ListDeployments(context.Background(), DeploymentFilter{Limit: 20, Status: "verified"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(verified) != 1 || verified[0].ID != "d-ok" {
		t.Fatalf("status filter returned %#v (total %d)", verified, total)
	}

	byCandidate, _, err := s.ListDeployments(context.Background(), DeploymentFilter{Limit: 20, CandidateID: "c2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byCandidate) != 1 || byCandidate[0].ID != "d-bad" {
		t.Fatalf("candidate filter returned %#v", byCandidate)
	}
}

// An invalid filter must fail loudly: silently returning an empty page would look like
// "no deployments" when the query itself was wrong.
func TestListDeploymentsRejectsInvalidFilters(t *testing.T) {
	s := openTestStore(t)
	cases := []DeploymentFilter{
		{Limit: 0},
		{Limit: -1},
		{Limit: maxDeploymentLimit + 1},
		{Limit: 20, Offset: -1},
		{Limit: 20, Status: "not_a_status"},
		{Limit: 20, Operation: "delete"},
	}
	for _, filter := range cases {
		if _, _, err := s.ListDeployments(context.Background(), filter); !errors.Is(err, ErrInvalidDeploymentFilter) {
			t.Fatalf("filter %#v: expected ErrInvalidDeploymentFilter, got %v", filter, err)
		}
	}
}

func TestDeploymentNotFound(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.Deployment(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestDeploymentEventsAreScopedAndOrdered(t *testing.T) {
	s := openTestStore(t)
	seedDeployment(t, s, "d-1", "c1", "verified", "2026-01-01T00:00:00Z")
	for _, event := range []struct{ id, deployment, created, outcome string }{
		{"e1", "d-1", "2026-01-01T00:00:01Z", "publishing"},
		{"e2", "d-1", "2026-01-01T00:00:02Z", "verified"},
		{"e3", "d-other", "2026-01-01T00:00:03Z", "failed"},
	} {
		if _, err := s.DB.Exec(`INSERT INTO audit_events(id,action,resource_type,resource_id,outcome,detail_json,created_at) VALUES(?,?,?,?,?,?,?)`,
			event.id, "deployment.publish."+event.outcome, "deployment", event.deployment, event.outcome, "{}", event.created); err != nil {
			t.Fatal(err)
		}
	}
	events, err := s.DeploymentEvents(context.Background(), "d-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events for d-1, got %d", len(events))
	}
	// Oldest first: the operator reads how the outcome was reached.
	if events[0].Outcome != "publishing" || events[1].Outcome != "verified" {
		t.Fatalf("unexpected order: %#v", events)
	}
}

func TestAuditEventsValidation(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.AuditEvents(context.Background(), 0, 0); !errors.Is(err, ErrInvalidDeploymentFilter) {
		t.Fatalf("limit 0 should be rejected, got %v", err)
	}
	if _, err := s.AuditEvents(context.Background(), 20, -1); !errors.Is(err, ErrInvalidDeploymentFilter) {
		t.Fatalf("negative offset should be rejected, got %v", err)
	}
	// Empty result must still be an array, not nil.
	events, err := s.AuditEvents(context.Background(), 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if events == nil {
		t.Fatal("AuditEvents returned nil; JSON would encode as null")
	}
}
