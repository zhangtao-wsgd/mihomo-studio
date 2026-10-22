package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"mihomo-studio/internal/domain"
)

func saveRuleForTest(t *testing.T, server *Server, rule domain.Rule) {
	t.Helper()
	if err := server.Store.SaveRule(context.Background(), rule); err != nil {
		t.Fatal(err)
	}
}

func TestRuleConflictsEmptyIsArray(t *testing.T) {
	server := newOverviewServer(t)
	response := doRequest(t, server, http.MethodGet, "/api/v1/rules/conflicts")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		Conflicts   []map[string]any `json:"conflicts"`
		Checked     int              `json:"checked"`
		Limitations string           `json:"limitations"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Conflicts == nil {
		t.Fatalf("conflicts encoded as null: %s", response.Body.String())
	}
	if payload.Checked != 0 {
		t.Fatalf("checked = %d, want 0", payload.Checked)
	}
	if payload.Limitations == "" {
		t.Fatal("response should state what the analysis cannot determine")
	}
}

func TestRuleConflictsReportsShadowedRule(t *testing.T) {
	server := newOverviewServer(t)
	saveRuleForTest(t, server, domain.Rule{
		ID: "r1", Name: "broad", RuleType: "DOMAIN-SUFFIX", RuleValue: "example.com",
		Target: "DIRECT", Priority: 100, Source: "custom", Enabled: true,
	})
	saveRuleForTest(t, server, domain.Rule{
		ID: "r2", Name: "narrow", RuleType: "DOMAIN", RuleValue: "api.example.com",
		Target: "DIRECT", Priority: 200, Source: "custom", Enabled: true,
	})

	response := doRequest(t, server, http.MethodGet, "/api/v1/rules/conflicts")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	var payload struct {
		Conflicts []struct {
			Kind          string `json:"kind"`
			RuleID        string `json:"ruleId"`
			ConflictsWith string `json:"conflictsWith"`
			Severity      string `json:"severity"`
		} `json:"conflicts"`
		Checked int `json:"checked"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Checked != 2 {
		t.Fatalf("checked = %d, want 2", payload.Checked)
	}
	if len(payload.Conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %+v", payload.Conflicts)
	}
	conflict := payload.Conflicts[0]
	if conflict.RuleID != "r2" || conflict.ConflictsWith != "r1" || conflict.Kind != "shadowed" {
		t.Fatalf("unexpected conflict: %+v", conflict)
	}
	if conflict.Severity != "warn" {
		t.Fatalf("a dead rule must warn, not block: %+v", conflict)
	}
}

// The analysis is read-only: a request must never mutate the stored rules.
func TestRuleConflictsDoesNotMutateRules(t *testing.T) {
	server := newOverviewServer(t)
	saveRuleForTest(t, server, domain.Rule{
		ID: "r1", Name: "a", RuleType: "DOMAIN-SUFFIX", RuleValue: "example.com",
		Target: "DIRECT", Priority: 100, Source: "custom", Enabled: true,
	})
	before, err := server.Store.ListRules(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	doRequest(t, server, http.MethodGet, "/api/v1/rules/conflicts")
	after, err := server.Store.ListRules(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatalf("rule count changed from %d to %d", len(before), len(after))
	}
	if before[0].Enabled != after[0].Enabled || before[0].Target != after[0].Target {
		t.Fatal("the analysis must not modify rules")
	}
}

func TestRuleConflictsRequiresAuth(t *testing.T) {
	server := newOverviewServer(t)
	server.Auth = AuthConfig{Token: "s3cret"}
	response := doRequest(t, server, http.MethodGet, "/api/v1/rules/conflicts")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.Code)
	}
}
