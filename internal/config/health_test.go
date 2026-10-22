package config

import "testing"

// healthyInput is a fully consistent baseline; each test removes or breaks exactly one thing
// so the resulting deduction can be attributed without ambiguity.
func healthyInput() HealthInput {
	return HealthInput{
		EnabledSubscriptions: 1,
		Subscriptions:        []SubscriptionHealth{{Name: "主力机场", LastStatus: "fetched"}},
		Nodes: []Node{
			{ID: "n1", Name: "主力机场 / 香港 01", SourceName: "香港 01", Status: "available"},
			{ID: "n2", Name: "主力机场 / 日本 01", SourceName: "日本 01", Status: "available"},
		},
		Groups: []GroupHealth{{
			ID: "g1", Name: "自动选择", SelectionMode: "manual", MemberNodeIDs: []string{"n1", "n2"},
		}},
		Rules: []RuleHealth{{ID: "r1", Name: "规则", Target: "g1", Enabled: true}},
	}
}

func TestComputeHealthFullyHealthyScores100(t *testing.T) {
	health := ComputeHealth(healthyInput())
	if health.Score != 100 || health.Grade != "healthy" {
		t.Fatalf("score=%d grade=%s, want 100/healthy", health.Score, health.Grade)
	}
	if health.BlockerCts != 0 || health.Warnings != 0 {
		t.Fatalf("blockers=%d warnings=%d, want 0/0", health.BlockerCts, health.Warnings)
	}
}

// A reference to a node that no longer exists blocks candidate composition, so it must be a
// blocker and cap the grade regardless of the remaining score.
func TestComputeHealthBrokenNodeReferenceIsBlocking(t *testing.T) {
	input := healthyInput()
	input.Groups[0].MemberNodeIDs = []string{"n1", "missing"}
	health := ComputeHealth(input)
	if health.Score != 75 {
		t.Fatalf("score=%d, want 75", health.Score)
	}
	if health.Grade != "blocked" || health.BlockerCts != 1 {
		t.Fatalf("grade=%s blockers=%d, want blocked/1", health.Grade, health.BlockerCts)
	}
	check := findCheck(t, health, "node_references")
	if check.Severity != "block" || check.Count != 1 {
		t.Fatalf("unexpected check: %+v", check)
	}
}

// A dynamic group whose filter matches nothing produces an invalid config, exactly like a
// broken reference.
func TestComputeHealthEmptyDynamicGroupIsBlocking(t *testing.T) {
	input := healthyInput()
	input.Groups = []GroupHealth{{
		ID: "g1", Name: "香港组", SelectionMode: "dynamic",
		MemberFilter: map[string]any{"keyword": "不存在的关键词"},
	}}
	input.Rules = []RuleHealth{{ID: "r1", Name: "规则", Target: "g1", Enabled: true}}
	health := ComputeHealth(input)
	check := findCheck(t, health, "group_members")
	if check.Severity != "block" || check.Deduction != healthWeightEmptyProxyGroup {
		t.Fatalf("unexpected check: %+v", check)
	}
}

func TestComputeHealthManualGroupWithoutMembersIsBlocking(t *testing.T) {
	input := healthyInput()
	input.Groups[0].MemberNodeIDs = nil
	input.Groups[0].MemberNodeNames = nil
	health := ComputeHealth(input)
	check := findCheck(t, health, "group_members")
	if check.Severity != "block" {
		t.Fatalf("an empty manual group must block: %+v", check)
	}
}

// Rules persist a group ID; a legacy record may hold a display name. Both must resolve.
func TestComputeHealthRuleTargetResolution(t *testing.T) {
	byID := healthyInput()
	if check := findCheck(t, ComputeHealth(byID), "rule_targets"); check.Severity != "ok" {
		t.Fatalf("target by ID should pass: %+v", check)
	}
	byName := healthyInput()
	byName.Rules[0].Target = "自动选择"
	if check := findCheck(t, ComputeHealth(byName), "rule_targets"); check.Severity != "ok" {
		t.Fatalf("target by display name should still pass: %+v", check)
	}
	broken := healthyInput()
	broken.Rules[0].Target = "g-does-not-exist"
	check := findCheck(t, ComputeHealth(broken), "rule_targets")
	if check.Severity != "block" || check.Deduction != healthWeightMissingRuleTarget {
		t.Fatalf("unresolvable target should block: %+v", check)
	}
}

// A disabled rule pointing nowhere is harmless and must not cost points.
func TestComputeHealthDisabledRuleIsIgnored(t *testing.T) {
	input := healthyInput()
	input.Rules = append(input.Rules, RuleHealth{ID: "r2", Name: "停用规则", Target: "gone", Enabled: false})
	if health := ComputeHealth(input); health.Score != 100 {
		t.Fatalf("a disabled rule must not affect the score: score=%d", health.Score)
	}
}

func TestComputeHealthNoInputsIsBlocking(t *testing.T) {
	health := ComputeHealth(HealthInput{EnabledSubscriptions: 0})
	check := findCheck(t, health, "inputs")
	if check.Severity != "block" || check.Deduction != healthWeightNoUsableNodes {
		t.Fatalf("unexpected check: %+v", check)
	}
	if health.Score != 80 {
		t.Fatalf("score=%d, want 80", health.Score)
	}
}

// A failed refresh keeps the previous snapshot usable, so it warns rather than blocks.
func TestComputeHealthStaleSubscriptionWarnsAndCaps(t *testing.T) {
	input := healthyInput()
	input.Subscriptions = []SubscriptionHealth{
		{Name: "a", LastStatus: "fetch_failed"},
		{Name: "b", LastStatus: "invalid_format"},
	}
	check := findCheck(t, ComputeHealth(input), "subscription_freshness")
	if check.Severity != "warn" || check.Deduction != 20 {
		t.Fatalf("two failures should deduct 20: %+v", check)
	}
	// Beyond two failures the deduction is capped so a single bad batch cannot zero the score.
	input.Subscriptions = []SubscriptionHealth{
		{Name: "a", LastStatus: "fetch_failed"},
		{Name: "b", LastStatus: "fetch_failed"},
		{Name: "c", LastStatus: "fetch_failed"},
		{Name: "d", LastStatus: "fetch_failed"},
	}
	if check := findCheck(t, ComputeHealth(input), "subscription_freshness"); check.Deduction != healthWeightStaleSubscriptionCap {
		t.Fatalf("deduction %d should be capped at %d", check.Deduction, healthWeightStaleSubscriptionCap)
	}
}

func TestComputeHealthUncertainDeploymentWarns(t *testing.T) {
	input := healthyInput()
	input.UncertainDeployments = 1
	health := ComputeHealth(input)
	if check := findCheck(t, health, "deployment_certainty"); check.Severity != "warn" {
		t.Fatalf("unexpected check: %+v", check)
	}
	if health.Score != 90 {
		t.Fatalf("score=%d, want 90", health.Score)
	}
}

// Node reachability must never reduce the score: a lab node pointing at a documentation
// address is a valid config, so scoring on it would misreport health.
func TestComputeHealthIgnoresNodeReachability(t *testing.T) {
	input := healthyInput()
	for i := range input.Nodes {
		input.Nodes[i].Status = "unavailable"
	}
	if health := ComputeHealth(input); health.Score != 100 {
		t.Fatalf("unreachable nodes must not lower the score: score=%d", health.Score)
	}
}

func TestComputeHealthMostlyUnprobedWarns(t *testing.T) {
	input := healthyInput()
	// Two of two unmeasured crosses the 50% threshold.
	for i := range input.Nodes {
		input.Nodes[i].Status = "unmeasured"
	}
	health := ComputeHealth(input)
	if check := findCheck(t, health, "probe_coverage"); check.Severity != "warn" {
		t.Fatalf("unexpected check: %+v", check)
	}
	if health.Score != 95 {
		t.Fatalf("score=%d, want 95", health.Score)
	}
}

// Even with warnings the score never goes negative, and a blocker always wins the grade.
func TestComputeHealthScoreFloorAndGradePrecedence(t *testing.T) {
	health := ComputeHealth(HealthInput{EnabledSubscriptions: 0, UncertainDeployments: 3})
	if health.Score < 0 {
		t.Fatalf("score went negative: %d", health.Score)
	}
	if health.Grade != "blocked" {
		t.Fatalf("a blocker should win over the numeric score: grade=%s score=%d", health.Grade, health.Score)
	}
}

func TestComputeHealthGradeBoundaries(t *testing.T) {
	cases := map[int]string{100: "healthy", 99: "attention", 70: "attention", 69: "degraded", 0: "degraded"}
	for score, want := range cases {
		if got := healthGrade(score, 0); got != want {
			t.Fatalf("healthGrade(%d) = %s, want %s", score, got, want)
		}
	}
}

// A rule that can never match does not stop a publish, so it must warn rather than block.
func TestComputeHealthRuleConflictsWarn(t *testing.T) {
	input := healthyInput()
	input.RuleAnalysis = RuleAnalysis{
		Checked: 2,
		Conflicts: []RuleConflict{{
			Kind: ConflictShadowed, RuleID: "r2", Name: "被遮蔽的规则", Severity: "warn",
		}},
	}
	health := ComputeHealth(input)
	check := findCheck(t, health, "rule_conflicts")
	if check.Severity != "warn" || check.Deduction != healthWeightRuleConflicts {
		t.Fatalf("unexpected check: %+v", check)
	}
	if health.Grade == "blocked" {
		t.Fatalf("rule conflicts alone must not block: grade=%s", health.Grade)
	}
	if health.Score != 95 {
		t.Fatalf("score=%d, want 95", health.Score)
	}
}

func TestComputeHealthNoRuleConflictsIsOk(t *testing.T) {
	input := healthyInput()
	input.RuleAnalysis = RuleAnalysis{Checked: 1}
	health := ComputeHealth(input)
	if check := findCheck(t, health, "rule_conflicts"); check.Severity != "ok" {
		t.Fatalf("unexpected check: %+v", check)
	}
	if health.Score != 100 {
		t.Fatalf("score=%d, want 100", health.Score)
	}
}

func findCheck(t *testing.T, health Health, id string) HealthCheck {
	t.Helper()
	for _, check := range health.Checks {
		if check.ID == id {
			return check
		}
	}
	t.Fatalf("check %q not present in %+v", id, health.Checks)
	return HealthCheck{}
}
