package config

import (
	"fmt"
	"net"
	"sort"
	"strings"

	"mihomo-studio/internal/domain"
)

// Rule conflict analysis answers a question the operator cannot answer by reading a sorted
// list: which rules can never take effect?
//
// Mihomo evaluates rules top to bottom and stops at the first match, so a rule is dead when
// an earlier rule already matches everything it would match. The analysis below only reports
// a shadowing relationship when it is provable from the two rule types alone. GeoIP, GeoSite,
// process, and port rules are deliberately not compared: their match sets depend on external
// databases or on details not present here, so claiming one shadows another would be a guess.

// ConflictKind names why a rule cannot take effect.
type ConflictKind string

const (
	// ConflictDuplicate means an earlier rule has the same type, value, and target.
	ConflictDuplicate ConflictKind = "duplicate"
	// ConflictShadowed means an earlier rule matches a strict superset of this rule's traffic.
	ConflictShadowed ConflictKind = "shadowed"
	// ConflictCidrOverlap means two SRC-IP-CIDR routes cover overlapping sources with
	// different targets, so one of them wins by order rather than by intent.
	ConflictCidrOverlap ConflictKind = "cidr_overlap"
)

// RuleConflict is one finding, always naming both sides so the operator can act.
type RuleConflict struct {
	Kind ConflictKind `json:"kind"`
	// RuleID is the rule that cannot take effect (or the later of the overlapping pair).
	RuleID string `json:"ruleId"`
	Name   string `json:"name"`
	Detail string `json:"detail"`
	// ConflictsWith is the earlier rule responsible, when one exists.
	ConflictsWith string `json:"conflictsWith,omitempty"`
	// Severity is always `warn` here: a dead or overlapping rule still produces a valid,
	// publishable configuration. `block` is reserved for conditions that make the
	// configuration invalid, which the candidate builder catches separately.
	Severity string `json:"severity"`
}

// RuleAnalysis is the result of analysis, ordered for stable display.
type RuleAnalysis struct {
	Conflicts []RuleConflict `json:"conflicts"`
	// Checked is the number of enabled rules considered.
	Checked int `json:"checked"`
	// Limitations states what the analysis cannot determine, so the result is not overread.
	Limitations string `json:"limitations"`
}

const ruleAnalysisLimitations = "仅比较能从规则类型直接推断的遮蔽关系。GeoIP、GeoSite、PROCESS-*、端口与 RULE-SET 规则依赖外部数据，未做比较；MATCH 由系统固定放在末尾。"

// AnalyzeRules reports rules that cannot take effect because an earlier rule already matched
// their traffic, plus duplicate rules.
//
// Rules are evaluated in the order Mihomo receives them: ascending priority, then ascending
// ID, matching how the candidate builder sorts them.
func AnalyzeRules(rules []domain.Rule) RuleAnalysis {
	enabled := make([]domain.Rule, 0, len(rules))
	for _, item := range rules {
		if item.Enabled {
			enabled = append(enabled, item)
		}
	}
	sort.SliceStable(enabled, func(i, j int) bool {
		if enabled[i].Priority == enabled[j].Priority {
			return enabled[i].ID < enabled[j].ID
		}
		return enabled[i].Priority < enabled[j].Priority
	})

	conflicts := make([]RuleConflict, 0)
	reported := make(map[string]bool)
	add := func(conflict RuleConflict) {
		// One finding per (later rule, earlier rule) pair keeps the list actionable.
		key := conflict.RuleID + "\x00" + string(conflict.Kind) + "\x00" + conflict.ConflictsWith
		if reported[key] {
			return
		}
		reported[key] = true
		conflicts = append(conflicts, conflict)
	}

	for later := 0; later < len(enabled); later++ {
		for earlier := 0; earlier < later; earlier++ {
			reason, kind, ok := shadowingRelation(enabled[earlier], enabled[later])
			if !ok {
				continue
			}
			add(RuleConflict{
				Kind:          kind,
				RuleID:        enabled[later].ID,
				Name:          enabled[later].Name,
				ConflictsWith: enabled[earlier].ID,
				Severity:      "warn",
				Detail: fmt.Sprintf("前序规则「%s」%s，因此这条规则永远不会命中。",
					describeRule(enabled[earlier]), reason),
			})
		}
	}

	conflicts = append(conflicts, analyzeCidrOverlap(enabled)...)
	return RuleAnalysis{
		Conflicts:   conflicts,
		Checked:     len(enabled),
		Limitations: ruleAnalysisLimitations,
	}
}

// shadowingRelation reports whether `earlier` necessarily matches every request `later`
// would match, together with a human-readable explanation.
func shadowingRelation(earlier, later domain.Rule) (reason string, kind ConflictKind, ok bool) {
	earlierType := strings.ToUpper(earlier.RuleType)
	laterType := strings.ToUpper(later.RuleType)

	if earlierType == laterType && strings.EqualFold(earlier.RuleValue, later.RuleValue) {
		if earlier.Target == later.Target {
			return "与其类型、内容、目标完全相同", ConflictDuplicate, true
		}
		return fmt.Sprintf("已用相同条件匹配并交给「%s」", earlier.Target), ConflictShadowed, true
	}

	switch earlierType {
	case "DOMAIN-KEYWORD":
		keyword := strings.ToLower(earlier.RuleValue)
		if keyword == "" {
			return "", "", false
		}
		// Every domain-matching type matches only names that contain their value, so a
		// keyword earlier in the list covers any later rule whose value contains it.
		if laterType == "DOMAIN" || laterType == "DOMAIN-SUFFIX" || laterType == "DOMAIN-KEYWORD" {
			if strings.Contains(strings.ToLower(later.RuleValue), keyword) {
				return fmt.Sprintf("匹配所有含「%s」的域名", keyword), ConflictShadowed, true
			}
		}
	case "DOMAIN-SUFFIX":
		suffix := strings.ToLower(strings.TrimPrefix(earlier.RuleValue, "."))
		if suffix == "" {
			return "", "", false
		}
		// DOMAIN-SUFFIX covers the bare domain and every subdomain.
		if laterType == "DOMAIN" || laterType == "DOMAIN-SUFFIX" {
			value := strings.ToLower(strings.TrimPrefix(later.RuleValue, "."))
			if value == suffix || strings.HasSuffix(value, "."+suffix) {
				return fmt.Sprintf("覆盖「%s」及其所有子域", suffix), ConflictShadowed, true
			}
		}
	case "IP-CIDR", "IP-CIDR6":
		if laterType != "IP-CIDR" && laterType != "IP-CIDR6" {
			return "", "", false
		}
		earlierNetwork, earlierBits, err := parseCidr(earlier.RuleValue)
		if err != nil {
			return "", "", false
		}
		laterNetwork, laterBits, err := parseCidr(later.RuleValue)
		if err != nil {
			return "", "", false
		}
		// A network covers a later one when it is no narrower and contains both of its ends.
		if earlierBits <= laterBits && earlierNetwork.Contains(laterNetwork.IP) && earlierNetwork.Contains(lastAddress(laterNetwork)) {
			return fmt.Sprintf("覆盖整个「%s」网段", laterNetwork.String()), ConflictShadowed, true
		}
	}
	return "", "", false
}

// analyzeCidrOverlap reports SRC-IP-CIDR rules whose source ranges overlap but point at
// different targets. The builder already warns about this while composing, but surfacing it
// in the rules view lets the operator fix the intent rather than discover it at publish time.
func analyzeCidrOverlap(rules []domain.Rule) []RuleConflict {
	type route struct {
		rule    domain.Rule
		network *net.IPNet
	}
	routes := make([]route, 0)
	for _, item := range rules {
		if strings.ToUpper(item.RuleType) != "SRC-IP-CIDR" {
			continue
		}
		network, _, err := parseCidr(item.RuleValue)
		if err != nil {
			continue
		}
		routes = append(routes, route{rule: item, network: network})
	}
	conflicts := make([]RuleConflict, 0)
	for i := 0; i < len(routes); i++ {
		for j := i + 1; j < len(routes); j++ {
			a, b := routes[i], routes[j]
			if a.rule.Target == b.rule.Target {
				continue
			}
			if !a.network.Contains(b.network.IP) && !b.network.Contains(a.network.IP) {
				continue
			}
			conflicts = append(conflicts, RuleConflict{
				Kind:          ConflictCidrOverlap,
				RuleID:        b.rule.ID,
				Name:          b.rule.Name,
				ConflictsWith: a.rule.ID,
				Severity:      "warn",
				Detail: fmt.Sprintf("源网段「%s」与「%s」重叠但目标不同（%s / %s）。更精确的网段会先匹配，结果由顺序决定而不是由意图决定。",
					a.rule.RuleValue, b.rule.RuleValue, a.rule.Target, b.rule.Target),
			})
		}
	}
	return conflicts
}

func parseCidr(value string) (*net.IPNet, int, error) {
	_, network, err := net.ParseCIDR(strings.TrimSpace(value))
	if err != nil {
		return nil, 0, err
	}
	bits, _ := network.Mask.Size()
	return network, bits, nil
}

// lastAddress returns the highest address in a network, so containment can be checked by
// testing both ends rather than assuming the input is canonical.
func lastAddress(network *net.IPNet) net.IP {
	ip := network.IP.Mask(network.Mask)
	result := make(net.IP, len(ip))
	copy(result, ip)
	for i := range result {
		result[i] |= ^network.Mask[i]
	}
	return result
}

func describeRule(item domain.Rule) string {
	return item.RuleType + "," + item.RuleValue + " → " + item.Target
}
