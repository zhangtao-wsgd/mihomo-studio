package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"mihomo-studio/internal/domain"
)

var ErrNotFound = errors.New("resource not found")
var ErrConflict = errors.New("resource is still referenced")

func now() time.Time                { return time.Now().UTC() }
func stamp(t time.Time) string      { return t.UTC().Format(time.RFC3339Nano) }
func parseStamp(s string) time.Time { t, _ := time.Parse(time.RFC3339Nano, s); return t }

var validIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func validID(id string) bool { return validIDPattern.MatchString(id) }

func (s *Store) ListSubscriptions(ctx context.Context) ([]domain.Subscription, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,name,url,enabled,last_fetch_at,last_status,created_at,updated_at FROM subscriptions ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []domain.Subscription{}
	for rows.Next() {
		var x domain.Subscription
		var enabled int
		var fetched sql.NullString
		var created, updated string
		if err := rows.Scan(&x.ID, &x.Name, &x.URL, &enabled, &fetched, &x.LastStatus, &created, &updated); err != nil {
			return nil, err
		}
		x.Enabled = enabled == 1
		x.CreatedAt = parseStamp(created)
		x.UpdatedAt = parseStamp(updated)
		if fetched.Valid {
			t := parseStamp(fetched.String)
			x.LastFetchAt = &t
		}
		items = append(items, x)
	}
	return items, rows.Err()
}
func (s *Store) SaveSubscription(ctx context.Context, x domain.Subscription) error {
	t := now()
	if !validID(x.ID) {
		return fmt.Errorf("id must contain only letters, digits, underscore, or hyphen (1-64 characters)")
	}
	if strings.TrimSpace(x.Name) == "" || strings.ContainsAny(x.Name, "\r\n") || strings.Contains(x.Name, " / ") || x.URL == "" {
		return fmt.Errorf("name and URL are required; name cannot contain line breaks or the reserved ' / ' delimiter")
	}
	parsedURL, parseErr := url.ParseRequestURI(x.URL)
	if parseErr != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Hostname() == "" {
		return fmt.Errorf("subscription URL must be an absolute HTTP(S) URL")
	}
	if x.CreatedAt.IsZero() {
		x.CreatedAt = t
	}
	x.UpdatedAt = t
	if x.LastStatus == "" {
		x.LastStatus = "never_fetched"
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO subscriptions(id,name,url,enabled,last_fetch_at,last_status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,url=excluded.url,enabled=excluded.enabled,updated_at=excluded.updated_at`, x.ID, x.Name, x.URL, boolInt(x.Enabled), nullableTime(x.LastFetchAt), x.LastStatus, stamp(x.CreatedAt), stamp(x.UpdatedAt))
	return err
}
func (s *Store) DeleteSubscription(ctx context.Context, id string) error {
	return deleteByID(ctx, s.DB, "subscriptions", id)
}

func (s *Store) ListTerminals(ctx context.Context) ([]domain.Terminal, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,name,identity_type,identity_value,proxy_group_id,enabled,notes,created_at,updated_at FROM terminals ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Terminal{}
	for rows.Next() {
		var x domain.Terminal
		var enabled int
		var c, u string
		if err := rows.Scan(&x.ID, &x.Name, &x.IdentityType, &x.IdentityValue, &x.ProxyGroupID, &enabled, &x.Notes, &c, &u); err != nil {
			return nil, err
		}
		x.Enabled = enabled == 1
		x.CreatedAt = parseStamp(c)
		x.UpdatedAt = parseStamp(u)
		out = append(out, x)
	}
	return out, rows.Err()
}
func (s *Store) SaveTerminal(ctx context.Context, x domain.Terminal) error {
	t := now()
	if !validID(x.ID) || strings.TrimSpace(x.Name) == "" || x.IdentityValue == "" || !validID(x.ProxyGroupID) {
		return fmt.Errorf("id, name, identityValue and proxyGroupId are required")
	}
	switch x.IdentityType {
	case "source_ip", "cidr", "mac", "manual":
	default:
		return fmt.Errorf("identityType must be source_ip, cidr, mac, or manual")
	}
	if x.CreatedAt.IsZero() {
		x.CreatedAt = t
	}
	x.UpdatedAt = t
	_, err := s.DB.ExecContext(ctx, `INSERT INTO terminals(id,name,identity_type,identity_value,proxy_group_id,enabled,notes,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,identity_type=excluded.identity_type,identity_value=excluded.identity_value,proxy_group_id=excluded.proxy_group_id,enabled=excluded.enabled,notes=excluded.notes,updated_at=excluded.updated_at`, x.ID, x.Name, x.IdentityType, x.IdentityValue, x.ProxyGroupID, boolInt(x.Enabled), x.Notes, stamp(x.CreatedAt), stamp(x.UpdatedAt))
	return err
}
func (s *Store) DeleteTerminal(ctx context.Context, id string) error {
	return deleteByID(ctx, s.DB, "terminals", id)
}

func (s *Store) ListProxyGroups(ctx context.Context) ([]domain.ProxyGroup, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,name,mihomo_type,selection_mode,member_node_names_json,member_filter_json,health_url,interval_seconds,tolerance_ms,created_at,updated_at,member_node_ids_json FROM proxy_groups ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.ProxyGroup{}
	for rows.Next() {
		var x domain.ProxyGroup
		var names, filter, c, u, nodeIDs string
		if err := rows.Scan(&x.ID, &x.Name, &x.MihomoType, &x.SelectionMode, &names, &filter, &x.HealthURL, &x.IntervalSeconds, &x.ToleranceMS, &c, &u, &nodeIDs); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(names), &x.MemberNodeNames); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(nodeIDs), &x.MemberNodeIDs); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(filter), &x.MemberFilter); err != nil {
			return nil, err
		}
		x.CreatedAt = parseStamp(c)
		x.UpdatedAt = parseStamp(u)
		out = append(out, x)
	}
	return out, rows.Err()
}
func (s *Store) SaveProxyGroup(ctx context.Context, x domain.ProxyGroup) error {
	t := now()
	if !validID(x.ID) || strings.TrimSpace(x.Name) == "" {
		return fmt.Errorf("id and name are required")
	}
	if strings.ContainsAny(x.Name, ",\r\n") || x.Name == "DIRECT" || x.Name == "REJECT" || x.Name == "MATCH" {
		return fmt.Errorf("proxy group name is invalid or reserved")
	}
	switch x.MihomoType {
	case "select", "url-test", "fallback", "load-balance":
	default:
		return fmt.Errorf("unsupported Mihomo proxy group type")
	}
	switch x.SelectionMode {
	case "manual", "dynamic":
	default:
		return fmt.Errorf("selectionMode must be manual or dynamic")
	}
	if x.IntervalSeconds < 1 || x.ToleranceMS < 0 {
		return fmt.Errorf("intervalSeconds must be positive and toleranceMs cannot be negative")
	}
	if x.HealthURL == "" {
		x.HealthURL = "https://www.gstatic.com/generate_204"
	}
	if x.MemberNodeNames == nil {
		x.MemberNodeNames = []string{}
	}
	if x.MemberNodeIDs == nil {
		x.MemberNodeIDs = []string{}
	}
	if x.MemberFilter == nil {
		x.MemberFilter = map[string]any{}
	}
	for key, value := range x.MemberFilter {
		switch key {
		case "keyword", "subscription", "region":
			if _, ok := value.(string); !ok {
				return fmt.Errorf("memberFilter.%s must be a string", key)
			}
		case "regex":
			pattern, ok := value.(string)
			if !ok {
				return fmt.Errorf("memberFilter.regex must be a string")
			}
			if pattern != "" {
				if _, err := regexp.Compile(pattern); err != nil {
					return fmt.Errorf("memberFilter.regex is invalid: %w", err)
				}
			}
		case "maxLatencyMs":
			return fmt.Errorf("memberFilter.maxLatencyMs requires live node latency samples and is not supported in this implementation slice")
		default:
			return fmt.Errorf("unsupported memberFilter field %q", key)
		}
	}
	seenNodeIDs := map[string]bool{}
	for _, nodeID := range x.MemberNodeIDs {
		if strings.TrimSpace(nodeID) == "" {
			return fmt.Errorf("member node IDs cannot be empty")
		}
		if seenNodeIDs[nodeID] {
			return fmt.Errorf("duplicate member node ID %q", nodeID)
		}
		seenNodeIDs[nodeID] = true
	}
	seenMembers := map[string]bool{}
	for _, member := range x.MemberNodeNames {
		if strings.TrimSpace(member) == "" {
			return fmt.Errorf("member node names cannot be empty")
		}
		if seenMembers[member] {
			return fmt.Errorf("duplicate member node %q", member)
		}
		seenMembers[member] = true
	}
	names, _ := json.Marshal(x.MemberNodeNames)
	nodeIDs, _ := json.Marshal(x.MemberNodeIDs)
	filter, _ := json.Marshal(x.MemberFilter)
	if x.CreatedAt.IsZero() {
		x.CreatedAt = t
	}
	x.UpdatedAt = t
	_, err := s.DB.ExecContext(ctx, `INSERT INTO proxy_groups(id,name,mihomo_type,selection_mode,member_node_names_json,member_filter_json,health_url,interval_seconds,tolerance_ms,created_at,updated_at,member_node_ids_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,mihomo_type=excluded.mihomo_type,selection_mode=excluded.selection_mode,member_node_names_json=excluded.member_node_names_json,member_filter_json=excluded.member_filter_json,health_url=excluded.health_url,interval_seconds=excluded.interval_seconds,tolerance_ms=excluded.tolerance_ms,updated_at=excluded.updated_at,member_node_ids_json=excluded.member_node_ids_json`, x.ID, x.Name, x.MihomoType, x.SelectionMode, string(names), string(filter), x.HealthURL, x.IntervalSeconds, x.ToleranceMS, stamp(x.CreatedAt), stamp(x.UpdatedAt), string(nodeIDs))
	return err
}
func (s *Store) DeleteProxyGroup(ctx context.Context, id string) error {
	var name string
	if err := s.DB.QueryRowContext(ctx, `SELECT name FROM proxy_groups WHERE id=?`, id).Scan(&name); err == sql.ErrNoRows {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	var terminalCount, ruleCount int
	if err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM terminals WHERE proxy_group_id=?`, id).Scan(&terminalCount); err != nil {
		return err
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM rules WHERE target=?`, id).Scan(&ruleCount); err != nil {
		return err
	}
	if terminalCount > 0 || ruleCount > 0 {
		return fmt.Errorf("%w: proxy group is used by %d terminal(s) and %d rule(s)", ErrConflict, terminalCount, ruleCount)
	}
	return deleteByID(ctx, s.DB, "proxy_groups", id)
}

func (s *Store) ListRules(ctx context.Context) ([]domain.Rule, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,name,rule_type,rule_value,target,priority,source,enabled,created_at,updated_at FROM rules ORDER BY priority,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Rule{}
	for rows.Next() {
		var x domain.Rule
		var enabled int
		var c, u string
		if err := rows.Scan(&x.ID, &x.Name, &x.RuleType, &x.RuleValue, &x.Target, &x.Priority, &x.Source, &enabled, &c, &u); err != nil {
			return nil, err
		}
		x.Enabled = enabled == 1
		x.CreatedAt = parseStamp(c)
		x.UpdatedAt = parseStamp(u)
		out = append(out, x)
	}
	return out, rows.Err()
}
func (s *Store) SaveRule(ctx context.Context, x domain.Rule) error {
	t := now()
	if !validID(x.ID) || strings.TrimSpace(x.Name) == "" || x.RuleType == "" || x.RuleValue == "" || x.Target == "" {
		return fmt.Errorf("id, name, ruleType, ruleValue and target are required")
	}
	allowedTypes := map[string]bool{"DOMAIN": true, "DOMAIN-SUFFIX": true, "DOMAIN-KEYWORD": true, "IP-CIDR": true, "IP-CIDR6": true, "GEOIP": true, "GEOSITE": true, "PROCESS-NAME": true, "PROCESS-PATH": true, "SRC-IP-CIDR": true, "DST-PORT": true, "SRC-PORT": true, "NETWORK": true, "RULE-SET": true}
	if !allowedTypes[x.RuleType] {
		return fmt.Errorf("unsupported ruleType %q", x.RuleType)
	}
	if strings.ContainsAny(x.RuleValue, ",\r\n") || strings.ContainsAny(x.Target, ",\r\n") {
		return fmt.Errorf("ruleValue and target cannot contain commas or line breaks")
	}
	switch x.Source {
	case "built_in", "custom", "external":
	default:
		return fmt.Errorf("source must be built_in, custom, or external")
	}
	if x.CreatedAt.IsZero() {
		x.CreatedAt = t
	}
	x.UpdatedAt = t
	_, err := s.DB.ExecContext(ctx, `INSERT INTO rules(id,name,rule_type,rule_value,target,priority,source,enabled,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,rule_type=excluded.rule_type,rule_value=excluded.rule_value,target=excluded.target,priority=excluded.priority,source=excluded.source,enabled=excluded.enabled,updated_at=excluded.updated_at`, x.ID, x.Name, x.RuleType, x.RuleValue, x.Target, x.Priority, x.Source, boolInt(x.Enabled), stamp(x.CreatedAt), stamp(x.UpdatedAt))
	return err
}
func (s *Store) DeleteRule(ctx context.Context, id string) error {
	return deleteByID(ctx, s.DB, "rules", id)
}

func deleteByID(ctx context.Context, db *sql.DB, table, id string) error {
	allowed := map[string]bool{"subscriptions": true, "terminals": true, "proxy_groups": true, "rules": true}
	if !allowed[table] {
		return fmt.Errorf("invalid table")
	}
	res, err := db.ExecContext(ctx, "DELETE FROM "+table+" WHERE id=?", id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func nullableTime(v *time.Time) any {
	if v == nil {
		return nil
	}
	return stamp(*v)
}
