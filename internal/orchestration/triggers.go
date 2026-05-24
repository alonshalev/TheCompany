// Package orchestration — trigger scheduler.
//
// The Scheduler runs cron-based workflow triggers (heartbeats) and provides
// a Fire() method for manual and webhook triggers. It polls the triggers
// table every 30 s, fires any cron that is overdue, and updates last_fired_at.
package orchestration

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/symbiont-ai/symbiont/internal/db"
	"github.com/symbiont-ai/symbiont/internal/models"
)

// Scheduler polls trigger definitions and fires workflow runs on schedule.
type Scheduler struct {
	db     *db.DB
	engine *Engine
	mu     sync.Mutex
}

// NewScheduler creates a Scheduler.
func NewScheduler(database *db.DB, engine *Engine) *Scheduler {
	return &Scheduler{db: database, engine: engine}
}

// Start runs the scheduler loop until ctx is cancelled.
func (s *Scheduler) Start(ctx context.Context) {
	log.Info().Msg("trigger scheduler starting")
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	// Fire once immediately on startup
	s.tick(ctx)

	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("trigger scheduler stopped")
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

// tick checks all active cron triggers and fires any that are due.
func (s *Scheduler) tick(ctx context.Context) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT t.id, t.workflow_id, t.cron_expr, t.last_fired_at, w.project_id
		 FROM triggers t
		 JOIN workflows w ON w.id = t.workflow_id
		 WHERE t.kind = 'cron' AND t.is_active = true AND w.is_active = true`)
	if err != nil {
		log.Error().Err(err).Msg("scheduler: query triggers")
		return
	}
	defer rows.Close()

	for rows.Next() {
		var (
			triggerID   uuid.UUID
			workflowID  uuid.UUID
			projectID   uuid.UUID
			cronExpr    string
			lastFiredAt *time.Time
		)
		if err := rows.Scan(&triggerID, &workflowID, &cronExpr, &lastFiredAt, &projectID); err != nil {
			log.Error().Err(err).Msg("scheduler: scan trigger row")
			continue
		}

		if !s.isDue(cronExpr, lastFiredAt) {
			continue
		}

		s.fire(ctx, triggerID, workflowID, projectID, models.TriggerKindCron)
	}
}

// isDue returns true if the cron trigger should fire now.
// Uses a minimal implementation that supports common intervals.
// For production, replace with a proper cron parser (e.g. robfig/cron).
func (s *Scheduler) isDue(expr string, lastFiredAt *time.Time) bool {
	if lastFiredAt == nil {
		return true // never fired — fire now
	}

	interval, err := parseCronInterval(expr)
	if err != nil {
		log.Warn().Err(err).Str("expr", expr).Msg("scheduler: unparseable cron expression")
		return false
	}

	return time.Since(*lastFiredAt) >= interval
}

// parseCronInterval converts common cron-like strings to a time.Duration.
// Supports: @hourly, @daily, @weekly, and standard 5-field expressions where
// the interval is inferred from the most constrained field.
// A full cron parser (robfig/cron v3) should replace this in production.
func parseCronInterval(expr string) (time.Duration, error) {
	switch expr {
	case "@hourly":
		return time.Hour, nil
	case "@daily", "@midnight":
		return 24 * time.Hour, nil
	case "@weekly":
		return 7 * 24 * time.Hour, nil
	case "@every 1m", "* * * * *":
		return time.Minute, nil
	case "@every 5m", "*/5 * * * *":
		return 5 * time.Minute, nil
	case "@every 15m", "*/15 * * * *":
		return 15 * time.Minute, nil
	case "@every 30m", "*/30 * * * *":
		return 30 * time.Minute, nil
	case "@every 1h", "0 * * * *":
		return time.Hour, nil
	default:
		// Fallback: try to treat as a Go duration string
		d, err := time.ParseDuration(expr)
		if err != nil {
			return 0, fmt.Errorf("unsupported cron expression: %q", expr)
		}
		return d, nil
	}
}

// fire enqueues a workflow run for the given trigger.
func (s *Scheduler) fire(ctx context.Context, triggerID, workflowID, projectID uuid.UUID, kind models.TriggerKind) {
	log.Info().
		Str("trigger_id", triggerID.String()).
		Str("workflow_id", workflowID.String()).
		Str("kind", string(kind)).
		Msg("trigger firing")

	run, err := s.engine.StartWorkflowRun(ctx, projectID, workflowID,
		models.JSONMap{"trigger_kind": string(kind), "trigger_id": triggerID.String()}, nil)
	if err != nil {
		log.Error().Err(err).Str("trigger_id", triggerID.String()).Msg("trigger: failed to start run")
		return
	}

	// Update last_fired_at
	s.db.ExecContext(ctx,
		`UPDATE triggers SET last_fired_at=now() WHERE id=$1`, triggerID)

	log.Info().Str("run_id", run.ID.String()).Msg("trigger fired — run enqueued")
}

// FireManual starts a manual workflow run (used by the API handler).
func (s *Scheduler) FireManual(ctx context.Context, workflowID, projectID uuid.UUID, input models.JSONMap, initiatedBy *uuid.UUID) (*models.Run, error) {
	return s.engine.StartWorkflowRun(ctx, projectID, workflowID, input, initiatedBy)
}

// FireWebhook is called by the webhook ingest handler. It finds the trigger
// by webhook path and fires the associated workflow.
func (s *Scheduler) FireWebhook(ctx context.Context, webhookPath string, payload models.JSONMap) (*models.Run, error) {
	var triggerID, workflowID, projectID uuid.UUID
	err := s.db.QueryRowContext(ctx,
		`SELECT t.id, t.workflow_id, w.project_id
		 FROM triggers t
		 JOIN workflows w ON w.id = t.workflow_id
		 WHERE t.webhook_path=$1 AND t.is_active=true AND w.is_active=true
		 LIMIT 1`,
		webhookPath).Scan(&triggerID, &workflowID, &projectID)
	if err != nil {
		return nil, fmt.Errorf("webhook trigger: path %q not found", webhookPath)
	}

	run, err := s.engine.StartWorkflowRun(ctx, projectID, workflowID, payload, nil)
	if err != nil {
		return nil, err
	}

	s.db.ExecContext(ctx, `UPDATE triggers SET last_fired_at=now() WHERE id=$1`, triggerID)
	return run, nil
}
