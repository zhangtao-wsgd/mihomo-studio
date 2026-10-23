package config

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mihomo-studio/internal/domain"
	"mihomo-studio/internal/store"
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

// The controller compares Origin exactly, so a dashboard served from a LAN address must be
// listed explicitly. The default only covers loopback and must not silently widen.
func TestCorsOriginsDefaultIsLoopbackOnly(t *testing.T) {
	got := (&Builder{}).corsOrigins()
	want := []string{"http://127.0.0.1:8081", "http://localhost:8081"}
	if len(got) != len(want) {
		t.Fatalf("corsOrigins() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("corsOrigins() = %v, want %v", got, want)
		}
	}
}

func TestCorsOriginsUsesConfiguredList(t *testing.T) {
	configured := []string{"http://192.168.152.130:8081", "https://studio.example"}
	got := (&Builder{DashboardOrigins: configured}).corsOrigins()
	if len(got) != len(configured) {
		t.Fatalf("corsOrigins() = %v, want %v", got, configured)
	}
	for i := range configured {
		if got[i] != configured[i] {
			t.Fatalf("corsOrigins() = %v, want %v", got, configured)
		}
	}
}

func TestBuildFailurePreservesCurrentSubscriptionSnapshot(t *testing.T) {
	cases := []struct {
		name       string
		fetchBody  []byte
		fetchErr   error
		wantStatus string
	}{
		{name: "network failure", fetchErr: errors.New("provider unavailable"), wantStatus: "fetch_failed"},
		{name: "invalid subscription payload", fetchBody: []byte("not a valid proxy subscription"), wantStatus: "parse_failed"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st, err := store.Open(filepath.Join(t.TempDir(), "studio.db"))
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			defer st.DB.Close()

			sub := domain.Subscription{
				ID: "primary", Name: "Primary", URL: "https://subscription.example/sub.yaml",
				Enabled: true, LastStatus: "fetched",
			}
			if err := st.SaveSubscription(ctx, sub); err != nil {
				t.Fatalf("save subscription: %v", err)
			}

			snapshotPath := filepath.Join(t.TempDir(), "previous.yaml")
			previousBody := []byte("proxies:\n  - name: old-node\n")
			if err := os.WriteFile(snapshotPath, previousBody, 0o600); err != nil {
				t.Fatalf("write previous snapshot: %v", err)
			}
			previousDigest := sha256.Sum256(previousBody)
			oldHash := hex.EncodeToString(previousDigest[:])
			builder := &Builder{DB: st.DB, SnapshotDir: t.TempDir(), ControllerSecret: "test-secret"}
			if err := builder.recordCurrentSnapshot(ctx, sub.ID, oldHash, snapshotPath, time.Now().UTC().Format(time.RFC3339Nano), 1); err != nil {
				t.Fatalf("seed previous snapshot: %v", err)
			}
			builder.fetch = func(context.Context, string) ([]byte, error) {
				return tc.fetchBody, tc.fetchErr
			}

			candidate, buildErr := builder.Build(ctx)
			if buildErr != nil {
				t.Fatalf("Build should reuse the last-known-good snapshot after refresh failure: %v", buildErr)
			}
			if !strings.Contains(candidate.ValidationMessage, "Partial subscription refresh") {
				t.Fatalf("candidate should clearly disclose stale source reuse, got %q", candidate.ValidationMessage)
			}

			var currentHash, currentPath string
			var current int
			if err := st.DB.QueryRowContext(ctx, `SELECT content_sha256,snapshot_path,is_current FROM subscription_snapshots WHERE subscription_id=? AND is_current=1`, sub.ID).Scan(&currentHash, &currentPath, &current); err != nil {
				t.Fatalf("read current snapshot: %v", err)
			}
			if currentHash != oldHash || currentPath != snapshotPath || current != 1 {
				t.Fatalf("current snapshot changed after failed refresh: hash=%q path=%q current=%d", currentHash, currentPath, current)
			}

			var gotStatus string
			if err := st.DB.QueryRowContext(ctx, `SELECT last_status FROM subscriptions WHERE id=?`, sub.ID).Scan(&gotStatus); err != nil {
				t.Fatalf("read subscription status: %v", err)
			}
			if gotStatus != tc.wantStatus {
				t.Errorf("last status = %q, want %q", gotStatus, tc.wantStatus)
			}
			var candidateInputs int
			if err := st.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM config_candidate_inputs WHERE candidate_id=? AND subscription_id=? AND content_sha256=?`, candidate.ID, sub.ID, oldHash).Scan(&candidateInputs); err != nil {
				t.Fatalf("read candidate input manifest: %v", err)
			}
			if candidateInputs != 1 {
				t.Errorf("candidate input manifest should reference previous snapshot, count=%d", candidateInputs)
			}
		})
	}
}

func TestBuildRejectsTamperedLastKnownGoodSnapshot(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "studio.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.DB.Close()

	sub := domain.Subscription{
		ID: "primary", Name: "Primary", URL: "https://subscription.example/sub.yaml",
		Enabled: true, LastStatus: "fetched",
	}
	if err := st.SaveSubscription(ctx, sub); err != nil {
		t.Fatalf("save subscription: %v", err)
	}

	snapshotPath := filepath.Join(t.TempDir(), "previous.yaml")
	originalBody := []byte("proxies:\n  - name: trusted-node\n")
	if err := os.WriteFile(snapshotPath, originalBody, 0o600); err != nil {
		t.Fatalf("write previous snapshot: %v", err)
	}
	digest := sha256.Sum256(originalBody)
	originalHash := hex.EncodeToString(digest[:])
	builder := &Builder{DB: st.DB, SnapshotDir: t.TempDir(), ControllerSecret: "test-secret"}
	if err := builder.recordCurrentSnapshot(ctx, sub.ID, originalHash, snapshotPath, time.Now().UTC().Format(time.RFC3339Nano), 1); err != nil {
		t.Fatalf("seed previous snapshot: %v", err)
	}

	// Simulate disk corruption after the snapshot was recorded as current.
	if err := os.WriteFile(snapshotPath, []byte("proxies:\n  - name: tampered-node\n"), 0o600); err != nil {
		t.Fatalf("tamper previous snapshot: %v", err)
	}
	builder.fetch = func(context.Context, string) ([]byte, error) {
		return nil, errors.New("provider unavailable")
	}

	_, buildErr := builder.Build(ctx)
	if buildErr == nil || !strings.Contains(buildErr.Error(), "SHA-256 integrity verification") {
		t.Fatalf("Build error = %v, want snapshot integrity verification failure", buildErr)
	}

	var currentHash string
	var current int
	if err := st.DB.QueryRowContext(ctx, `SELECT content_sha256,is_current FROM subscription_snapshots WHERE subscription_id=? AND is_current=1`, sub.ID).Scan(&currentHash, &current); err != nil {
		t.Fatalf("read current snapshot: %v", err)
	}
	if currentHash != originalHash || current != 1 {
		t.Fatalf("corrupt snapshot pointer changed: hash=%q current=%d", currentHash, current)
	}

	var candidates int
	if err := st.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM config_candidates`).Scan(&candidates); err != nil {
		t.Fatalf("count candidates: %v", err)
	}
	if candidates != 0 {
		t.Fatalf("corrupt snapshot must not produce a candidate, got %d", candidates)
	}
}

func TestBuildBlocksFailedRefreshWithoutPreviousSnapshot(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "studio.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.DB.Close()

	sub := domain.Subscription{
		ID: "fresh", Name: "Fresh", URL: "https://subscription.example/sub.yaml",
		Enabled: true,
	}
	if err := st.SaveSubscription(ctx, sub); err != nil {
		t.Fatalf("save subscription: %v", err)
	}

	builder := &Builder{DB: st.DB, SnapshotDir: t.TempDir(), ControllerSecret: "test-secret"}
	builder.fetch = func(context.Context, string) ([]byte, error) {
		return nil, errors.New("provider unavailable")
	}
	_, buildErr := builder.Build(ctx)
	if buildErr == nil ||
		!strings.Contains(buildErr.Error(), "no last-known-good snapshot exists") ||
		!strings.Contains(buildErr.Error(), "candidate generation is blocked") {
		t.Fatalf("Build error = %v, want explicit block when no trusted snapshot exists", buildErr)
	}

	var candidates int
	if err := st.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM config_candidates`).Scan(&candidates); err != nil {
		t.Fatalf("count candidates: %v", err)
	}
	if candidates != 0 {
		t.Fatalf("failed initial refresh must not produce a candidate, got %d", candidates)
	}

	var status string
	if err := st.DB.QueryRowContext(ctx, `SELECT last_status FROM subscriptions WHERE id=?`, sub.ID).Scan(&status); err != nil {
		t.Fatalf("read subscription status: %v", err)
	}
	if status != "fetch_failed" {
		t.Fatalf("subscription status = %q, want fetch_failed", status)
	}
}

