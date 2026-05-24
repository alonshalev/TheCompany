package governance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/symbiont-ai/symbiont/internal/models"
)

// ── ApprovalResolver ──────────────────────────────────────────
//
// Creates and resolves human-in-the-loop approval requests.
//
// Workflow integration:
//   When the orchestration engine encounters an approval_gate node it calls
//   CreateRequest, then pauses the run (sets status = 'paused').
//   The API exposes GET/POST endpoints so an operator can approve or reject.
//   The engine polls (or is notified via the broadcaster) for resolution.
//
// Timeout handling:
//   A background goroutine (StartExpiryWorker) expires pending requests
//   whose expires_at has elapsed and unpauses the run with status 'failed'.

var (
	ErrAlreadyResolved = errors.New("approval request already resolved")
	ErrNotFound        = errors.New("approval request not found")
	ErrExpired         = errors.New("approval request has expired")
)

// CreateRequest inserts a new pending approval request and pauses the
// associated run.
func (r *ApprovalResolver) CreateRequest(
	ctx context.Context,
	req *models.ApprovalRequest,
) error {
	if req.ID == (uuid.UUID{}) {
		req.ID = uuid.New()
	}
	req.Status = models.ApprovalStatusPending
	req.CreatedAt = time.Now().UTC()

	const q = `
		INSERT INTO approval_requests
		       (id, project_id, run_id, reason_type, reason_detail,
		        payload, status, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`

	if _, err := r.db.ExecContext(ctx, q,
		req.ID, req.ProjectID, req.RunID,
		req.ReasonType, req.ReasonDetail,
		req.Payload, req.Status, req.ExpiresAt,
	); err != nil {
		return fmt.Errorf("create approval request: %w", err)
	}

	// Pause the run
	if _, err := r.db.ExecContext(ctx,
		`UPDATE runs SET status = 'paused', updated_at = now() WHERE id = $1 AND status IN ('pending','running')`,
		req.RunID,
	); err != nil {
		return fmt.Errorf("pause run: %w", err)
	}

	return nil
}

// Resolve approves or rejects a request. Only pending requests can be resolved.
// On success, the associated run is unpaused (running) or failed (rejected).
func (r *ApprovalResolver) Resolve(
	ctx context.Context,
	requestID uuid.UUID,
	resolverID uuid.UUID,
	approve bool,
	note string,
) (*models.ApprovalRequest, error) {
	req, err := r.GetRequest(ctx, requestID)
	if err != nil {
		return nil, err
	}

	if req.Status != models.ApprovalStatusPending {
		return nil, ErrAlreadyResolved
	}
	if req.ExpiresAt != nil && req.ExpiresAt.Before(time.Now().UTC()) {
		return nil, ErrExpired
	}

	newStatus := models.ApprovalStatusApproved
	if !approve {
		newStatus = models.ApprovalStatusRejected
	}

	now := time.Now().UTC()
	const q = `
		UPDATE approval_requests
		SET    status = $1, resolved_by = $2, resolution_note = $3, resolved_at = $4
		WHERE  id = $5`

	if _, err = r.db.ExecContext(ctx, q, newStatus, resolverID, note, now, requestID); err != nil {
		return nil, fmt.Errorf("resolve approval: %w", err)
	}

	// Resume or fail the run
	runStatus := "running"
	if !approve {
		runStatus = "failed"
	}
	if _, err = r.db.ExecContext(ctx,
		`UPDATE runs SET status = $1, updated_at = now() WHERE id = $2`,
		runStatus, req.RunID,
	); err != nil {
		return nil, fmt.Errorf("update run after approval: %w", err)
	}

	req.Status = newStatus
	req.ResolvedBy = &resolverID
	if note != "" {
		req.ResolutionNote = &note
	}
	req.ResolvedAt = &now

	return req, nil
}

// GetRequest fetches a single approval request by ID.
func (r *ApprovalResolver) GetRequest(ctx context.Context, id uuid.UUID) (*models.ApprovalRequest, error) {
	const q = `
		SELECT id, project_id, run_id, reason_type, reason_detail,
		       payload, status, resolved_by, resolution_note,
		       expires_at, created_at, resolved_at
		FROM   approval_requests
		WHERE  id = $1`

	req := &models.ApprovalRequest{}
	if err := r.db.QueryRowContext(ctx, q, id).Scan(
		&req.ID, &req.ProjectID, &req.RunID, &req.ReasonType, &req.ReasonDetail,
		&req.Payload, &req.Status, &req.ResolvedBy, &req.ResolutionNote,
		&req.ExpiresAt, &req.CreatedAt, &req.ResolvedAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return req, nil
}

// ListPending returns all pending approval requests for a project.
func (r *ApprovalResolver) ListPending(ctx context.Context, projectID uuid.UUID) ([]*models.ApprovalRequest, error) {
	const q = `
		SELECT id, project_id, run_id, reason_type, reason_detail,
		       payload, status, resolved_by, resolution_note,
		       expires_at, created_at, resolved_at
		FROM   approval_requests
		WHERE  project_id = $1 AND status = 'pending'
		ORDER  BY created_at ASC`

	rows, err := r.db.QueryContext(ctx, q, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*models.ApprovalRequest
	for rows.Next() {
		req := &models.ApprovalRequest{}
		if err = rows.Scan(
			&req.ID, &req.ProjectID, &req.RunID, &req.ReasonType, &req.ReasonDetail,
			&req.Payload, &req.Status, &req.ResolvedBy, &req.ResolutionNote,
			&req.ExpiresAt, &req.CreatedAt, &req.ResolvedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, req)
	}
	return out, rows.Err()
}

// ListAll returns the most recent approval requests for a project (any status).
func (r *ApprovalResolver) ListAll(ctx context.Context, projectID uuid.UUID, limit int) ([]*models.ApprovalRequest, error) {
	const q = `
		SELECT id, project_id, run_id, reason_type, reason_detail,
		       payload, status, resolved_by, resolution_note,
		       expires_at, created_at, resolved_at
		FROM   approval_requests
		WHERE  project_id = $1
		ORDER  BY created_at DESC
		LIMIT  $2`

	rows, err := r.db.QueryContext(ctx, q, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*models.ApprovalRequest
	for rows.Next() {
		req := &models.ApprovalRequest{}
		if err = rows.Scan(
			&req.ID, &req.ProjectID, &req.RunID, &req.ReasonType, &req.ReasonDetail,
			&req.Payload, &req.Status, &req.ResolvedBy, &req.ResolutionNote,
			&req.ExpiresAt, &req.CreatedAt, &req.ResolvedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, req)
	}
	return out, rows.Err()
}

// StartExpiryWorker runs a background goroutine that expires timed-out
// approval requests every minute and fails the associated runs.
func (r *ApprovalResolver) StartExpiryWorker(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := r.expireStale(ctx); err != nil {
					// Non-fatal: log and continue
					_ = err
				}
			}
		}
	}()
}

func (r *ApprovalResolver) expireStale(ctx context.Context) error {
	// Find pending requests that have passed their expiry time
	const q = `
		UPDATE approval_requests
		SET    status = 'expired', resolved_at = now()
		WHERE  status = 'pending'
		  AND  expires_at IS NOT NULL
		  AND  expires_at < now()
		RETURNING run_id`

	rows, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()

	var runIDs []uuid.UUID
	for rows.Next() {
		var rid uuid.UUID
		if err = rows.Scan(&rid); err != nil {
			return err
		}
		runIDs = append(runIDs, rid)
	}
	if err = rows.Err(); err != nil {
		return err
	}

	// Fail the paused runs
	for _, rid := range runIDs {
		_, _ = r.db.ExecContext(ctx,
			`UPDATE runs SET status = 'failed', error = 'approval request expired', updated_at = now() WHERE id = $1 AND status = 'paused'`,
			rid,
		)
	}

	return nil
}

// ApprovalResolver coordinates approval gate lifecycle.
type ApprovalResolver struct {
	db *sql.DB
}

// NewApprovalResolver creates an ApprovalResolver backed by the given DB.
func NewApprovalResolver(db *sql.DB) *ApprovalResolver {
	return &ApprovalResolver{db: db}
}
