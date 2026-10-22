package config

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// RuntimeStatus reports what is actually known about the Mihomo runtime.
//
// The three fields are not interchangeable:
//   - Configured=false means no controller URL was supplied, so nothing can be claimed.
//   - Configured=true, Reachable=false means the controller did not answer; the state of
//     the running instance is unknown, not "down".
//   - Reachable=true carries the version the controller reported.
//
// Treating an unreachable controller as a failure would be a guess, so it is reported as
// unreachable with the transport error preserved.
type RuntimeStatus struct {
	Configured bool   `json:"configured"`
	Reachable  bool   `json:"reachable"`
	Version    string `json:"version"`
	CheckedAt  string `json:"checkedAt"`
	Error      string `json:"error,omitempty"`
}

// runtimeProbeTimeout bounds the controller call so an unreachable host cannot stall the
// overview page. It is shorter than the reload timeout because this is a read-only status
// check, not a deployment step.
const runtimeProbeTimeout = 3 * time.Second

// Runtime checks the controller version endpoint.
func (b *Builder) Runtime(ctx context.Context) RuntimeStatus {
	status := RuntimeStatus{Configured: strings.TrimSpace(b.ControllerURL) != "", CheckedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if !status.Configured {
		status.Error = "Mihomo controller URL is not configured"
		return status
	}
	probeCtx, cancel := context.WithTimeout(ctx, runtimeProbeTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(probeCtx, http.MethodGet, strings.TrimRight(b.ControllerURL, "/")+"/version", nil)
	if err != nil {
		status.Error = err.Error()
		return status
	}
	if b.ControllerSecret != "" {
		request.Header.Set("Authorization", "Bearer "+b.ControllerSecret)
	}
	response, err := (&http.Client{Timeout: runtimeProbeTimeout}).Do(request)
	if err != nil {
		status.Error = "Mihomo controller is unreachable: " + err.Error()
		return status
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		status.Error = fmt.Sprintf("Mihomo controller returned HTTP %d", response.StatusCode)
		return status
	}
	var payload struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		status.Error = "Mihomo controller returned an unreadable version payload: " + err.Error()
		return status
	}
	status.Reachable = true
	status.Version = payload.Version
	return status
}

// Overview is the single payload the dashboard needs. It is assembled server-side so the
// page does not issue six requests and so the counts are computed from one consistent read.
type Overview struct {
	Runtime          RuntimeStatus         `json:"runtime"`
	ActiveDeployment *DeploymentSummary    `json:"activeDeployment"`
	Deployments      []DeploymentSummary   `json:"deployments"`
	Health           Health                `json:"health"`
	Nodes            NodeSummary           `json:"nodes"`
	Counts           CountSummary          `json:"counts"`
	Subscriptions    []SubscriptionSummary `json:"subscriptions"`
	Config           ConfigPaths           `json:"config"`
	Auth             AuthSummary           `json:"auth"`
	// RuleConflicts lists rules an earlier rule makes unreachable. The health check only
	// summarises them, so the detail is exposed here as well.
	RuleConflicts RuleAnalysis `json:"ruleConflicts"`
	GeneratedAt   string       `json:"generatedAt"`
}

// DeploymentSummary is the projection of a deployment the dashboard shows.
type DeploymentSummary struct {
	ID                  string `json:"id"`
	Operation           string `json:"operation"`
	CandidateID         string `json:"candidateId"`
	CandidateSHA256     string `json:"candidateSha256"`
	PreviousCandidateID string `json:"previousCandidateId,omitempty"`
	Status              string `json:"status"`
	RuntimeMessage      string `json:"runtimeMessage"`
	StartedAt           string `json:"startedAt"`
}

// NodeSummary counts nodes by how much is actually known about them.
type NodeSummary struct {
	Total       int `json:"total"`
	Available   int `json:"available"`
	Unavailable int `json:"unavailable"`
	NotLoaded   int `json:"notLoaded"`
	Unmeasured  int `json:"unmeasured"`
	Stale       int `json:"stale"`
}

// CountSummary is the desired-state inventory size.
type CountSummary struct {
	Subscriptions        int `json:"subscriptions"`
	EnabledSubscriptions int `json:"enabledSubscriptions"`
	Terminals            int `json:"terminals"`
	ProxyGroups          int `json:"proxyGroups"`
	Rules                int `json:"rules"`
	EnabledRules         int `json:"enabledRules"`
	UncertainDeployments int `json:"uncertainDeployments"`
}

// SubscriptionSummary exposes fetch health without leaking the URL, which may embed a token.
type SubscriptionSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Enabled     bool   `json:"enabled"`
	LastStatus  string `json:"lastStatus"`
	LastFetchAt string `json:"lastFetchAt,omitempty"`
}

// ConfigPaths reports the paths the service actually uses, so the settings page can show
// real values instead of a hard-coded example.
type ConfigPaths struct {
	SnapshotDir      string `json:"snapshotDir"`
	ActiveConfigPath string `json:"activeConfigPath"`
	MihomoBinary     string `json:"mihomoBinary"`
	ControllerURL    string `json:"controllerUrl"`
}

// AuthSummary reports whether the panel requires a credential. The secret itself is never
// included.
type AuthSummary struct {
	Required bool `json:"required"`
}

// BuildOverview assembles the dashboard payload.
//
// activeConfigPath and authRequired are supplied by the caller because the builder does not
// own them; they are threaded through so the response reflects the running process rather
// than a guess.
func (b *Builder) BuildOverview(ctx context.Context, activeConfigPath string, authRequired bool) (Overview, error) {
	nodes, err := b.Nodes(ctx)
	if err != nil {
		return Overview{}, fmt.Errorf("read node inventory: %w", err)
	}
	groups, err := loadGroups(ctx, b.DB)
	if err != nil {
		return Overview{}, fmt.Errorf("read proxy groups: %w", err)
	}
	rules, err := loadRules(ctx, b.DB)
	if err != nil {
		return Overview{}, fmt.Errorf("read rules: %w", err)
	}

	overview := Overview{
		Runtime:       b.Runtime(ctx),
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339Nano),
		Config:        ConfigPaths{SnapshotDir: b.SnapshotDir, ActiveConfigPath: activeConfigPath, MihomoBinary: b.MihomoBinary, ControllerURL: b.ControllerURL},
		Auth:          AuthSummary{Required: authRequired},
		Deployments:   []DeploymentSummary{},
		Subscriptions: []SubscriptionSummary{},
	}

	// Subscriptions: never expose the URL, because providers embed access tokens in it.
	subscriptionRows, err := b.DB.QueryContext(ctx, `SELECT id,name,enabled,last_status,last_fetch_at FROM subscriptions ORDER BY name`)
	if err != nil {
		return Overview{}, fmt.Errorf("read subscriptions: %w", err)
	}
	healthSubscriptions := make([]SubscriptionHealth, 0)
	for subscriptionRows.Next() {
		var summary SubscriptionSummary
		var enabled int
		var fetched sql.NullString
		if err := subscriptionRows.Scan(&summary.ID, &summary.Name, &enabled, &summary.LastStatus, &fetched); err != nil {
			subscriptionRows.Close()
			return Overview{}, fmt.Errorf("scan subscription: %w", err)
		}
		summary.Enabled = enabled == 1
		summary.LastFetchAt = fetched.String
		overview.Subscriptions = append(overview.Subscriptions, summary)
		overview.Counts.Subscriptions++
		if summary.Enabled {
			overview.Counts.EnabledSubscriptions++
			healthSubscriptions = append(healthSubscriptions, SubscriptionHealth{Name: summary.Name, LastStatus: summary.LastStatus})
		}
	}
	if err := subscriptionRows.Err(); err != nil {
		subscriptionRows.Close()
		return Overview{}, fmt.Errorf("iterate subscriptions: %w", err)
	}
	if err := subscriptionRows.Close(); err != nil {
		return Overview{}, fmt.Errorf("close subscription rows: %w", err)
	}

	overview.Counts.ProxyGroups = len(groups)
	overview.Counts.Rules = len(rules)
	for _, rule := range rules {
		if rule.Enabled {
			overview.Counts.EnabledRules++
		}
	}
	if err := b.DB.QueryRowContext(ctx, `SELECT count(*) FROM terminals`).Scan(&overview.Counts.Terminals); err != nil {
		return Overview{}, fmt.Errorf("count terminals: %w", err)
	}
	// An uncertain deployment is one whose live effect was never confirmed.
	if err := b.DB.QueryRowContext(ctx, `SELECT count(*) FROM deployments WHERE status IN ('unknown','published_unverified','publishing')`).Scan(&overview.Counts.UncertainDeployments); err != nil {
		return Overview{}, fmt.Errorf("count uncertain deployments: %w", err)
	}

	// Node counts distinguish "unknown" from "unavailable"; collapsing them would let the
	// dashboard imply knowledge it does not have.
	for _, node := range nodes {
		overview.Nodes.Total++
		if node.ProbeStale {
			overview.Nodes.Stale++
		}
		switch node.Status {
		case "available":
			overview.Nodes.Available++
		case "unavailable":
			overview.Nodes.Unavailable++
		case "not_loaded":
			overview.Nodes.NotLoaded++
		default:
			overview.Nodes.Unmeasured++
		}
	}

	// Deployments, newest first. The most recent verified record is what "currently in
	// effect" means; a later unverified attempt does not replace it.
	deploymentRows, err := b.DB.QueryContext(ctx, `SELECT d.id,d.operation,d.candidate_id,d.status,d.runtime_message,d.started_at,d.previous_candidate_id,c.sha256
		FROM deployments d JOIN config_candidates c ON c.id=d.candidate_id ORDER BY d.started_at DESC, d.id DESC LIMIT 5`)
	if err != nil {
		return Overview{}, fmt.Errorf("read recent deployments: %w", err)
	}
	for deploymentRows.Next() {
		var summary DeploymentSummary
		var previous sql.NullString
		if err := deploymentRows.Scan(&summary.ID, &summary.Operation, &summary.CandidateID, &summary.Status, &summary.RuntimeMessage, &summary.StartedAt, &previous, &summary.CandidateSHA256); err != nil {
			deploymentRows.Close()
			return Overview{}, fmt.Errorf("scan deployment: %w", err)
		}
		summary.PreviousCandidateID = previous.String
		overview.Deployments = append(overview.Deployments, summary)
	}
	if err := deploymentRows.Err(); err != nil {
		deploymentRows.Close()
		return Overview{}, fmt.Errorf("iterate deployments: %w", err)
	}
	if err := deploymentRows.Close(); err != nil {
		return Overview{}, fmt.Errorf("close deployment rows: %w", err)
	}
	for _, deployment := range overview.Deployments {
		if deployment.Status == "verified" {
			active := deployment
			overview.ActiveDeployment = &active
			break
		}
	}

	// Health runs against the same snapshot the rest of the payload describes.
	healthGroups := make([]GroupHealth, 0, len(groups))
	for _, group := range groups {
		healthGroups = append(healthGroups, GroupHealth{
			ID: group.ID, Name: group.Name, SelectionMode: group.SelectionMode,
			MemberNodeIDs: group.MemberNodeIDs, MemberNodeNames: group.MemberNodeNames, MemberFilter: group.MemberFilter,
		})
	}
	healthRules := make([]RuleHealth, 0, len(rules))
	for _, rule := range rules {
		healthRules = append(healthRules, RuleHealth{ID: rule.ID, Name: rule.Name, Target: rule.Target, Enabled: rule.Enabled})
	}
	overview.Health = ComputeHealth(HealthInput{
		EnabledSubscriptions: overview.Counts.EnabledSubscriptions,
		Subscriptions:        healthSubscriptions,
		Nodes:                nodes,
		Groups:               healthGroups,
		Rules:                healthRules,
		// Computed from the same persisted rules the health checks already read, so the
		// dashboard and the rules page cannot disagree about which rules are unreachable.
		RuleAnalysis:         AnalyzeRules(rules),
		UncertainDeployments: overview.Counts.UncertainDeployments,
	})
	// Expose the findings directly too; the health check only summarises them.
	overview.RuleConflicts = AnalyzeRules(rules)
	return overview, nil
}
