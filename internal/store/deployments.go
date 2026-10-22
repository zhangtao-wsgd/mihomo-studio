package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"mihomo-studio/internal/domain"
)

// DeploymentFilter narrows a deployment history query. Zero values mean "no filter",
// which is why Limit must be normalized by the caller rather than defaulted here.
type DeploymentFilter struct {
	Limit       int
	Offset      int
	Status      string
	Operation   string
	CandidateID string
}

// maxDeploymentLimit bounds a single page so one request cannot load unbounded history.
const maxDeploymentLimit = 100

// DeploymentRecord is a deployment joined with the identity of the candidate it deployed,
// so history can show a version identifier without a second round trip per row.
type DeploymentRecord struct {
	domain.Deployment
	CandidateSHA256 string `json:"candidateSha256"`
}

var ErrInvalidDeploymentFilter = errors.New("invalid deployment filter")

func (f DeploymentFilter) normalized() (DeploymentFilter, error) {
	if f.Limit < 1 || f.Limit > maxDeploymentLimit {
		return f, fmt.Errorf("%w: limit must be between 1 and %d", ErrInvalidDeploymentFilter, maxDeploymentLimit)
	}
	if f.Offset < 0 {
		return f, fmt.Errorf("%w: offset cannot be negative", ErrInvalidDeploymentFilter)
	}
	if f.Status != "" && !validDeploymentStatus(f.Status) {
		return f, fmt.Errorf("%w: unknown status %q", ErrInvalidDeploymentFilter, f.Status)
	}
	if f.Operation != "" && f.Operation != "publish" && f.Operation != "rollback" {
		return f, fmt.Errorf("%w: operation must be publish or rollback", ErrInvalidDeploymentFilter)
	}
	return f, nil
}

// validDeploymentStatus mirrors the CHECK constraint on the deployments table so that an
// invalid filter is rejected as a bad request instead of silently returning no rows.
func validDeploymentStatus(status string) bool {
	switch status {
	case "prepared", "publishing", "published_unverified", "verified", "failed", "unknown", "rolled_back":
		return true
	}
	return false
}

// ListDeployments returns one page of deployment history, newest first, plus the total
// number of rows matching the filter (ignoring pagination).
func (s *Store) ListDeployments(ctx context.Context, filter DeploymentFilter) ([]DeploymentRecord, int, error) {
	filter, err := filter.normalized()
	if err != nil {
		return nil, 0, err
	}

	conditions := make([]string, 0, 3)
	args := make([]any, 0, 3)
	if filter.Status != "" {
		conditions = append(conditions, "d.status=?")
		args = append(args, filter.Status)
	}
	if filter.Operation != "" {
		conditions = append(conditions, "d.operation=?")
		args = append(args, filter.Operation)
	}
	if filter.CandidateID != "" {
		conditions = append(conditions, "d.candidate_id=?")
		args = append(args, filter.CandidateID)
	}
	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}

	var total int
	countQuery := `SELECT count(*) FROM deployments d` + where
	if err := s.DB.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count deployments: %w", err)
	}

	// started_at is an RFC3339Nano string, so lexical DESC ordering is chronological.
	// id is the tiebreaker so pagination cannot repeat or skip a row.
	listQuery := `SELECT d.id,d.operation,d.candidate_id,d.previous_candidate_id,d.status,d.runtime_message,d.started_at,d.finished_at,c.sha256
		FROM deployments d JOIN config_candidates c ON c.id=d.candidate_id` + where + `
		ORDER BY d.started_at DESC, d.id DESC LIMIT ? OFFSET ?`
	listArgs := append(append([]any{}, args...), filter.Limit, filter.Offset)

	rows, err := s.DB.QueryContext(ctx, listQuery, listArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list deployments: %w", err)
	}
	// Drain and close before returning: the pool holds a single connection, so leaving the
	// iterator open would block every later query.
	records := []DeploymentRecord{}
	for rows.Next() {
		var record DeploymentRecord
		var previous, finished sql.NullString
		var started string
		if err := rows.Scan(&record.ID, &record.Operation, &record.CandidateID, &previous, &record.Status, &record.RuntimeMessage, &started, &finished, &record.CandidateSHA256); err != nil {
			rows.Close()
			return nil, 0, fmt.Errorf("scan deployment: %w", err)
		}
		record.PreviousCandidateID = previous.String
		record.StartedAt = parseStamp(started)
		if finished.Valid {
			parsed := parseStamp(finished.String)
			record.FinishedAt = &parsed
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, 0, fmt.Errorf("iterate deployments: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, 0, fmt.Errorf("close deployment rows: %w", err)
	}
	return records, total, nil
}

// Deployment returns one deployment with its candidate identity.
func (s *Store) Deployment(ctx context.Context, id string) (DeploymentRecord, error) {
	var record DeploymentRecord
	var previous, finished sql.NullString
	var started string
	err := s.DB.QueryRowContext(ctx, `SELECT d.id,d.operation,d.candidate_id,d.previous_candidate_id,d.status,d.runtime_message,d.started_at,d.finished_at,c.sha256
		FROM deployments d JOIN config_candidates c ON c.id=d.candidate_id WHERE d.id=?`, id).
		Scan(&record.ID, &record.Operation, &record.CandidateID, &previous, &record.Status, &record.RuntimeMessage, &started, &finished, &record.CandidateSHA256)
	if errors.Is(err, sql.ErrNoRows) {
		return DeploymentRecord{}, ErrNotFound
	}
	if err != nil {
		return DeploymentRecord{}, fmt.Errorf("read deployment: %w", err)
	}
	record.PreviousCandidateID = previous.String
	record.StartedAt = parseStamp(started)
	if finished.Valid {
		parsed := parseStamp(finished.String)
		record.FinishedAt = &parsed
	}
	return record, nil
}

// AuditEvent is one recorded lifecycle or audit transition.
type AuditEvent struct {
	ID           string    `json:"id"`
	Action       string    `json:"action"`
	ResourceType string    `json:"resourceType"`
	ResourceID   string    `json:"resourceId"`
	Outcome      string    `json:"outcome"`
	DetailJSON   string    `json:"detailJson"`
	CreatedAt    time.Time `json:"createdAt"`
}

// DeploymentEvents returns the audit trail for one deployment, oldest first, because the
// reader wants the order in which outcomes were recorded.
func (s *Store) DeploymentEvents(ctx context.Context, deploymentID string) ([]AuditEvent, error) {
	return s.auditEvents(ctx, `resource_type='deployment' AND resource_id=?`, []any{deploymentID}, false)
}

// AuditEvents returns recent audit events across all resources, newest first.
func (s *Store) AuditEvents(ctx context.Context, limit, offset int) ([]AuditEvent, error) {
	if limit < 1 || limit > maxDeploymentLimit {
		return nil, fmt.Errorf("%w: limit must be between 1 and %d", ErrInvalidDeploymentFilter, maxDeploymentLimit)
	}
	if offset < 0 {
		return nil, fmt.Errorf("%w: offset cannot be negative", ErrInvalidDeploymentFilter)
	}
	return s.auditEvents(ctx, "", nil, true, limit, offset)
}

func (s *Store) auditEvents(ctx context.Context, where string, args []any, newestFirst bool, pagination ...int) ([]AuditEvent, error) {
	query := `SELECT id,action,resource_type,resource_id,outcome,detail_json,created_at FROM audit_events`
	if where != "" {
		query += " WHERE " + where
	}
	order := " ORDER BY created_at ASC, id ASC"
	if newestFirst {
		order = " ORDER BY created_at DESC, id DESC"
	}
	query += order
	if len(pagination) == 2 {
		query += " LIMIT ? OFFSET ?"
		args = append(append([]any{}, args...), pagination[0], pagination[1])
	}

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list audit events: %w", err)
	}
	events := []AuditEvent{}
	for rows.Next() {
		var event AuditEvent
		var created string
		if err := rows.Scan(&event.ID, &event.Action, &event.ResourceType, &event.ResourceID, &event.Outcome, &event.DetailJSON, &created); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan audit event: %w", err)
		}
		event.CreatedAt = parseStamp(created)
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate audit events: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close audit rows: %w", err)
	}
	return events, nil
}
