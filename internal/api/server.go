package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"mihomo-studio/internal/config"
	"mihomo-studio/internal/domain"
	"mihomo-studio/internal/store"
	"mihomo-studio/internal/workflow"
)

type Server struct {
	Store    *store.Store
	Workflow *workflow.Service
	Logger   *slog.Logger
	Auth     AuthConfig
	// ActiveConfigPath is the runtime config file the service writes. It is reported by the
	// overview so the settings page shows the path actually in use.
	ActiveConfigPath string
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	servePrototype := func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, "web/prototype-v6.html") }
	mux.HandleFunc("GET /{$}", servePrototype)
	mux.HandleFunc("GET /prototype-v6.html", servePrototype)
	mux.HandleFunc("GET /api.js", func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, "web/api.js") })
	// Reports whether the panel currently requires a credential, so the client can tell
	// "wrong secret" apart from "no secret configured" without probing a protected route.
	mux.HandleFunc("GET /api/v1/auth/status", s.authStatus)
	// Confirms a supplied credential. It sits behind requireAuth on purpose: reaching the
	// handler at all proves the token was accepted.
	mux.HandleFunc("GET /api/v1/auth/verify", s.authVerify)
	mux.HandleFunc("GET /api/v1/subscriptions", s.listSubscriptions)
	mux.HandleFunc("POST /api/v1/subscriptions", s.saveSubscription)
	mux.HandleFunc("GET /api/v1/subscriptions/{id}", s.getSubscription)
	mux.HandleFunc("PUT /api/v1/subscriptions/{id}", s.updateSubscription)
	mux.HandleFunc("DELETE /api/v1/subscriptions/{id}", s.deleteSubscription)
	mux.HandleFunc("POST /api/v1/subscriptions/{id}/refresh", s.refreshSubscription)
	mux.HandleFunc("GET /api/v1/subscriptions/{id}/snapshots", s.subscriptionSnapshots)
	mux.HandleFunc("GET /api/v1/terminals", s.listTerminals)
	mux.HandleFunc("POST /api/v1/terminals", s.saveTerminal)
	mux.HandleFunc("PUT /api/v1/terminals/{id}", s.updateTerminal)
	mux.HandleFunc("DELETE /api/v1/terminals/{id}", s.deleteTerminal)
	mux.HandleFunc("GET /api/v1/nodes", s.listNodes)
	mux.HandleFunc("POST /api/v1/nodes/probe", s.probeNodes)
	mux.HandleFunc("GET /api/v1/proxy-groups", s.listProxyGroups)
	mux.HandleFunc("POST /api/v1/proxy-groups", s.saveProxyGroup)
	mux.HandleFunc("PUT /api/v1/proxy-groups/{id}", s.updateProxyGroup)
	mux.HandleFunc("DELETE /api/v1/proxy-groups/{id}", s.deleteProxyGroup)
	mux.HandleFunc("GET /api/v1/proxy-groups/runtime-check", s.verifyRuntimeProxyGroups)
	mux.HandleFunc("GET /api/v1/rules", s.listRules)
	mux.HandleFunc("GET /api/v1/rules/conflicts", s.ruleConflicts)
	mux.HandleFunc("POST /api/v1/rules", s.saveRule)
	mux.HandleFunc("PUT /api/v1/rules/{id}", s.updateRule)
	mux.HandleFunc("DELETE /api/v1/rules/{id}", s.deleteRule)
	mux.HandleFunc("POST /api/v1/config-candidates", s.buildCandidate)
	mux.HandleFunc("GET /api/v1/config-candidates/{id}", s.getCandidate)
	mux.HandleFunc("GET /api/v1/config-candidates/{id}/inputs", s.getCandidateInputs)
	mux.HandleFunc("GET /api/v1/config-candidates/{id}/content", s.getCandidateContent)
	mux.HandleFunc("POST /api/v1/config-candidates/{id}/publish", s.publishCandidate)
	mux.HandleFunc("GET /api/v1/deployments", s.listDeployments)
	mux.HandleFunc("GET /api/v1/deployments/{id}", s.getDeployment)
	mux.HandleFunc("GET /api/v1/deployments/{id}/events", s.getDeploymentEvents)
	mux.HandleFunc("POST /api/v1/deployments/{id}/verify", s.verifyDeployment)
	mux.HandleFunc("POST /api/v1/deployments/{id}/rollback", s.rollbackDeployment)
	mux.HandleFunc("GET /api/v1/audit-events", s.listAuditEvents)
	mux.HandleFunc("GET /api/v1/overview", s.getOverview)
	mux.HandleFunc("GET /api/v1/runtime", s.getRuntime)
	return requestLog(s.Logger, securityHeaders(requireAuth(s.Auth, sameOrigin(mux))))
}

// getOverview returns the dashboard payload assembled from persisted state.
func (s *Server) getOverview(w http.ResponseWriter, r *http.Request) {
	if s.Workflow == nil || s.Workflow.Builder == nil {
		fail(w, 503, errors.New("overview is unavailable"))
		return
	}
	overview, err := s.Workflow.Builder.BuildOverview(r.Context(), s.ActiveConfigPath, s.Auth.Enabled())
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, 200, overview)
}

// getRuntime reports controller reachability. An unreachable controller is reported as
// unreachable rather than as a failure, because the running state is then simply unknown.
func (s *Server) getRuntime(w http.ResponseWriter, r *http.Request) {
	if s.Workflow == nil || s.Workflow.Builder == nil {
		fail(w, 503, errors.New("runtime status is unavailable"))
		return
	}
	writeJSON(w, 200, s.Workflow.Builder.Runtime(r.Context()))
}
func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"required": s.Auth.Enabled(),
		// Stating the header name lets the client set it without hard-coding a constant.
		"header": adminTokenHeader,
		"reason": authStatusReason(s.Auth),
	})
}

// authVerify confirms the presented credential. requireAuth already rejected anything
// invalid, so a response here means the token is accepted.
func (s *Server) authVerify(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"valid": true})
}

// authStatusReason explains why a credential is or is not required, using the same
// conditions that produced the decision in main so the two cannot drift apart in wording.
func authStatusReason(config AuthConfig) string {
	if config.Enabled() {
		return "MIHOMO_SECRET is set, so this panel requires the same secret that protects the Mihomo controller."
	}
	if config.AllowUnauthenticated {
		return "No secret is configured and the listener is loopback-only, so requests from this host are not authenticated."
	}
	return "No secret is configured; only loopback listeners may run without one."
}
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"status": "ok", "service": "mihomo-studio", "time": time.Now().UTC()})
}
func (s *Server) listSubscriptions(w http.ResponseWriter, r *http.Request) {
	x, e := s.Store.ListSubscriptions(r.Context())
	respond(w, x, e)
}
func (s *Server) saveSubscription(w http.ResponseWriter, r *http.Request) {
	var x domain.Subscription
	if err := decode(r, &x); err != nil {
		fail(w, 400, err)
		return
	}
	if x.ID == "" {
		x.ID = makeID()
	}
	if err := s.Store.SaveSubscription(r.Context(), x); err != nil {
		fail(w, 400, err)
		return
	}
	writeJSON(w, 201, x)
}
func (s *Server) getSubscription(w http.ResponseWriter, r *http.Request) {
	items, e := s.Store.ListSubscriptions(r.Context())
	if e != nil {
		fail(w, 500, e)
		return
	}
	for _, x := range items {
		if x.ID == r.PathValue("id") {
			writeJSON(w, 200, x)
			return
		}
	}
	fail(w, 404, store.ErrNotFound)
}
func (s *Server) updateSubscription(w http.ResponseWriter, r *http.Request) {
	var x domain.Subscription
	if err := decode(r, &x); err != nil {
		fail(w, 400, err)
		return
	}
	x.ID = r.PathValue("id")
	if err := s.Store.SaveSubscription(r.Context(), x); err != nil {
		fail(w, 400, err)
		return
	}
	writeJSON(w, 200, x)
}
func (s *Server) refreshSubscription(w http.ResponseWriter, r *http.Request) {
	if s.Workflow == nil || s.Workflow.Builder == nil {
		fail(w, 503, errors.New("subscription refresh is unavailable"))
		return
	}
	result, err := s.Workflow.Builder.RefreshSubscription(r.Context(), r.PathValue("id"))
	if err != nil {
		status := 422
		if strings.Contains(err.Error(), "not found") {
			status = 404
		}
		fail(w, status, err)
		return
	}
	writeJSON(w, 200, result)
}
func (s *Server) subscriptionSnapshots(w http.ResponseWriter, r *http.Request) {
	if s.Workflow == nil || s.Workflow.Builder == nil {
		fail(w, 503, errors.New("snapshot history is unavailable"))
		return
	}
	items, err := s.Workflow.Builder.SubscriptionSnapshotHistory(r.Context(), r.PathValue("id"))
	if err != nil {
		status := 500
		if strings.Contains(err.Error(), "not found") {
			status = 404
		}
		fail(w, status, err)
		return
	}
	writeJSON(w, 200, items)
}
func (s *Server) deleteSubscription(w http.ResponseWriter, r *http.Request) {
	err := s.Store.DeleteSubscription(r.Context(), r.PathValue("id"))
	if err != nil {
		status := 500
		if errors.Is(err, store.ErrNotFound) {
			status = 404
		}
		if errors.Is(err, store.ErrConflict) {
			status = 409
		}
		fail(w, status, err)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) listTerminals(w http.ResponseWriter, r *http.Request) {
	x, e := s.Store.ListTerminals(r.Context())
	respond(w, x, e)
}
func (s *Server) saveTerminal(w http.ResponseWriter, r *http.Request) {
	var x domain.Terminal
	if err := decode(r, &x); err != nil {
		fail(w, 400, err)
		return
	}
	if x.ID == "" {
		x.ID = makeID()
	}
	if err := validateTerminalIdentity(x); err != nil {
		fail(w, 400, err)
		return
	}
	if err := s.validateTerminalGroup(r.Context(), x.ProxyGroupID); err != nil {
		fail(w, 400, err)
		return
	}
	if err := s.Store.SaveTerminal(r.Context(), x); err != nil {
		fail(w, 400, err)
		return
	}
	writeJSON(w, 201, x)
}
func (s *Server) updateTerminal(w http.ResponseWriter, r *http.Request) {
	var x domain.Terminal
	if err := decode(r, &x); err != nil {
		fail(w, 400, err)
		return
	}
	x.ID = r.PathValue("id")
	if err := validateTerminalIdentity(x); err != nil {
		fail(w, 400, err)
		return
	}
	if err := s.validateTerminalGroup(r.Context(), x.ProxyGroupID); err != nil {
		fail(w, 400, err)
		return
	}
	if err := s.Store.SaveTerminal(r.Context(), x); err != nil {
		fail(w, 400, err)
		return
	}
	writeJSON(w, 200, x)
}
func (s *Server) validateTerminalGroup(ctx context.Context, id string) error {
	groups, err := s.Store.ListProxyGroups(ctx)
	if err != nil {
		return err
	}
	for _, g := range groups {
		if g.ID == id {
			return nil
		}
	}
	return fmt.Errorf("terminal default proxy group must reference an existing proxy group ID")
}
func (s *Server) deleteTerminal(w http.ResponseWriter, r *http.Request) {
	err := s.Store.DeleteTerminal(r.Context(), r.PathValue("id"))
	deleteResult(w, err)
}
func (s *Server) listNodes(w http.ResponseWriter, r *http.Request) {
	if s.Workflow == nil || s.Workflow.Builder == nil {
		fail(w, 503, errors.New("node inventory is unavailable"))
		return
	}
	x, err := s.Workflow.Builder.Nodes(r.Context())
	respond(w, x, err)
}
func (s *Server) probeNodes(w http.ResponseWriter, r *http.Request) {
	if s.Workflow == nil || s.Workflow.Builder == nil {
		fail(w, 503, errors.New("node probing is unavailable"))
		return
	}
	result, err := s.Workflow.Builder.ProbeNodes(r.Context())
	if err != nil {
		status := 502
		if strings.Contains(err.Error(), "not configured") {
			status = 503
		}
		fail(w, status, err)
		return
	}
	writeJSON(w, 200, result)
}
func (s *Server) listProxyGroups(w http.ResponseWriter, r *http.Request) {
	x, e := s.Store.ListProxyGroups(r.Context())
	respond(w, x, e)
}
func (s *Server) verifyRuntimeProxyGroups(w http.ResponseWriter, r *http.Request) {
	if s.Workflow == nil || s.Workflow.Builder == nil {
		fail(w, 503, errors.New("runtime group verification is unavailable"))
		return
	}
	result, err := s.Workflow.Builder.VerifyRuntimeProxyGroups(r.Context())
	if err != nil {
		status := 502
		if strings.Contains(err.Error(), "not configured") {
			status = 503
		}
		fail(w, status, err)
		return
	}
	writeJSON(w, 200, result)
}
func (s *Server) saveProxyGroup(w http.ResponseWriter, r *http.Request) {
	var x domain.ProxyGroup
	if err := decode(r, &x); err != nil {
		fail(w, 400, err)
		return
	}
	if x.ID == "" {
		x.ID = makeID()
	}
	if err := s.Store.SaveProxyGroup(r.Context(), x); err != nil {
		fail(w, 400, err)
		return
	}
	writeJSON(w, 201, x)
}
func (s *Server) updateProxyGroup(w http.ResponseWriter, r *http.Request) {
	var x domain.ProxyGroup
	if err := decode(r, &x); err != nil {
		fail(w, 400, err)
		return
	}
	x.ID = r.PathValue("id")
	if err := s.Store.SaveProxyGroup(r.Context(), x); err != nil {
		fail(w, 400, err)
		return
	}
	writeJSON(w, 200, x)
}
func (s *Server) deleteProxyGroup(w http.ResponseWriter, r *http.Request) {
	err := s.Store.DeleteProxyGroup(r.Context(), r.PathValue("id"))
	deleteResult(w, err)
}
func (s *Server) listRules(w http.ResponseWriter, r *http.Request) {
	x, e := s.Store.ListRules(r.Context())
	respond(w, x, e)
}

// ruleConflicts reports rules that an earlier rule makes unreachable, plus overlapping
// source routes. It is read-only: the operator decides whether to reorder, retarget, or
// delete. Nothing is rewritten automatically.
func (s *Server) ruleConflicts(w http.ResponseWriter, r *http.Request) {
	rules, err := s.Store.ListRules(r.Context())
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, 200, config.AnalyzeRules(rules))
}
func (s *Server) saveRule(w http.ResponseWriter, r *http.Request) {
	var x domain.Rule
	if err := decode(r, &x); err != nil {
		fail(w, 400, err)
		return
	}
	if x.ID == "" {
		x.ID = makeID()
	}
	if err := s.validateRuleTarget(r.Context(), x.Target); err != nil {
		fail(w, 400, err)
		return
	}
	if err := s.Store.SaveRule(r.Context(), x); err != nil {
		fail(w, 400, err)
		return
	}
	writeJSON(w, 201, x)
}
func (s *Server) updateRule(w http.ResponseWriter, r *http.Request) {
	var x domain.Rule
	if err := decode(r, &x); err != nil {
		fail(w, 400, err)
		return
	}
	x.ID = r.PathValue("id")
	if err := s.validateRuleTarget(r.Context(), x.Target); err != nil {
		fail(w, 400, err)
		return
	}
	if err := s.Store.SaveRule(r.Context(), x); err != nil {
		fail(w, 400, err)
		return
	}
	writeJSON(w, 200, x)
}
func (s *Server) deleteRule(w http.ResponseWriter, r *http.Request) {
	err := s.Store.DeleteRule(r.Context(), r.PathValue("id"))
	deleteResult(w, err)
}
func (s *Server) buildCandidate(w http.ResponseWriter, r *http.Request) {
	c, err := s.Workflow.Build(r.Context())
	if err != nil {
		fail(w, 422, err)
		return
	}
	writeJSON(w, 201, c)
}
func (s *Server) getCandidate(w http.ResponseWriter, r *http.Request) {
	c, err := s.Workflow.Candidate(r.Context(), r.PathValue("id"))
	if err != nil {
		status := 500
		if strings.Contains(err.Error(), "not found") {
			status = 404
		}
		fail(w, status, err)
		return
	}
	writeJSON(w, 200, c)
}
func (s *Server) getCandidateInputs(w http.ResponseWriter, r *http.Request) {
	inputs, err := s.Workflow.CandidateInputs(r.Context(), r.PathValue("id"))
	if err != nil {
		status := 500
		if strings.Contains(err.Error(), "not found") {
			status = 404
		}
		fail(w, status, err)
		return
	}
	writeJSON(w, 200, inputs)
}

// getCandidateContent returns the exact YAML bytes of an immutable candidate.
//
// The operator needs to see what will actually be written to the runtime before confirming a
// publish; without this the only way to inspect a candidate would be the filesystem. The
// response is the raw document, and its hash is compared against the recorded SHA-256 so a
// tampered or truncated snapshot is reported rather than displayed as authoritative.
func (s *Server) getCandidateContent(w http.ResponseWriter, r *http.Request) {
	candidate, err := s.Workflow.Candidate(r.Context(), r.PathValue("id"))
	if err != nil {
		status := 500
		if strings.Contains(err.Error(), "not found") {
			status = 404
		}
		fail(w, status, err)
		return
	}
	body, err := os.ReadFile(candidate.SnapshotPath)
	if err != nil {
		fail(w, 500, fmt.Errorf("read candidate snapshot: %w", err))
		return
	}
	digest := sha256.Sum256(body)
	actual := hex.EncodeToString(digest[:])
	if actual != candidate.SHA256 {
		// Refuse to present unverified content as the candidate; the mismatch is the finding.
		fail(w, 500, fmt.Errorf("candidate snapshot hash mismatch: recorded %s, actual %s", candidate.SHA256, actual))
		return
	}
	writeJSON(w, 200, map[string]any{
		"id":                candidate.ID,
		"sha256":            candidate.SHA256,
		"status":            candidate.Status,
		"validationMessage": candidate.ValidationMessage,
		"content":           string(body),
	})
}

func (s *Server) publishCandidate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Confirm bool `json:"confirm"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, 400, err)
		return
	}
	d, err := s.Workflow.Publish(r.Context(), r.PathValue("id"), body.Confirm)
	if err != nil {
		if d.ID != "" {
			writeJSON(w, 202, d)
			return
		}
		fail(w, 409, err)
		return
	}
	writeJSON(w, 202, d)
}
func (s *Server) verifyDeployment(w http.ResponseWriter, r *http.Request) {
	d, err := s.Workflow.Verify(r.Context(), r.PathValue("id"))
	if err != nil {
		if d.ID != "" {
			writeJSON(w, 202, d)
			return
		}
		fail(w, 409, err)
		return
	}
	writeJSON(w, 200, d)
}
func (s *Server) rollbackDeployment(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Confirm bool `json:"confirm"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, 400, err)
		return
	}
	d, err := s.Workflow.Rollback(r.Context(), r.PathValue("id"), body.Confirm)
	if err != nil {
		if d.ID != "" {
			writeJSON(w, 202, d)
			return
		}
		fail(w, 409, err)
		return
	}
	writeJSON(w, 202, d)
}
func (s *Server) listDeployments(w http.ResponseWriter, r *http.Request) {
	limit, err := intQuery(r, "limit", defaultDeploymentPageSize)
	if err != nil {
		fail(w, 400, err)
		return
	}
	offset, err := intQuery(r, "offset", 0)
	if err != nil {
		fail(w, 400, err)
		return
	}
	filter := store.DeploymentFilter{
		Limit:       limit,
		Offset:      offset,
		Status:      r.URL.Query().Get("status"),
		Operation:   r.URL.Query().Get("operation"),
		CandidateID: r.URL.Query().Get("candidateId"),
	}
	items, total, err := s.Store.ListDeployments(r.Context(), filter)
	if err != nil {
		status := 500
		if errors.Is(err, store.ErrInvalidDeploymentFilter) {
			status = 400
		}
		fail(w, status, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": total, "limit": filter.Limit, "offset": filter.Offset})
}

func (s *Server) getDeployment(w http.ResponseWriter, r *http.Request) {
	record, err := s.Store.Deployment(r.Context(), r.PathValue("id"))
	if err != nil {
		status := 500
		if errors.Is(err, store.ErrNotFound) {
			status = 404
		}
		fail(w, status, err)
		return
	}
	writeJSON(w, 200, record)
}

// getDeploymentEvents exposes the audit trail of one deployment. An unknown deployment is
// a 404 rather than an empty list, so a typo is distinguishable from a deployment that
// genuinely recorded no events.
func (s *Server) getDeploymentEvents(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Store.Deployment(r.Context(), r.PathValue("id")); err != nil {
		status := 500
		if errors.Is(err, store.ErrNotFound) {
			status = 404
		}
		fail(w, status, err)
		return
	}
	events, err := s.Store.DeploymentEvents(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": events})
}

func (s *Server) listAuditEvents(w http.ResponseWriter, r *http.Request) {
	limit, err := intQuery(r, "limit", defaultDeploymentPageSize)
	if err != nil {
		fail(w, 400, err)
		return
	}
	offset, err := intQuery(r, "offset", 0)
	if err != nil {
		fail(w, 400, err)
		return
	}
	events, err := s.Store.AuditEvents(r.Context(), limit, offset)
	if err != nil {
		status := 500
		if errors.Is(err, store.ErrInvalidDeploymentFilter) {
			status = 400
		}
		fail(w, status, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": events, "limit": limit, "offset": offset})
}

// defaultDeploymentPageSize keeps history responses bounded when the caller omits a limit.
const defaultDeploymentPageSize = 20

// intQuery reads an optional integer query parameter without silently coercing garbage to
// zero, which would turn a client mistake into a plausible-looking empty result.
func intQuery(r *http.Request, name string, fallback int) (int, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	if value < 0 {
		return 0, fmt.Errorf("%s cannot be negative", name)
	}
	return value, nil
}

func decode(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid request body: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("request body must contain exactly one JSON value")
		}
		return fmt.Errorf("invalid trailing request data: %w", err)
	}
	return nil
}
func respond(w http.ResponseWriter, v any, err error) {
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, 200, v)
}
func deleteResult(w http.ResponseWriter, err error) {
	if err != nil {
		status := 500
		if errors.Is(err, store.ErrNotFound) {
			status = 404
		}
		if errors.Is(err, store.ErrConflict) {
			status = 409
		}
		fail(w, status, err)
		return
	}
	w.WriteHeader(204)
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"error": err.Error()})
}
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}
func sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			u, err := url.Parse(origin)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || !strings.EqualFold(u.Host, r.Host) {
				fail(w, http.StatusForbidden, fmt.Errorf("cross-origin requests are not allowed"))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func requestLog(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if logger != nil {
			logger.Info("http request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(start))
		}
	})
}
func makeID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err == nil {
		return hex.EncodeToString(value[:])
	}
	return fmt.Sprintf("%x", time.Now().UTC().UnixNano())
}

func (s *Server) validateRuleTarget(ctx context.Context, target string) error {
	if target == "DIRECT" || target == "REJECT" {
		return nil
	}
	groups, err := s.Store.ListProxyGroups(ctx)
	if err != nil {
		return err
	}
	for _, group := range groups {
		if group.ID == target {
			return nil
		}
	}
	return fmt.Errorf("rule target must be DIRECT, REJECT, or an existing proxy group ID")
}

func validateTerminalIdentity(x domain.Terminal) error {
	switch x.IdentityType {
	case "source_ip":
		if net.ParseIP(x.IdentityValue) == nil {
			return fmt.Errorf("identityValue must be a valid IPv4 or IPv6 address")
		}
	case "cidr":
		if _, _, err := net.ParseCIDR(x.IdentityValue); err != nil {
			return fmt.Errorf("identityValue must be a valid CIDR range")
		}
	case "mac":
		if _, err := net.ParseMAC(x.IdentityValue); err != nil {
			return fmt.Errorf("identityValue must be a valid MAC address")
		}
	case "manual":
		if strings.TrimSpace(x.IdentityValue) == "" {
			return fmt.Errorf("identityValue is required")
		}
	default:
		return fmt.Errorf("unsupported identityType")
	}
	return nil
}
