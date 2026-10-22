package config

import (
	"strings"
	"testing"

	"mihomo-studio/internal/domain"
)

func rule(id, ruleType, value, target string, priority int) domain.Rule {
	return domain.Rule{ID: id, Name: ruleType + " " + value, RuleType: ruleType, RuleValue: value, Target: target, Priority: priority, Enabled: true}
}

func conflictFor(analysis RuleAnalysis, ruleID string, kind ConflictKind) (RuleConflict, bool) {
	for _, conflict := range analysis.Conflicts {
		if conflict.RuleID == ruleID && conflict.Kind == kind {
			return conflict, true
		}
	}
	return RuleConflict{}, false
}

func TestAnalyzeRulesFindsExactDuplicate(t *testing.T) {
	analysis := AnalyzeRules([]domain.Rule{
		rule("r1", "DOMAIN-SUFFIX", "openai.com", "g1", 100),
		rule("r2", "DOMAIN-SUFFIX", "openai.com", "g1", 200),
	})
	conflict, ok := conflictFor(analysis, "r2", ConflictDuplicate)
	if !ok {
		t.Fatalf("expected r2 to be a duplicate: %+v", analysis.Conflicts)
	}
	if conflict.ConflictsWith != "r1" || !strings.Contains(conflict.Detail, "r1") && !strings.Contains(conflict.Detail, "openai.com") {
		t.Fatalf("unexpected conflict: %+v", conflict)
	}
}

// Same match condition but a different target is shadowing, not a duplicate: the second rule
// can never run, which is a different mistake with a different fix.
func TestAnalyzeRulesSameConditionDifferentTargetIsShadowed(t *testing.T) {
	analysis := AnalyzeRules([]domain.Rule{
		rule("r1", "DOMAIN-SUFFIX", "openai.com", "g1", 100),
		rule("r2", "DOMAIN-SUFFIX", "openai.com", "g2", 200),
	})
	if _, ok := conflictFor(analysis, "r2", ConflictShadowed); !ok {
		t.Fatalf("expected shadowed: %+v", analysis.Conflicts)
	}
}

func TestAnalyzeRulesKeywordShadowsLaterDomainRules(t *testing.T) {
	analysis := AnalyzeRules([]domain.Rule{
		rule("r1", "DOMAIN-KEYWORD", "telemetry", "REJECT", 100),
		rule("r2", "DOMAIN", "metrics.telemetry.example", "g1", 200),
		rule("r3", "DOMAIN-SUFFIX", "telemetry.example", "g1", 300),
	})
	for _, id := range []string{"r2", "r3"} {
		if _, ok := conflictFor(analysis, id, ConflictShadowed); !ok {
			t.Fatalf("expected %s to be shadowed by the keyword rule: %+v", id, analysis.Conflicts)
		}
	}
	// A domain that does not contain the keyword must survive.
	analysis = AnalyzeRules([]domain.Rule{
		rule("r1", "DOMAIN-KEYWORD", "telemetry", "REJECT", 100),
		rule("r2", "DOMAIN", "api.openai.com", "g1", 200),
	})
	if len(analysis.Conflicts) != 0 {
		t.Fatalf("unrelated domain should not be shadowed: %+v", analysis.Conflicts)
	}
}

func TestAnalyzeRulesSuffixShadowsSubdomain(t *testing.T) {
	analysis := AnalyzeRules([]domain.Rule{
		rule("r1", "DOMAIN-SUFFIX", "example.com", "g1", 100),
		rule("r2", "DOMAIN", "api.example.com", "g2", 200),
		rule("r3", "DOMAIN-SUFFIX", "sub.example.com", "g2", 300),
		rule("r4", "DOMAIN", "notexample.com", "g2", 400),
	})
	if _, ok := conflictFor(analysis, "r2", ConflictShadowed); !ok {
		t.Fatalf("api.example.com should be shadowed: %+v", analysis.Conflicts)
	}
	if _, ok := conflictFor(analysis, "r3", ConflictShadowed); !ok {
		t.Fatalf("sub.example.com should be shadowed: %+v", analysis.Conflicts)
	}
	// "notexample.com" does not end with ".example.com", so it must not be flagged.
	if _, ok := conflictFor(analysis, "r4", ConflictShadowed); ok {
		t.Fatalf("notexample.com must not be shadowed by example.com: %+v", analysis.Conflicts)
	}
}

func TestAnalyzeRulesCidrContainment(t *testing.T) {
	analysis := AnalyzeRules([]domain.Rule{
		rule("r1", "IP-CIDR", "10.0.0.0/8", "g1", 100),
		rule("r2", "IP-CIDR", "10.1.2.0/24", "g2", 200),
	})
	conflict, ok := conflictFor(analysis, "r2", ConflictShadowed)
	if !ok {
		t.Fatalf("a narrower CIDR inside a broader one must be shadowed: %+v", analysis.Conflicts)
	}
	if !strings.Contains(conflict.Detail, "10.1.2.0/24") {
		t.Fatalf("detail should name the shadowed network: %+v", conflict)
	}
}

// A broader CIDR placed after a narrower one is not shadowed; the narrow rule only covers part
// of it, so the wider rule still matches other traffic.
func TestAnalyzeRulesBroaderCidrLaterIsNotShadowed(t *testing.T) {
	analysis := AnalyzeRules([]domain.Rule{
		rule("r1", "IP-CIDR", "10.1.2.0/24", "g2", 100),
		rule("r2", "IP-CIDR", "10.0.0.0/8", "g1", 200),
	})
	if len(analysis.Conflicts) != 0 {
		t.Fatalf("a later broader CIDR is still reachable: %+v", analysis.Conflicts)
	}
}

// A later rule with a higher priority value runs later even if the slice order differs, so the
// analysis must sort rather than trust input order.
func TestAnalyzeRulesUsesEffectiveOrderNotInputOrder(t *testing.T) {
	analysis := AnalyzeRules([]domain.Rule{
		rule("r-late", "DOMAIN-SUFFIX", "example.com", "g1", 900),
		rule("r-early", "DOMAIN", "api.example.com", "g2", 10),
	})
	for _, conflict := range analysis.Conflicts {
		if conflict.RuleID == "r-early" {
			t.Fatalf("the earlier rule must not be reported as shadowed: %+v", conflict)
		}
	}
	if _, ok := conflictFor(analysis, "r-late", ConflictShadowed); ok {
		t.Fatalf("the later suffix rule cannot be shadowed by an exact domain rule: %+v", analysis.Conflicts)
	}
}

func TestAnalyzeRulesSrcCidrOverlapIsReported(t *testing.T) {
	analysis := AnalyzeRules([]domain.Rule{
		rule("r1", "SRC-IP-CIDR", "192.168.1.0/24", "g1", 100),
		rule("r2", "SRC-IP-CIDR", "192.168.0.0/16", "g2", 200),
	})
	conflict, ok := conflictFor(analysis, "r2", ConflictCidrOverlap)
	if !ok {
		t.Fatalf("overlapping source ranges with different targets must be reported: %+v", analysis.Conflicts)
	}
	// An overlap still yields a valid configuration, so it warns rather than blocks. Only
	// invalid configuration blocks, and the builder catches that separately.
	if conflict.Severity != "warn" {
		t.Fatalf("an ambiguous source route should warn, not block: %+v", conflict)
	}
}

func TestAnalyzeRulesSrcCidrSameTargetIsFine(t *testing.T) {
	analysis := AnalyzeRules([]domain.Rule{
		rule("r1", "SRC-IP-CIDR", "192.168.1.0/24", "g1", 100),
		rule("r2", "SRC-IP-CIDR", "192.168.0.0/16", "g1", 200),
	})
	if len(analysis.Conflicts) != 0 {
		t.Fatalf("overlap with the same target is harmless: %+v", analysis.Conflicts)
	}
}

func TestAnalyzeRulesIgnoresDisabledRules(t *testing.T) {
	disabled := rule("r2", "DOMAIN-SUFFIX", "openai.com", "g1", 200)
	disabled.Enabled = false
	analysis := AnalyzeRules([]domain.Rule{
		rule("r1", "DOMAIN-SUFFIX", "openai.com", "g1", 100),
		disabled,
	})
	if len(analysis.Conflicts) != 0 {
		t.Fatalf("disabled rules cannot conflict: %+v", analysis.Conflicts)
	}
	if analysis.Checked != 1 {
		t.Fatalf("checked = %d, want 1", analysis.Checked)
	}
}

// The analysis must not claim relationships it cannot prove from the rule types alone.
func TestAnalyzeRulesDoesNotGuessForOpaqueRuleTypes(t *testing.T) {
	analysis := AnalyzeRules([]domain.Rule{
		rule("r1", "GEOSITE", "cn", "DIRECT", 100),
		rule("r2", "GEOIP", "CN", "DIRECT", 200),
		rule("r3", "RULE-SET", "ads", "REJECT", 300),
		rule("r4", "DOMAIN-SUFFIX", "example.com", "g1", 400),
	})
	if len(analysis.Conflicts) != 0 {
		t.Fatalf("opaque rule types must not produce shadowing claims: %+v", analysis.Conflicts)
	}
	if analysis.Limitations == "" {
		t.Fatal("the result must state what it cannot determine")
	}
}

func TestAnalyzeRulesEmptyInput(t *testing.T) {
	analysis := AnalyzeRules(nil)
	if analysis.Conflicts == nil {
		t.Fatal("Conflicts must be an empty slice, not nil, so JSON encodes []")
	}
	if analysis.Checked != 0 {
		t.Fatalf("checked = %d, want 0", analysis.Checked)
	}
}
