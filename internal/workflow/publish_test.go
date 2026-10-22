package workflow

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mihomo-studio/internal/store"
)

// errNoRowsForTest keeps the "row is absent" assertion readable at the call site.
func errNoRowsForTest() error { return sql.ErrNoRows }

// hashOf returns the hex SHA-256 of a string, matching what publish verification compares.
func hashOf(t *testing.T, body string) string {
	t.Helper()
	digest := sha256.Sum256([]byte(body))
	return hex.EncodeToString(digest[:])
}

// fakeRuntime lets the publish state machine be exercised without a live Mihomo.
// Each field selects which outcome the next Reload/Ready call reports.
type fakeRuntime struct {
	reloadErr error
	readyErr  error
	// reloadErrs overrides reloadErr per call index, so a test can model "rejected, then
	// the restore reload succeeds".
	reloadErrs []error
	reloads    int
}

func (f *fakeRuntime) Reload(context.Context, string) error {
	index := f.reloads
	f.reloads++
	if index < len(f.reloadErrs) {
		return f.reloadErrs[index]
	}
	return f.reloadErr
}

func (f *fakeRuntime) Ready(context.Context) error { return f.readyErr }

// newHarness builds a Service over a real migrated database and a real candidate file.
// The candidate row and its snapshot are inserted directly so the state machine can be
// tested without the network-dependent builder.
func newHarness(t *testing.T, runtime Runtime) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "workflow.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DB.Close() })

	candidatePath := filepath.Join(dir, "candidate.yaml")
	body := "# candidate\nmode: rule\nrules:\n  - MATCH,DIRECT\n"
	if err := writeAtomic(candidatePath, []byte(body)); err != nil {
		t.Fatal(err)
	}
	return &Service{
		DB:         s.DB,
		Runtime:    runtime,
		ActivePath: filepath.Join(dir, "active", "config.yaml"),
	}, candidatePath
}

// insertCandidate records an immutable candidate row and returns its ID.
func insertCandidate(t *testing.T, s *Service, path, sha string) string {
	t.Helper()
	actual, err := hashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if sha == "" {
		sha = actual
	}
	if _, err := s.DB.Exec(`INSERT INTO config_candidates(id,sha256,snapshot_path,status,validation_message,source_fingerprint,created_at) VALUES(?,?,?,?,?,?,?)`,
		"cand-"+sha[:8], sha, path, "validated", "", "fp", time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	return "cand-" + sha[:8]
}

func TestPublishRequiresConfirmation(t *testing.T) {
	service, path := newHarness(t, &fakeRuntime{})
	id := insertCandidate(t, service, path, "")
	if _, err := service.Publish(context.Background(), id, false); err == nil {
		t.Fatal("publish without confirmation must fail")
	}
	if _, err := service.DB.Exec(`SELECT count(*) FROM deployments`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := service.DB.QueryRow(`SELECT count(*) FROM deployments`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("a refused publish must not create a deployment row, found %d", count)
	}
}

// With no runtime configured the deployment must be recorded as unverified, never as
// verified: claiming success without a controller would be a false statement about live state.
func TestPublishWithoutRuntimeIsUnverifiedNotVerified(t *testing.T) {
	service, path := newHarness(t, nil)
	id := insertCandidate(t, service, path, "")
	deployment, err := service.Publish(context.Background(), id, true)
	if err != nil {
		t.Fatalf("publish should not error when only the runtime is missing: %v", err)
	}
	if deployment.Status != "published_unverified" {
		t.Fatalf("status = %q, want published_unverified", deployment.Status)
	}
	// Critical: active_candidate_id must stay unset so nothing can be rolled back from a
	// deployment whose live effect was never confirmed.
	var value string
	err = service.DB.QueryRow(`SELECT value FROM app_state WHERE key='active_candidate_id'`).Scan(&value)
	if !errors.Is(err, errNoRowsForTest()) {
		t.Fatalf("active_candidate_id should be unset after an unverified publish, got value=%q err=%v", value, err)
	}
}

func TestPublishVerifiedRecordsActiveCandidate(t *testing.T) {
	runtime := &fakeRuntime{}
	service, path := newHarness(t, runtime)
	id := insertCandidate(t, service, path, "")
	deployment, err := service.Publish(context.Background(), id, true)
	if err != nil {
		t.Fatal(err)
	}
	if deployment.Status != "verified" {
		t.Fatalf("status = %q, want verified (%s)", deployment.Status, deployment.RuntimeMessage)
	}
	var active string
	if err := service.DB.QueryRow(`SELECT value FROM app_state WHERE key='active_candidate_id'`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != id {
		t.Fatalf("active_candidate_id = %q, want %q", active, id)
	}
	// An audit event must be recorded for the outcome.
	var events int
	if err := service.DB.QueryRow(`SELECT count(*) FROM audit_events WHERE resource_id=?`, deployment.ID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("expected 1 audit event, got %d", events)
	}
}

// A rejected reload must be recorded as failed and must leave the previous active file in
// place, because silently keeping the rejected config on disk would be worse on restart.
// The runtime is modelled to reject the candidate and then accept the restored config.
func TestPublishRejectedReloadFailsAndRestoresPreviousFile(t *testing.T) {
	runtime := &fakeRuntime{reloadErrs: []error{
		errors.New("Mihomo rejected configuration with HTTP 400"),
		nil,
	}}
	service, path := newHarness(t, runtime)

	// Seed a previous active file so restoration has something to restore.
	previous := "# previous\nmode: rule\nrules:\n  - MATCH,DIRECT\n"
	if err := os.MkdirAll(filepath.Dir(service.ActivePath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(service.ActivePath, []byte(previous)); err != nil {
		t.Fatal(err)
	}
	id := insertCandidate(t, service, path, "")
	deployment, err := service.Publish(context.Background(), id, true)
	if err == nil {
		t.Fatal("a rejected reload must surface an error")
	}
	if deployment.Status != "failed" {
		t.Fatalf("status = %q, want failed", deployment.Status)
	}
	restored, readErr := hashFile(service.ActivePath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	previousHash := hashOf(t, previous)
	if restored != previousHash {
		t.Fatal("the rejected config must not remain as the active file")
	}
}

// A transport-level failure is ambiguous: the reload may have succeeded. It must be
// recorded as unknown, never as failed or verified.
func TestPublishUncertainReloadIsRecordedAsUnknown(t *testing.T) {
	runtime := &fakeRuntime{reloadErr: errors.New("reload result is uncertain: context deadline exceeded")}
	service, path := newHarness(t, runtime)
	id := insertCandidate(t, service, path, "")
	deployment, _ := service.Publish(context.Background(), id, true)
	if deployment.Status != "unknown" {
		t.Fatalf("status = %q, want unknown", deployment.Status)
	}
}

// Rollback is only permitted from a verified deployment, and only while it is still the
// active candidate. Both guards must hold.
func TestRollbackRequiresVerifiedAndActive(t *testing.T) {
	service, path := newHarness(t, nil)
	id := insertCandidate(t, service, path, "")
	deployment, err := service.Publish(context.Background(), id, true)
	if err != nil {
		t.Fatal(err)
	}
	if deployment.Status == "verified" {
		t.Fatal("test setup expected an unverified deployment")
	}
	if _, err := service.Rollback(context.Background(), deployment.ID, true); err == nil {
		t.Fatal("rollback of a non-verified deployment must be refused")
	}
}

func TestRollbackStopsOnUnknownDeployment(t *testing.T) {
	service, _ := newHarness(t, nil)
	if _, err := service.Rollback(context.Background(), "missing", true); err == nil {
		t.Fatal("rollback of an unknown deployment must be refused")
	}
}
