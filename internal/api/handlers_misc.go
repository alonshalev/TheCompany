package api

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/symbiont-ai/symbiont/internal/auth"
	"github.com/symbiont-ai/symbiont/internal/models"
)

// GET /healthz
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// GET /readyz — checks DB connectivity
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if err := s.db.PingContext(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unhealthy", "reason": "db"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// POST /v1/setup/init — one-time bootstrap: create org + owner + first API key
type setupInitRequest struct {
	OrgName string `json:"org_name"`
	OrgSlug string `json:"org_slug"`
	UserName string `json:"user_name"`
	Email    string `json:"email"`
}

func (s *Server) handleSetupInit(w http.ResponseWriter, r *http.Request) {
	// Refuse if any org already exists (single-tenant bootstrap only)
	var count int
	if err := s.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM organizations`).Scan(&count); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if count > 0 {
		writeError(w, http.StatusConflict, "system already initialised — use your API key to authenticate")
		return
	}

	var req setupInitRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	if req.OrgName == "" || req.OrgSlug == "" || req.Email == "" || req.UserName == "" {
		writeError(w, http.StatusBadRequest, "org_name, org_slug, user_name, email are required")
		return
	}

	// Generate API key
	plaintext, hash, prefix, err := auth.GenerateAPIKey()
	if err != nil {
		log.Error().Err(err).Msg("setup: generate API key")
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer tx.Rollback()

	orgID := uuid.New()
	userID := uuid.New()
	now := time.Now()

	if _, err := tx.ExecContext(r.Context(),
		`INSERT INTO organizations (id, name, slug, created_at, updated_at) VALUES ($1,$2,$3,$4,$4)`,
		orgID, req.OrgName, req.OrgSlug, now); err != nil {
		log.Error().Err(err).Msg("setup: insert org")
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	role := models.UserRoleOwner
	if _, err := tx.ExecContext(r.Context(),
		`INSERT INTO users (id, org_id, email, name, role, api_key_hash, api_key_prefix, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8)`,
		userID, orgID, req.Email, req.UserName, role, hash, prefix, now); err != nil {
		log.Error().Err(err).Msg("setup: insert user")
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"org_id":  orgID,
		"user_id": userID,
		"api_key": plaintext, // shown ONCE — never stored in plaintext
		"warning": "Save your API key now — it will not be shown again.",
	})
}

// ── Stub handlers (implemented in later Parts) ────────────────

// Agent spec handlers — fully implemented in handlers_agents.go (Part 4)
// Workflow handlers — implemented in handlers_workflows.go (Part 2)
func (s *Server) handleListWorkflows(w http.ResponseWriter, r *http.Request)  { s.handleListWorkflowsImpl(w, r) }
func (s *Server) handleCreateWorkflow(w http.ResponseWriter, r *http.Request) { s.handleCreateWorkflowImpl(w, r) }
func (s *Server) handleGetWorkflow(w http.ResponseWriter, r *http.Request)    { s.handleGetWorkflowImpl(w, r) }
func (s *Server) handleUpdateWorkflow(w http.ResponseWriter, r *http.Request) { s.handleUpdateWorkflowImpl(w, r) }
// handleListRuns returns all runs for the project, ordered by creation time desc.
func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	project, _ := projectFromContext(r.Context())
	rows, err := s.db.QueryContext(r.Context(),
		`SELECT id, project_id, workflow_id, agent_instance_id, status, input, output,
		        error_message, spend_cents, started_at, completed_at, created_at, updated_at
		 FROM runs WHERE project_id = $1 ORDER BY created_at DESC LIMIT 100`,
		project.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	var runs []models.Run
	for rows.Next() {
		var run models.Run
		if err := rows.Scan(
			&run.ID, &run.ProjectID, &run.WorkflowID, &run.AgentInstanceID,
			&run.Status, &run.Input, &run.Output, &run.Error,
			&run.SpendCents, &run.StartedAt, &run.CompletedAt,
			&run.CreatedAt, &run.UpdatedAt,
		); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		runs = append(runs, run)
	}
	if runs == nil {
		runs = []models.Run{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs, "count": len(runs)})
}

func (s *Server) handleStartRun(w http.ResponseWriter, r *http.Request)          { s.handleStartRunImpl(w, r) }
func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request)            { s.handleGetRunImpl(w, r) }
func (s *Server) handleGetRunEvents(w http.ResponseWriter, r *http.Request)      { s.handleGetRunEventsImpl(w, r) }
func (s *Server) handleCancelRun(w http.ResponseWriter, r *http.Request)         { writeJSON(w, 501, map[string]string{"error": "not yet implemented"}) }
func (s *Server) handleStreamRun(w http.ResponseWriter, r *http.Request)         { s.handleStreamRunImpl(w, r) }

// Memory handlers — implemented in handlers_memory.go (Part 3)
func (s *Server) handleQueryMemory(w http.ResponseWriter, r *http.Request)       { s.handleQueryMemoryImpl(w, r) }
func (s *Server) handleWriteMemory(w http.ResponseWriter, r *http.Request)       { s.handleWriteMemoryImpl(w, r) }
func (s *Server) handleGetKnowledgeGraph(w http.ResponseWriter, r *http.Request) { s.handle