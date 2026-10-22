package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"mihomo-studio/internal/domain"
)

func TestSaveSubscriptionEditPreservesFetchState(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "studio.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.DB.Close()

	lastFetch := time.Date(2026, 10, 22, 8, 30, 0, 0, time.UTC)
	original := domain.Subscription{
		ID:          "primary",
		Name:        "Primary",
		URL:         "https://example.com/subscription",
		Enabled:     true,
		LastFetchAt: &lastFetch,
		LastStatus:  "fetched",
	}
	if err := s.SaveSubscription(ctx, original); err != nil {
		t.Fatalf("save original subscription: %v", err)
	}

	// An edit request contains user-managed fields only; it must not reset the
	// status or timestamp recorded by the most recent refresh operation.
	edited := domain.Subscription{
		ID:      "primary",
		Name:    "Renamed",
		URL:     "https://example.com/updated-subscription",
		Enabled: true,
	}
	if err := s.SaveSubscription(ctx, edited); err != nil {
		t.Fatalf("save edited subscription: %v", err)
	}

	items, err := s.ListSubscriptions(ctx)
	if err != nil {
		t.Fatalf("list subscriptions: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d subscriptions, want 1", len(items))
	}
	got := items[0]
	if got.Name != edited.Name || got.URL != edited.URL {
		t.Fatalf("editable fields not updated: got name=%q url=%q", got.Name, got.URL)
	}
	if got.LastStatus != "fetched" {
		t.Errorf("last status = %q, want fetched", got.LastStatus)
	}
	if got.LastFetchAt == nil || !got.LastFetchAt.Equal(lastFetch) {
		t.Errorf("last fetch time = %v, want %v", got.LastFetchAt, lastFetch)
	}
}
func TestNewSubscriptionStartsWithoutFetchHistory(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "studio.db"))
	if err != nil { t.Fatalf("open store: %v", err) }
	defer s.DB.Close()
	x := domain.Subscription{ID: "fresh", Name: "Fresh", URL: "https://example.com/fresh", Enabled: true}
	if err := s.SaveSubscription(ctx, x); err != nil { t.Fatalf("save subscription: %v", err) }
	items, err := s.ListSubscriptions(ctx)
	if err != nil { t.Fatalf("list subscriptions: %v", err) }
	if len(items) != 1 { t.Fatalf("got %d subscriptions, want 1", len(items)) }
	if items[0].LastStatus != "never_fetched" { t.Errorf("last status = %q, want never_fetched", items[0].LastStatus) }
	if items[0].LastFetchAt != nil { t.Errorf("last fetch time = %v, want nil", items[0].LastFetchAt) }
}
func TestDisablingSubscriptionPreservesFetchHistory(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "studio.db"))
	if err != nil { t.Fatalf("open store: %v", err) }
	defer s.DB.Close()
	lastFetch := time.Date(2026, 10, 22, 8, 30, 0, 0, time.UTC)
	x := domain.Subscription{ID: "primary", Name: "Primary", URL: "https://example.com/subscription", Enabled: true, LastFetchAt: &lastFetch, LastStatus: "fetched"}
	if err := s.SaveSubscription(ctx, x); err != nil { t.Fatalf("save subscription: %v", err) }
	x.Enabled = false
	x.LastFetchAt = nil
	x.LastStatus = ""
	if err := s.SaveSubscription(ctx, x); err != nil { t.Fatalf("disable subscription: %v", err) }
	items, err := s.ListSubscriptions(ctx)
	if err != nil { t.Fatalf("list subscriptions: %v", err) }
	if len(items) != 1 { t.Fatalf("got %d subscriptions, want 1", len(items)) }
	got := items[0]
	if got.Enabled { t.Error("subscription remains enabled after disabling") }
	if got.LastStatus != "fetched" { t.Errorf("last status = %q, want fetched", got.LastStatus) }
	if got.LastFetchAt == nil || !got.LastFetchAt.Equal(lastFetch) { t.Errorf("last fetch time = %v, want %v", got.LastFetchAt, lastFetch) }
}
