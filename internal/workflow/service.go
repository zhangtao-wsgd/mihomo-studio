package workflow

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"mihomo-studio/internal/config"
	"mihomo-studio/internal/domain"
)

type Service struct {
	DB         *sql.DB
	Builder    *config.Builder
	Runtime    Runtime
	ActivePath string
	publishMu  sync.Mutex
}

func (s *Service) Build(ctx context.Context) (config.Candidate, error) { return s.Builder.Build(ctx) }
func (s *Service) Candidate(ctx context.Context, id string) (config.Candidate, error) {
	var c config.Candidate
	var created string
	err := s.DB.QueryRowContext(ctx, `SELECT id,sha256,snapshot_path,status,validation_message,source_fingerprint,created_at FROM config_candidates WHERE id=?`, id).Scan(&c.ID, &c.SHA256, &c.SnapshotPath, &c.Status, &c.ValidationMessage, &c.SourceFingerprint, &created)
	if err == sql.ErrNoRows {
		return c, errors.New("candidate not found")
	}
	if err != nil {
		return c, err
	}
	c.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return c, nil
}
func (s *Service) CandidateInputs(ctx context.Context, id string) ([]config.CandidateInput, error) {
	if _, err := s.Candidate(ctx, id); err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT subscription_id,content_sha256,snapshot_path FROM config_candidate_inputs WHERE candidate_id=? ORDER BY subscription_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	inputs := []config.CandidateInput{}
	for rows.Next() {
		var input config.CandidateInput
		if err := rows.Scan(&input.SubscriptionID, &input.SHA256, &input.SnapshotPath); err != nil {
			return nil, err
		}
		inputs = append(inputs, input)
	}
	return inputs, rows.Err()
}

func (s *Service) Publish(ctx context.Context, id string, confirm bool) (domain.Deployment, error) {
	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	return s.publishLocked(ctx, id, confirm, "publish")
}

func (s *Service) publishLocked(ctx context.Context, id string, confirm bool, operation string) (domain.Deployment, error) {
	if !confirm {
		return domain.Deployment{}, errors.New("explicit confirmation is required")
	}
	c, err := s.Candidate(ctx, id)
	if err != nil {
		return domain.Deployment{}, err
	}
	if c.Status != "validated" && c.Status != "published" {
		return domain.Deployment{}, fmt.Errorf("candidate status %q cannot be published", c.Status)
	}
	actual, err := hashFile(c.SnapshotPath)
	if err != nil {
		return domain.Deployment{}, err
	}
	if actual != c.SHA256 {
		return domain.Deployment{}, errors.New("candidate snapshot hash mismatch; refusing to publish")
	}
	if s.ActivePath == "" {
		return domain.Deployment{}, errors.New("active configuration path is not configured")
	}
	if err := os.MkdirAll(filepath.Dir(s.ActivePath), 0o750); err != nil {
		return domain.Deployment{}, err
	}
	var previous sql.NullString
	_ = s.DB.QueryRowContext(ctx, `SELECT value FROM app_state WHERE key='active_candidate_id'`).Scan(&previous)
	d := domain.Deployment{ID: newID(), Operation: operation, CandidateID: c.ID, Status: "publishing", StartedAt: time.Now().UTC()}
	if previous.Valid {
		d.PreviousCandidateID = previous.String
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO deployments(id,operation,candidate_id,previous_candidate_id,status,runtime_message,started_at) VALUES(?,?,?,?,?,?,?)`, d.ID, d.Operation, d.CandidateID, nullString(d.PreviousCandidateID), d.Status, "Publishing immutable candidate snapshot", d.StartedAt.Format(time.RFC3339Nano)); err != nil {
		return d, err
	}
	previousBytes, previousFileErr := os.ReadFile(s.ActivePath)
	hadPreviousFile := previousFileErr == nil
	if previousFileErr != nil && !errors.Is(previousFileErr, os.ErrNotExist) {
		d.Status = "failed"
		d.RuntimeMessage = previousFileErr.Error()
		if persistErr := s.finish(ctx, &d); persistErr != nil {
			return d, fmt.Errorf("persist deployment failure: %w", persistErr)
		}
		return d, previousFileErr
	}
	if err := copyAtomic(c.SnapshotPath, s.ActivePath); err != nil {
		d.Status = "failed"
		d.RuntimeMessage = err.Error()
		if persistErr := s.finish(ctx, &d); persistErr != nil {
			return d, fmt.Errorf("persist deployment failure: %w", persistErr)
		}
		return d, err
	}
	if s.Runtime == nil {
		d.Status = "published_unverified"
		d.RuntimeMessage = "Snapshot promoted, but no Mihomo runtime controller is configured; live state is not claimed."
	} else if err := s.Runtime.Reload(ctx, s.ActivePath); err != nil {
		// A transport failure can mean the reload succeeded but its response was lost. Do not mutate runtime state automatically when outcome is ambiguous.
		if stringsContains(err.Error(), "uncertain") {
			d.Status = "unknown"
			d.RuntimeMessage = err.Error()
		} else {
			d.Status = "failed"
			d.RuntimeMessage = err.Error()
			if hadPreviousFile {
				if restoreErr := writeAtomic(s.ActivePath, previousBytes); restoreErr != nil {
					d.Status = "unknown"
					d.RuntimeMessage += "; failed to restore previous file: " + restoreErr.Error()
				} else if restoreErr := s.Runtime.Reload(ctx, s.ActivePath); restoreErr != nil {
					d.Status = "unknown"
					d.RuntimeMessage += "; previous config restored on disk but runtime restore is unverified: " + restoreErr.Error()
				} else {
					d.RuntimeMessage += "; previous active file restored after rejection"
				}
			} else if removeErr := os.Remove(s.ActivePath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				d.Status = "unknown"
				d.RuntimeMessage += "; could not remove rejected initial config: " + removeErr.Error()
			}
		}
		if persistErr := s.finish(ctx, &d); persistErr != nil {
			return d, fmt.Errorf("persist deployment result: %w", persistErr)
		}
		return d, err
	} else if err := s.Runtime.Ready(ctx); err != nil {
		d.Status = "unknown"
		d.RuntimeMessage = "Reload returned success but runtime verification failed: " + err.Error()
	} else {
		d.Status = "verified"
		d.RuntimeMessage = "Mihomo accepted the candidate and controller readiness check succeeded."
		if err := s.setActiveCandidate(ctx, c.ID); err != nil {
			d.Status = "unknown"
			d.RuntimeMessage += "; failed to persist active candidate: " + err.Error()
		}
	}
	if d.Status == "verified" {
		if _, err := s.DB.ExecContext(ctx, `UPDATE config_candidates SET status='published' WHERE id=?`, c.ID); err != nil {
			d.Status = "unknown"
			d.RuntimeMessage += "; failed to persist candidate status: " + err.Error()
		}
	}
	if err := s.finish(ctx, &d); err != nil {
		return d, fmt.Errorf("persist deployment result: %w", err)
	}
	return d, nil
}
func (s *Service) Verify(ctx context.Context, id string) (domain.Deployment, error) {
	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	d, err := s.getDeployment(ctx, id)
	if err != nil {
		return d, err
	}
	if d.Status != "published_unverified" && d.Status != "unknown" {
		return d, fmt.Errorf("deployment status %q does not require verification", d.Status)
	}
	if s.Runtime == nil {
		return d, errors.New("Mihomo runtime controller is not configured")
	}
	candidate, err := s.Candidate(ctx, d.CandidateID)
	if err != nil {
		return d, err
	}
	if actual, hashErr := hashFile(candidate.SnapshotPath); hashErr != nil || actual != candidate.SHA256 {
		return d, errors.New("candidate snapshot is missing or its hash does not match; refusing verification")
	}
	activeHash, activeErr := hashFile(s.ActivePath)
	if activeErr != nil || activeHash != candidate.SHA256 {
		return d, errors.New("active config file does not match this deployment candidate; refusing verification")
	}
	if err := s.Runtime.Reload(ctx, s.ActivePath); err != nil {
		d.Status = "unknown"
		d.RuntimeMessage = "Explicit verification reload did not return a definitive result: " + err.Error()
		if persistErr := s.finish(ctx, &d); persistErr != nil {
			return d, fmt.Errorf("persist verification result: %w", persistErr)
		}
		return d, err
	}
	if err := s.Runtime.Ready(ctx); err != nil {
		d.Status = "unknown"
		d.RuntimeMessage = "Runtime remains unverified: " + err.Error()
	} else {
		d.Status = "verified"
		d.RuntimeMessage = "Mihomo accepted the candidate on explicit reload and controller readiness succeeded; traffic-level behavior still requires a separate functional check."
		if err := s.setActiveCandidate(ctx, d.CandidateID); err != nil {
			d.Status = "unknown"
			d.RuntimeMessage += "; failed to persist active candidate: " + err.Error()
		}
		if d.Status == "verified" {
			if _, err := s.DB.ExecContext(ctx, `UPDATE config_candidates SET status='published' WHERE id=?`, d.CandidateID); err != nil {
				d.Status = "unknown"
				d.RuntimeMessage += "; failed to persist candidate status: " + err.Error()
			}
		}
	}
	if err := s.finish(ctx, &d); err != nil {
		return d, fmt.Errorf("persist verification result: %w", err)
	}
	return d, nil
}
func (s *Service) Rollback(ctx context.Context, id string, confirm bool) (domain.Deployment, error) {
	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	if !confirm {
		return domain.Deployment{}, errors.New("explicit confirmation is required")
	}
	d, err := s.getDeployment(ctx, id)
	if err != nil {
		return d, err
	}
	if d.Status != "verified" {
		return d, errors.New("only a verified deployment can be rolled back automatically")
	}
	if d.PreviousCandidateID == "" {
		return d, errors.New("no previous known candidate is available for rollback")
	}
	var active sql.NullString
	if err := s.DB.QueryRowContext(ctx, `SELECT value FROM app_state WHERE key='active_candidate_id'`).Scan(&active); err != nil {
		return d, err
	}
	if !active.Valid || active.String != d.CandidateID {
		return d, errors.New("deployment is no longer the active verified candidate; refusing stale rollback")
	}
	rollbackDeployment, err := s.publishLocked(ctx, d.PreviousCandidateID, true, "rollback")
	if err != nil || rollbackDeployment.Status != "verified" {
		return rollbackDeployment, err
	}
	d.Status = "rolled_back"
	d.RuntimeMessage = "This deployment was explicitly rolled back to its recorded previous verified candidate."
	if err := s.finish(ctx, &d); err != nil {
		return rollbackDeployment, fmt.Errorf("rollback succeeded but original deployment audit could not be persisted: %w", err)
	}
	return rollbackDeployment, nil
}
func (s *Service) getDeployment(ctx context.Context, id string) (domain.Deployment, error) {
	var d domain.Deployment
	var previous sql.NullString
	var started string
	var finished sql.NullString
	err := s.DB.QueryRowContext(ctx, `SELECT id,operation,candidate_id,previous_candidate_id,status,runtime_message,started_at,finished_at FROM deployments WHERE id=?`, id).Scan(&d.ID, &d.Operation, &d.CandidateID, &previous, &d.Status, &d.RuntimeMessage, &started, &finished)
	if err == sql.ErrNoRows {
		return d, errors.New("deployment not found")
	}
	if err != nil {
		return d, err
	}
	d.PreviousCandidateID = previous.String
	d.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
	if finished.Valid {
		t, _ := time.Parse(time.RFC3339Nano, finished.String)
		d.FinishedAt = &t
	}
	return d, nil
}
func (s *Service) setActiveCandidate(ctx context.Context, candidateID string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO app_state(key,value,updated_at) VALUES('active_candidate_id',?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`, candidateID, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Service) finish(ctx context.Context, d *domain.Deployment) error {
	now := time.Now().UTC()
	d.FinishedAt = &now
	if _, err := s.DB.ExecContext(ctx, `UPDATE deployments SET status=?,runtime_message=?,finished_at=? WHERE id=?`, d.Status, d.RuntimeMessage, now.Format(time.RFC3339Nano), d.ID); err != nil {
		return err
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO audit_events(id,action,resource_type,resource_id,outcome,detail_json,created_at) VALUES(?,?,?,?,?,?,?)`, newID(), "deployment."+d.Operation+"."+d.Status, "deployment", d.ID, d.Status, `{"candidateId":"`+d.CandidateID+`","operation":"`+d.Operation+`"}`, now.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return nil
}
func writeAtomic(dst string, body []byte) error {
	tmp := dst + ".restore.tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	if _, err = f.Write(body); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err = f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err = os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func copyAtomic(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err = out.Sync(); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err = out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err = os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
func hashFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}
func newID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err == nil {
		return hex.EncodeToString(value[:])
	}
	h := sha256.Sum256([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
	return hex.EncodeToString(h[:16])
}
func nullString(v string) any {
	if v == "" {
		return nil
	}
	return v
}
func stringsContains(s, part string) bool { return len(s) >= len(part) && contains(s, part) }
func contains(s, part string) bool {
	for i := 0; i+len(part) <= len(s); i++ {
		if s[i:i+len(part)] == part {
			return true
		}
	}
	return false
}
