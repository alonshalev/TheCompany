package governance

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/symbiont-ai/symbiont/internal/models"
)

// ── AuditWriter ───────────────────────────────────────────────
//
// Writes tamper-evident, hash-chained audit events to audit_events.
//
// Hash chain:
//   row_hash = SHA-256( prev_hash || ext_id || action || created_at_unix )
//
// The chain is per-org: each org has its own independent chain anchored
// at the genesis record (prev_hash = "").
//
// Thread safety: a single mutex serialises writes within a process.
// In a multi-replica deployment, the DB RULE (no UPDATE/DELETE on
// audit_events) provides the immutability guarantee; chain continuity
// must be verified offline rather than enforced on write.

// AuditEntry is the input to Write; mirrors models.AuditEvent fields.
type AuditEntry struct {
	OrgID        uuid.UUID
	ProjectID    *uuid.UUID
	ActorType    string // "user" | "agent" | "system"
	ActorID      *uuid.UUID
	ActorName    *string
	Action       string
	ResourceType *string
	ResourceID   *uuid.UUID
	Outcome      string // "success" | "failure" | "blocked"
	Detail       map[string]any
}

// AuditWriter writes hash-chained events.
type AuditWriter struct {
	db  *sql.DB
	mu  sync.Mutex
}

// NewAuditWriter creates an AuditWriter backed by the given DB.
func NewAuditWriter(db *sql.DB) *AuditWriter {
	return &AuditWriter{db: db}
}

// Write records an immutable audit event and chains it to the previous
// event for the same org. It is safe to call concurrently.
func (w *AuditWriter) Write(ctx context.Context, entry AuditEntry) (*models.AuditEvent, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Fetch the row_hash of the most recent event for this org
	prevHash, err := w.fetchPrevHash(ctx, entry.OrgID)
	if err != nil {
		return nil, fmt.Errorf("audit: fetch prev hash: %w", err)
	}

	extID := uuid.New()
	now := time.Now().UTC()

	// Compute row hash
	rowHash := computeHash(prevHash, extID, entry.Action, now)

	detail := models.JSONMap(entry.Detail)
	if detail == nil {
		detail = models.JSONMap{}
	}

	const insertQ = `
		INSERT INTO audit_events
		       (ext_id, org_id, project_id,
		        actor_type, actor_id, actor_name,
		        action, resource_type, resource_id,
		        outcome, detail, prev_hash, row_hash, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		RETURNING id`

	var id int64
	if err = w.db.QueryRowContext(ctx, insertQ,
		extID, entry.OrgID, entry.ProjectID,
		entry.ActorType, entry.ActorID, entry.ActorName,
		entry.Action, entry.ResourceType, entry.ResourceID,
		entry.Outcome, detail, nullableString(prevHash), rowHash, now,
	).Scan(&id); err != nil {
		return nil, fmt.Errorf("audit: insert: %w", err)
	}

	return &models.AuditEvent{
		ID:           id,
		ExtID:        extID,
		OrgID:        entry.OrgID,
		ProjectID:    entry.ProjectID,
		ActorType:    entry.ActorType,
		ActorID:      entry.ActorID,
		ActorName:    entry.ActorName,
		Action:       entry.Action,
		ResourceType: entry.ResourceType,
		ResourceID:   entry.ResourceID,
		Outcome:      entry.Outcome,
		Detail:       detail,
		RowHash:      rowHash,
		CreatedAt:    now,
	}, nil
}

// VerifyChain reads all audit events for an org in order and verifies
// each row_hash. Returns the first broken link, or nil if the chain is intact.
func (w *AuditWriter) VerifyChain(ctx context.Context, orgID uuid.UUID) error {
	const q = `
		SELECT ext_id, action, prev_hash, row_hash, created_at
		FROM   audit_events
		WHERE  org_id = $1
		ORDER  BY id ASC`

	rows, err := w.db.QueryContext(ctx, q, orgID)
	if err != nil {
		return err
	}
	defer rows.Close()

	var prev string // empty string for genesis
	for rows.Next() {
		var (
			extID    uuid.UUID
			action   string
			prevH    *string
			rowH     string
			createdAt time.Time
		)
		if err = rows.Scan(&extID, &action, &prevH, &rowH, &createdAt); err != nil {
			return err
		}

		prevHashVal := ""
		if prevH != nil {
			prevHashVal = *prevH
		}

		if prevHashVal != prev {
			return fmt.Errorf("audit chain broken at ext_id=%s: expected prev_hash %q, got %q", extID, prev, prevHashVal)
		}

		expected := computeHash(prevHashVal, extID, action, createdAt)
		if expected != rowH {
			return fmt.Errorf("audit row tampered at ext_id=%s: computed hash %q, stored %q", extID, expected, rowH)
		}

		prev = rowH
	}
	return rows.Err()
}

// ListAuditEvents returns a page of audit events for the given org/project.
func (w *AuditWriter) ListAuditEvents(
	ctx context.Context,
	orgID uuid.UUID,
	projectID *uuid.UUID,
	limit, offset int,
) ([]*models.AuditEvent, error) {
	const q = `
		SELECT id, ext_id, org_id, project_id,
		       actor_type, actor_id, actor_name,
		       action, resource_type, resource_id,
		       outcome, detail, prev_hash, row_hash, created_at
		FROM   audit_events
		WHERE  org_id = $1
		  AND  ($2::uuid IS NULL OR project_id = $2)
		ORDER  BY id DESC
		LIMIT  $3 OFFSET $4`

	rows, err := w.db.QueryContext(ctx, q, orgID, projectID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*models.AuditEvent
	for rows.Next() {
		e := &models.AuditEvent{}
		if err = rows.Scan(
			&e.ID, &e.ExtID, &e.OrgID, &e.ProjectID,
			&e.ActorType, &e.ActorID, &e.ActorName,
			&e.Action, &e.ResourceType, &e.ResourceID,
			&e.Outcome, &e.Detail, &e.PrevHash, &e.RowHash, &e.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ── helpers ───────────────────────────────────────────────────

func (w *AuditWriter) fetchPrevHash(ctx context.Context, orgID uuid.UUID) (string, error) {
	const q = `SELECT row_hash FROM audit_events WHERE org_id = $1 ORDER BY id DESC LIMIT 1`
	var h string
	err := w.db.QueryRowContext(ctx, q, orgID).Scan(&h)
	if err == sql.ErrNoRows {
		return "", nil // genesis
	}
	return h, err
}

func computeHash(prevHash string, extID uuid.UUID, action string, ts time.Time) string {
	h := sha256.New()
	h.Write([]byte(prevHash))
	h.Write([]byte(extID.String()))
	h.Write([]byte(action))
	h.Write([]byte(fmt.Sprintf("%d", ts.Unix())))
	return hex.EncodeToString(h.Sum(nil))
}

func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
