package config

import (
	"fmt"
	"sort"
)

// Overview health is a derived summary, not a stored fact. Every deduction below is tied to
// a check whose evidence is returned alongside it, so the number can always be explained.
//
// The score deliberately does NOT incorporate node reachability. A node pointing at a
// private or documentation address reports `unavailable` in a lab environment while the
// configuration itself is perfectly valid, so scoring on it would produce a misleading
// verdict. Availability is reported separately as a count.
const (
	healthWeightBrokenNodeReference  = 25
	healthWeightEmptyProxyGroup      = 25
	healthWeightMissingRuleTarget    = 20
	healthWeightNoUsableNodes        = 20
	healthWeightStaleSubscription    = 10
	healthWeightStaleSubscriptionCap = 20
	healthWeightUncertainDeployment  = 10
	healthWeightMostlyUnprobed       = 5
	// healthWeightRuleConflicts is small because a dead rule still yields a valid config; it
	// costs confidence in whether the configuration does what the operator intended.
	healthWeightRuleConflicts = 5
	// majorityUnprobedRatio is the unprobed share above which the operator cannot judge
	// node health at all. It costs little because it is a visibility gap, not a defect.
	majorityUnprobedRatio = 0.5
)

// HealthCheck is one explainable result contributing to the score.
type HealthCheck struct {
	ID string `json:"id"`
	// Severity is `block` when the condition prevents a valid publish, `warn` when it
	// degrades confidence, and `ok` when the check passed.
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	// Deduction is how many points this check cost. Zero for a passing check.
	Deduction int `json:"deduction"`
	// Count is the number of offending items, when the check aggregates several.
	Count int `json:"count"`
}

// Health is the derived health summary for the overview page.
type Health struct {
	Score      int           `json:"score"`
	Grade      string        `json:"grade"`
	BlockerCts int           `json:"blockers"`
	Warnings   int           `json:"warnings"`
	Checks     []HealthCheck `json:"checks"`
}

// HealthInput collects the already-loaded facts the checks run against. Everything here is
// read from persisted state; nothing is fetched from the runtime, so the score cannot be
// delayed or made flaky by an unreachable controller.
type HealthInput struct {
	EnabledSubscriptions int
	Subscriptions        []SubscriptionHealth
	Nodes                []Node
	Groups               []GroupHealth
	Rules                []RuleHealth
	// RuleAnalysis carries the shadowing/overlap findings so the score reflects rules that
	// can never take effect. It is computed by the caller from the same rule set.
	RuleAnalysis         RuleAnalysis
	UncertainDeployments int
}

// SubscriptionHealth is the slice of subscription state the checks need.
type SubscriptionHealth struct {
	Name       string
	LastStatus string
}

// GroupHealth carries what is needed to decide whether a group can produce members.
type GroupHealth struct {
	ID              string
	Name            string
	SelectionMode   string
	MemberNodeIDs   []string
	MemberNodeNames []string
	MemberFilter    map[string]any
}

// RuleHealth is the slice of rule state the checks need.
type RuleHealth struct {
	ID      string
	Name    string
	Target  string
	Enabled bool
}

// ComputeHealth derives the score and the list of checks that produced it.
func ComputeHealth(input HealthInput) Health {
	checks := make([]HealthCheck, 0, 7)
	nodesByID := make(map[string]bool, len(input.Nodes))
	nodesByName := make(map[string]bool, len(input.Nodes))
	unprobed := 0
	for _, node := range input.Nodes {
		nodesByID[node.ID] = true
		nodesByName[node.SourceName] = true
		nodesByName[node.Name] = true
		if node.Status == "unmeasured" || node.Status == "not_loaded" {
			unprobed++
		}
	}

	// 1. Broken node references. Candidate composition fails outright on these, so they are
	// the single most actionable signal.
	brokenReferences := 0
	brokenGroups := make([]string, 0)
	for _, group := range input.Groups {
		broken := 0
		for _, nodeID := range group.MemberNodeIDs {
			if !nodesByID[nodeID] {
				broken++
			}
		}
		brokenReferences += broken
		if broken > 0 {
			brokenGroups = append(brokenGroups, group.Name)
		}
	}
	if brokenReferences > 0 {
		checks = append(checks, HealthCheck{
			ID: "node_references", Severity: "block",
			Title:     "有代理组引用了当前快照中不存在的节点",
			Detail:    fmt.Sprintf("%d 处引用无法解析，涉及代理组：%s。构建候选会失败，需要刷新订阅并复核成员。", brokenReferences, joinLimited(brokenGroups, 3)),
			Deduction: healthWeightBrokenNodeReference, Count: brokenReferences,
		})
	} else {
		checks = append(checks, HealthCheck{ID: "node_references", Severity: "ok", Title: "节点引用完整", Detail: "所有手动引用的节点 ID 都能在当前快照中解析。"})
	}

	// 2. Groups that cannot yield any member. A dynamic group whose filter matches nothing is
	// as blocking as a missing reference, because the generated config would be invalid.
	emptyGroups := make([]string, 0)
	for _, group := range input.Groups {
		if group.SelectionMode == "manual" {
			if len(group.MemberNodeIDs) == 0 && len(group.MemberNodeNames) == 0 {
				emptyGroups = append(emptyGroups, group.Name)
			}
			continue
		}
		matched := 0
		for name := range nodesByName {
			if matchFilter(name, group.MemberFilter) {
				matched++
			}
		}
		if matched == 0 {
			emptyGroups = append(emptyGroups, group.Name)
		}
	}
	if len(emptyGroups) > 0 {
		sort.Strings(emptyGroups)
		checks = append(checks, HealthCheck{
			ID: "group_members", Severity: "block",
			Title:     "有代理组没有任何可用的候选节点",
			Detail:    fmt.Sprintf("%d 个代理组无法产生成员：%s。构建候选会失败。", len(emptyGroups), joinLimited(emptyGroups, 3)),
			Deduction: healthWeightEmptyProxyGroup, Count: len(emptyGroups),
		})
	} else if len(input.Groups) > 0 {
		checks = append(checks, HealthCheck{ID: "group_members", Severity: "ok", Title: "代理组成员可用", Detail: "每个代理组都能匹配到节点。"})
	}

	// 3. Rule targets that no longer resolve to DIRECT, REJECT, or an existing group.
	// Rules persist a group ID. A legacy record may still hold a display name, so both are
	// accepted here; anything else is unresolvable.
	groupIDs := make(map[string]bool, len(input.Groups))
	groupNames := make(map[string]bool, len(input.Groups))
	for _, group := range input.Groups {
		if group.ID != "" {
			groupIDs[group.ID] = true
		}
		groupNames[group.Name] = true
	}
	missingTargets := make([]string, 0)
	for _, rule := range input.Rules {
		if !rule.Enabled {
			continue
		}
		if rule.Target == "DIRECT" || rule.Target == "REJECT" {
			continue
		}
		if !groupIDs[rule.Target] && !groupNames[rule.Target] {
			missingTargets = append(missingTargets, rule.Name)
		}
	}
	if len(missingTargets) > 0 {
		checks = append(checks, HealthCheck{
			ID: "rule_targets", Severity: "block",
			Title:     "有启用规则的目标无法解析",
			Detail:    fmt.Sprintf("%d 条规则指向不存在的代理组：%s。", len(missingTargets), joinLimited(missingTargets, 3)),
			Deduction: healthWeightMissingRuleTarget, Count: len(missingTargets),
		})
	} else {
		checks = append(checks, HealthCheck{ID: "rule_targets", Severity: "ok", Title: "规则目标可解析", Detail: "所有启用规则都指向 DIRECT、REJECT 或存在的代理组。"})
	}

	// 4. Nothing to build from.
	if input.EnabledSubscriptions == 0 || len(input.Nodes) == 0 {
		detail := "还没有启用任何订阅。"
		if input.EnabledSubscriptions > 0 {
			detail = "已启用订阅，但当前没有任何节点快照；请先刷新订阅。"
		}
		checks = append(checks, HealthCheck{
			ID: "inputs", Severity: "block", Title: "没有可用于生成配置的节点",
			Detail: detail, Deduction: healthWeightNoUsableNodes,
		})
	} else {
		checks = append(checks, HealthCheck{ID: "inputs", Severity: "ok", Title: "输入可用", Detail: fmt.Sprintf("%d 个启用订阅提供 %d 个节点。", input.EnabledSubscriptions, len(input.Nodes))})
	}

	// 5. Subscriptions whose last refresh failed. The previous snapshot remains usable, so
	// this degrades confidence rather than blocking.
	failed := make([]string, 0)
	for _, subscription := range input.Subscriptions {
		switch subscription.LastStatus {
		case "fetch_failed", "invalid_format":
			failed = append(failed, subscription.Name)
		}
	}
	if len(failed) > 0 {
		deduction := len(failed) * healthWeightStaleSubscription
		if deduction > healthWeightStaleSubscriptionCap {
			deduction = healthWeightStaleSubscriptionCap
		}
		checks = append(checks, HealthCheck{
			ID: "subscription_freshness", Severity: "warn",
			Title:     "有订阅最近一次获取失败",
			Detail:    fmt.Sprintf("%d 个订阅失败：%s。系统仍使用上一次成功快照，但输入可能已过期。", len(failed), joinLimited(failed, 3)),
			Deduction: deduction, Count: len(failed),
		})
	} else if input.EnabledSubscriptions > 0 {
		checks = append(checks, HealthCheck{ID: "subscription_freshness", Severity: "ok", Title: "订阅获取正常", Detail: "所有已启用订阅最近一次获取成功。"})
	}

	// 6. Deployments whose runtime outcome was never confirmed.
	if input.UncertainDeployments > 0 {
		checks = append(checks, HealthCheck{
			ID: "deployment_certainty", Severity: "warn",
			Title:     "有发布结果尚未确认",
			Detail:    fmt.Sprintf("%d 次发布记录的运行结果未知，需要显式核验后才能判断真实状态。", input.UncertainDeployments),
			Deduction: healthWeightUncertainDeployment, Count: input.UncertainDeployments,
		})
	}

	// 7. Visibility: if most nodes have never been probed, availability cannot be judged.
	if len(input.Nodes) > 0 {
		ratio := float64(unprobed) / float64(len(input.Nodes))
		if ratio > majorityUnprobedRatio {
			checks = append(checks, HealthCheck{
				ID: "probe_coverage", Severity: "warn",
				Title:     "多数节点尚未探测",
				Detail:    fmt.Sprintf("%d/%d 个节点没有真实延迟样本，暂时无法判断可用性。", unprobed, len(input.Nodes)),
				Deduction: healthWeightMostlyUnprobed, Count: unprobed,
			})
		}
	}

	// 8. Rules that can never match because an earlier rule already covers them. This does not
	// block a publish, so it warns: the configuration is valid but may not do what was intended.
	if findings := input.RuleAnalysis.Conflicts; len(findings) > 0 {
		names := make([]string, 0, len(findings))
		for _, finding := range findings {
			names = append(names, finding.Name)
		}
		checks = append(checks, HealthCheck{
			ID: "rule_conflicts", Severity: "warn",
			Title:     "有规则因前序规则而不会命中",
			Detail:    fmt.Sprintf("%d 条规则受影响：%s。发布不会失败，但这些规则不会生效。", len(findings), joinLimited(names, 3)),
			Deduction: healthWeightRuleConflicts, Count: len(findings),
		})
	} else if input.RuleAnalysis.Checked > 0 {
		checks = append(checks, HealthCheck{
			ID: "rule_conflicts", Severity: "ok",
			Title:  "规则顺序无覆盖问题",
			Detail: "未发现被前序规则完全覆盖的规则。",
		})
	}

	score := 100
	blockers := 0
	warnings := 0
	for _, check := range checks {
		score -= check.Deduction
		switch check.Severity {
		case "block":
			blockers++
		case "warn":
			warnings++
		}
	}
	if score < 0 {
		score = 0
	}
	return Health{
		Score:      score,
		Grade:      healthGrade(score, blockers),
		BlockerCts: blockers,
		Warnings:   warnings,
		Checks:     checks,
	}
}

// healthGrade names the verdict. A blocker caps the grade regardless of the numeric score,
// because "cannot publish a valid config" outweighs any amount of partial credit.
func healthGrade(score, blockers int) string {
	if blockers > 0 {
		return "blocked"
	}
	switch {
	case score >= 100:
		return "healthy"
	case score >= 70:
		return "attention"
	default:
		return "degraded"
	}
}

// joinLimited renders at most `limit` names plus a remainder count, so a long list cannot
// flood the overview card.
func joinLimited(values []string, limit int) string {
	if len(values) == 0 {
		return ""
	}
	if len(values) <= limit {
		return joinComma(values)
	}
	return fmt.Sprintf("%s 等 %d 项", joinComma(values[:limit]), len(values))
}

func joinComma(values []string) string {
	out := ""
	for i, value := range values {
		if i > 0 {
			out += "、"
		}
		out += value
	}
	return out
}
