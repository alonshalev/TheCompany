// Package governance implements multi-level budget enforcement, hash-chained
// audit logging, approval gates, PII redaction, and anomaly detection.
package governance

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/symbiont-ai/symbiont/internal/models"
)

// ── BudgetManager ─────────────────────────────────────────────

// BudgetLevel identifies which level of the budget hierarchy is being checked.
type BudgetLevel string

const (
	BudgetLevelAgent    BudgetLevel = "agent"
	BudgetLevelWorkflow BudgetLevel = "workflow"
	BudgetLevelProject  BudgetLevel = "project"
)

// BudgetResult is returned by CheckAndReserve.
type BudgetResult struct {
	Allowed      bool
	SoftWarning  bool   // spend has crossed the warning threshold
	HardExceeded bool   // spend has reached or exceeded the hard limit
	Message      string // human-readable reason (populated when !Allowed)
	BudgetID     uuid.UUID
	LimitCents   int64
	SpendCents   int64 // current spend BEFORE this reservation
}

// SpendEntry records a single spend event across all levels.
type SpendEntry struct {
	ProjectID       uuid.UUID
	RunID           uuid.UUID
	EventID         *int64
	AgentInstanceID *uuid.UUID
	AmountCents     int
	Provider        string
	Model           string
	InputTokens     int
	OutputTokens    int
}

// BudgetManager enforces multi-level budget caps and records spend.
type BudgetManager struct {
	db *sql.DB
}

// NewBudgetManager creates a BudgetManager backed by the given DB.
func NewBudgetManager(db *sql.DB) *BudgetManager {
	return &BudgetManager{db: db}
}

// CheckAndReserve verifies that spending amountCents will not exceed any
// active budget for the given scope. It checks all three levels in order:
// agent → workflow → project. The first hard-exceeded budget stops the chain.
//
// It does NOT record spend — call RecordSpend after the model call completes.
func (m *BudgetManager) CheckAndReserve(
	ctx context.Context,
	projectID uuid.UUID,
	agentInstanceID *uuid.UUID,
	workflowID *uuid.UUID,
	amountCents int,
) (*BudgetResult, error) {
	// Collect (scope_type, scope_id) pairs to check in order
	type scope struct {
		kind BudgetLevel
		id   uuid.UUID
	}

	scopes := []scope{
		{BudgetLevelProject, projectID},
	}
	if workflowID != nil {
		scopes = append([]scope{{BudgetLevelWorkflow, *workflowID}}, scopes...)
	}
	if agentInstanceID != nil {
		scopes = append([]scope{{BudgetLevelAgent, *agentInstanceID}}, scopes...)
	}

	for _, sc := range scopes {
		res, err := m.checkScope(ctx, projectID, sc.kind, sc.id, amountCents)
		if err != nil {
			return nil, fmt.Errorf("budget check (%s): %w", sc.kind, err)
		}
		if res != nil {
			return res, nil
		}
	}

	// All checks passed
	return &BudgetResult{Allowed: true}, nil
}

// checkScope returns a *BudgetResult only when the budget would be exceeded.
// Returns nil when within limits (caller should continue to next scope).
func (m *BudgetManager) checkScope(
	ctx context.Context,
	projectID uuid.UUID,
	level BudgetLevel,
	scopeID uuid.UUID,
	amountCents int,
) (*BudgetResult, error) {
	const q = `
		SELECT b.id, b.limit_cents, b.warning_pct,
		       COALESCE(SUM(sl.amount_cents), 0) AS current_spend
		FROM   budgets b
		LEFT JOIN spend_ledger sl
		       ON sl.project_id = b.project_id
		      AND (
		            (b.scope_type = 'agent'    AND sl.agent_instance_id = b.scope_id)
		         OR (b.scope_type = 'workflow' AND sl.run_id IN (
		                SELECT id FROM runs WHERE workflow_id = b.scope_id
		             ))
		         OR (b.scope_type = 'project'  AND sl.project_id = b.scope_id)
		          )
		      AND (b.next_reset_at IS NULL OR sl.created_at < b.next_reset_at)
		WHERE  b.scope_type = $1
		  AND  b.scope_id   = $2
		  AND  b.is_active   = true
		GROUP  BY b.id, b.limit_cents, b.warning_pct
		LIMIT  1`

	var (
		budgetID    uuid.UUID
		limitCents  int64
		warningPct  int
		currentSpend int64
	)

	err := m.db.QueryRowContext(ctx, q, string(level), scopeID).
		Scan(&budgetID, &limitCents, &warningPct, &currentSpend)
	if err == sql.ErrNoRows {
		return nil, nil // no budget configured for this scope — allow
	}
	if err != nil {
		return nil, err
	}

	projected := currentSpend + int64(amountCents)

	if projected > limitCents {
		return &BudgetResult{
			Allowed:      false,
			HardExceeded: true,
			Message:      fmt.Sprintf("%s budget exceeded: limit $%.2f, current $%.2f, requested $%.4f", level, float64(limitCents)/100, float64(currentSpend)/100, float64(amountCents)/100),
			BudgetID:     budgetID,
			LimitCents:   limitCents,
			SpendCents:   currentSpend,
		}, nil
	}

	// Check soft warning threshold
	softWarning := warningPct > 0 && (projected*100/limitCents) >= int64(warningPct)
	return &BudgetResult{
		Allowed:     true,
		SoftWarning: softWarning,
		BudgetID:    budgetID,
		LimitCents:  limitCents,
		SpendCents:  currentSpend,
	}, nil
}

// RecordSpend writes an entry to spend_ledger and increments denormalised
// spend_cents on runs, agent_instances, and projects atomically.
func (m *BudgetManager) RecordSpend(ctx context.Context, e SpendEntry) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	// Insert into spend_ledger
	const insertLedger = `
		INSERT INTO spend_ledger
		       (project_id, run_id, event_id, agent_instance_id,
		        amount_cents, provider, model, input_tokens, output_tokens)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`

	var providerVal, modelVal interface{}
	if e.Provider != "" {
		providerVal = e.Provider
	}
	if e.Model != "" {
		modelVal = e.Model
	}

	_, err = tx.ExecContext(ctx, insertLedger,
		e.ProjectID, e.RunID, e.EventID, e.AgentInstanceID,
		e.AmountCents, providerVal, modelVal, e.InputTokens, e.OutputTokens,
	)
	if err != nil {
		return fmt.Errorf("insert spend_ledger: %w", err)
	}

	// Update run spend
	if _, err = tx.ExecContext(ctx,
		`UPDATE runs SET spend_cents = spend_cents + $1 WHERE id = $2`,
		e.AmountCents, e.RunID,
	); err != nil {
		return fmt.Errorf("update run spend: %w", err)
	}

	// Update agent instance spend
	if e.AgentInstanceID != nil {
		if _, err = tx.ExecContext(ctx,
			`UPDATE agent_instances SET spend_cents = spend_cents + $1, updated_at = now() WHERE id = $2`,
			e.AmountCents, *e.AgentInstanceID,
		); err != nil {
			return fmt.Errorf("update agent spend: %w", err)
		}
	}

	// Update project spend
	if _, err = tx.ExecContext(ctx,
		`UPDATE projects SET spend_cents = spend_cents + $1, updated_at = now() WHERE id = $2`,
		e.AmountCents, e.ProjectID,
	); err != nil {
		return fmt.Errorf("update project spend: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit spend: %w", err)
	}

	return nil
}

// UpsertBudget creates or replaces the budget for a given scope.
func (m *BudgetManager) UpsertBudget(ctx context.Context, b *models.Budget) error {
	const q = `
		INSERT INTO budgets
		       (id, project_id, scope_type, scope_id, limit_cents, warning_pct, reset_period, next_reset_at, is_active)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (id)
		  DO UPDATE SET
		    limit_cents  = EXCLUDED.limit_cents,
		    warning_pct  = EXCLUDED.warning_pct,
		    reset_period = EXCLUDED.reset_period,
		    next_reset_at= EXCLUDED.next_reset_at,
		    is_active    = EXCLUDED.is_active,
		    updated_at   = now()`

	_, err := m.db.ExecContext(ctx, q,
		b.ID, b.ProjectID, b.ScopeType, b.ScopeID,
		b.LimitCents, b.WarningPct, b.ResetPeriod, b.NextResetAt, b.IsActive,
	)
	return err
}

// GetBudgets lists all active budgets for a project.
func (m *BudgetManager) GetBudgets(ctx context.Context, projectID uuid.UUID) ([]*models.Budget, error) {
	const q = `
		SELECT id, project_id, scope_type, scope_id, limit_cents,
		       warning_pct, reset_period, next_reset_at, is_active, created_at, updated_at
		FROM   budgets
		WHERE  project_id = $1
		ORDER  BY scope_type, created_at`

	rows, err := m.db.QueryContext(ctx, q, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*models.Budget
	for rows.Next() {
		b := &models.Budget{}
		if err = rows.Scan(
			&b.ID, &b.ProjectID, &b.ScopeType, &b.ScopeID,
			&b.LimitCents, &b.WarningPct, &b.ResetPeriod, &b.NextResetAt,
			&b.IsActive, &b.CreatedAt, &b.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// RunBudgetResets checks all budgets with elapsed next_reset_at and resets
// their period. Intended to be called from a background goroutine.
func (m *BudgetManager) RunBudgetResets(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(1 * time.Hour):
			if err := m.resetElapsedBudgets(ctx); err != nil {
				log.Error().Err(err).Msg("budget reset failed")
			}
		}
	}
}

func (m *BudgetManager) resetElapsedBudgets(ctx context.Context) error {
	const q = `
		UPDATE budgets
		SET    next_reset_at = CASE
		           WHEN reset_period = 'monthly' THEN next_reset_at + INTERVAL '1 month'
		           WHEN reset_period = 'weekly'  THEN next_reset_at + INTERVAL '7 days'
		           ELSE NULL
		       END,
		       updated_at = now()
		WHERE  next_reset_at <= now()
		  AND  reset_period  != 'never'`

	res, err := m.db.ExecContext(ctx, q)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		log.Info().Int64("count", n).Msg("budget periods reset")
	}
	return nil
}
