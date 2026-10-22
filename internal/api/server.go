package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"mihomo-studio/internal/domain"
	"mihomo-studio/internal/store"
	"mihomo-studio/internal/workflow"
)

type Server struct {
	Store    *store.Store
	Workflow *workflow.Service
	Logger   *slog.Logger
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	servePrototype := func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, "web/prototype-v6.html") }
	mux.HandleFunc("GET /{$}", servePrototype)
	mux.HandleFunc("GET /prototype-v6.html", servePrototype)
	mux.HandleFunc("GET /api.js", func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, "web/api.js") })
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
	mux.HandleFunc("POST /api/v1/rules", s.saveRule)
	mux.HandleFunc("PUT /api/v1/rules/{id}", s.updateRule)
	mux.HandleFunc("DELETE /api/v1/rules/{id}", s.deleteRule)
	mux.HandleFunc("POST /api/v1/config-candidates", s.buildCandidate)
	mux.HandleFunc("GET /api/v1/config-candidates/{id}", s.getCandidate)
	mux.HandleFunc("GET /api/v1/config-candidates/{id}/inputs", s.getCandidateInputs)
	mux.HandleFunc("POST /api/v1/config-candidates/{id}/publish", s.publishCandidate)
	mux.HandleFunc("POST /api/v1/deployments/{id}/verify", s.verifyDeployment)
	mux.HandleFunc("POST /api/v1/deployments/{id}/rollback", s.rollbackDeployment)
	return requestLog(s.Logger, securityHeaders(sameOrigin(mux)))
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
