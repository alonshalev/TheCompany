package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/symbiont-ai/symbiont/internal/auth"
	"github.com/symbiont-ai/symbiont/internal/models"
	"github.com/symbiont-ai/symbiont/internal/orchestration"
)

// ── List workflows ────────────────────────────────────────────

func (s *Server) handleListWorkflowsImpl(w http.ResponseWriter, r *http.Request) {
	project, _ := projectFromContext(r.Context())

	rows, err := s.db.QueryContext(r.Context(),
		`SELECT id, project_id, name, slug, description, version, definition, is_active, created_by, created_at, updated_at
		 FROM workflows WHERE project_id=$1 AND is_active=true ORDER BY created_at DESC`,
		project.ID)
	if err != nil {
		log.Error().Err(err).Msg("listWorkflows: query")
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer rows.Close()

	var workflows []models.Workflow
	for rows.Next() {
		var wf models.Workflow
		if err := rows.Scan(&wf.ID, &wf.ProjectID, &wf.Name, &wf.Slug, &wf.Description,
			&wf.Version, &wf.Definition, &wf.IsActive, &wf.CreatedBy, &wf.CreatedAt, &wf.UpdatedAt); err != nil {
			continue
		}
		workflows = append(workflows, wf)
	}
	if workflows == nil {
		workflows = []models.Workflow{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflows": workflows})
}

// ── Create workflow ───────────────────────────────────────────

type createWorkflowRequest struct {
	Name        string                        `json:"name"`
	Slug        string                        `json:"slug"`
	Description string                        `json:"description"`
	Definition  orchestration.WorkflowDefinition `json:"definition"`
}

func (s *Server) handleCreateWorkflowImpl(w http.ResponseWriter, r *http.Request) {
	project, _ := projectFromContext(r.Context())
	user, _ := auth.UserFromContext(r.Context())

	var req createWorkflowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	if req.Name == "" || req.Slug == "" {
		writeError(w, http.StatusBadRequest, "name and slug are required")
		return
	}

	defJSON, _ := json.Marshal(req.Definition)

	wf := &models.Workflow{
		ID:          uuid.New(),
		ProjectID:   project.ID,
		Name:        req.Name,
		Slug:        req.Slug,
		Version:     1,
		Definition:  models.JSONMap{},
		IsActive:    true,
		CreatedBy:   &user.ID,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if req.Description != "" {
		wf.Description = &req.Description
	}

	_, err := s.db.ExecContext(r.Context(),
		`INSERT INTO workflows (id, project_id, name, slug, description, version, definition, is_active, created_by, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$10)`,
		wf.ID, wf.ProjectID, wf.Name, wf.Slug, wf.Description,
		wf.Version, string(defJSON), wf.IsActive, wf.CreatedBy, wf.CreatedAt)
	if err != nil {
		log.Error().Err(err).Msg("createWorkflow: insert")
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusCreated, wf)
}

// ── Get workflow ──────────────────────────────────────────────

func (s *Server) handleGetWorkflowImpl(w http.ResponseWriter, r *http.Request) {
	project, _ := projectFromContext(r.Context())
	wfID, err := uuid.Parse(chi.URLParam(r, "workflowID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid workflow ID")
		return
	}

	var wf models.Workflow
	err = s.db.QueryRowContext(r.Context(),
		`SELECT id, project_id, name, slug, description, version, definition, is_active, created_by, created_at, updated_at
		 FROM workflows WHERE id=$1 AND project_id=$2`,
		wfID, project.ID).Scan(
		&wf.ID, &wf.ProjectID, &wf.Name, &wf.Slug, &wf.Description,
		&wf.Version, &wf.Definition, &wf.IsActive, &wf.CreatedBy, &wf.CreatedAt, &wf.UpdatedAt)
	if err != nil {
		writeError(w, http.StatusNotFound, "workflow not found")
		return
	}
	writeJSON(w, http.StatusOK, wf)
}

// ── Update workflow (saves a new version) ────────────────────

type updateWorkflowRequest struct {
	Name        string                           `json:"name"`
	Description string                           `json:"description"`
	Definition  *orchestration.WorkflowDefinition `json:"definition"`
}

func (s *Server) handleUpdateWorkflowImpl(w http.ResponseWriter, r *http.Request) {
	project, _ := projectFromContext(r.Context())
	wfID, err := uuid.Parse(chi.URLParam(r, "workflowID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid workflow ID")
		return
	}

	var req updateWorkflowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}

	// Load current
	var wf models.Workflow
	err = s.db.QueryRowContext(r.Context(),
		`SELECT id, project_id, name, slug, description, version, definition, is_active, created_by, created_at, updated_at
		 FROM workflows WHERE id=$1 AND project_id=$2`, wfID, project.ID).Scan(
		&wf.ID, &wf.ProjectID, &wf.Name, &wf.Slug, &wf.Description,
		&wf.Version, &wf.Definition, &wf.IsActive, &wf.CreatedBy, &wf.CreatedAt, &wf.UpdatedAt)
	if err != nil {
		writeError(w, http.StatusNotFound, "workflow not found")
		return
	}

	if req.Name != "" {
		wf.Name = req.Name
	}
	if req.Description != "" {
		wf.Description = &req.Description
	}

	var defStr string
	if req.Definition != nil {
		b, _ := json.Marshal(req.Definition)
		defStr = string(b)
		wf.Version++
	} else {
		b, _ := json.Marshal(wf.Definition)
		defStr = string(b)
	}

	_, err = s.db.ExecContext(r.Context(),
		`UPDATE workflows SET name=$1, description=$2, definition=$3, version=$4, updated_at=now() WHERE id=$5`,
		wf.Name, wf.Description, defStr, wf.Version, wf.ID)
	if err != nil {
		log.Error().Err(err).Msg("updateWorkflow: update")
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, wf)
}

// ── Start a workflow run (manual trigger) ─────────────────────

type startWorkflowRunRequest struct {
	Input models.JSONMap `json:"input"`
}

func (s *Server) handleStartWorkflowRun(w http.ResponseWriter, r *http.Request) {
	project, _ := projectFromContext(r.Context())
	user, _ := auth.UserFromContext(r.Context())
	wfID, err := uuid.Parse(chi.URLParam(r, "workflowID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid workflow ID")
		return
	}

	var req startWorkflowRunRequest
	json.NewDecoder(r.Body).Decode(&req)
	if req.Input == nil {
		req.Input = models.JSONMap{}
	}

	run, err := s.scheduler.FireManual(r.Context(), wfID, project.ID, req.Input, &user.ID)
	if err != nil {
		log.Error().Err(err).Msg("startWorkflowRun: fire manual")
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}

// ── Trigger management ────────────────────────────────────────

type createTriggerRequest struct {
	Kind        string `json:"kind"`
	CronExpr    string `json:"cron_expr"`
	WebhookPath string `json:"webhook_path"`
}

func (s *Server) handleCreateTrigger(w http.ResponseWriter, r *http.Request) {
	project, _ := projectFromContext(r.Context())
	wfID, err := uuid.Parse(chi.URLParam(r, "workflowID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid workflow ID")
		return
	}

	// Verify workflow belongs to project
	var exists bool
	s.db.QueryRowContext(r.Context(),
		`SELECT EXISTS(SELECT 1 FROM workflows WHERE id=$1 AND project_id=$2)`,
		wfID, project.ID).Scan(&exists)
	if !exists {
		writeError(w, http.StatusNotFound, "workflow not found")
		return
	}

	var req createTriggerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}

	t := &models.Trigger{
		ID:         uuid.New(),
		WorkflowID: wfID,
		Kind:       models.TriggerKind(req.Kind),
		IsActive:   true,
		Settings:   models.JSONMap{},
		CreatedAt:  time.Now(),
	}
	if req.CronExpr != "" {
		t.CronExpr = &req.CronExpr
	}
	if req.WebhookPath != "" {
		path := fmt.Sprintf("%s-%s", req.WebhookPath, uuid.New().String()[:8])
		t.WebhookPath = &path
	}

	_, err = s.db.ExecContext(r.Context(),
		`INSERT INTO triggers (id, workflow_id, kind, cron_expr, webhook_path, settings, is_active, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		t.ID, t.WorkflowID, t.Kind, t.CronExpr, t.WebhookPath, t.Settings, t.IsActive, t.CreatedAt)
	if err != nil {
		log.Error().Err(err).Msg("createTrigger: insert")
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

// ── Webhook ingest ────────────────────────────────────────────

func (s *Server) handleWebhookTriggerImpl(w http.ResponseWriter, r *http.Request) {
	path := chi.URLParam(r, "triggerPath")

	var payload models.JSONMap
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		payload = models.JSONMap{}
	}

	run, err := s.scheduler.FireWebhook(r.Context(), path, payload)
	if err != nil {
		writeError(w, http.StatusNotFound, "webhook trigger not found: "+err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"run_id": run.ID, "status": run.Status})
}
