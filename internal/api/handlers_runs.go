package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/symbiont-ai/symbiont/internal/agent"
	"github.com/symbiont-ai/symbiont/internal/auth"
	"github.com/symbiont-ai/symbiont/internal/models"
)

// POST /v1/projects/{projectID}/runs
// Starts a direct agent run (no workflow — plain agent + task).
type startRunRequest struct {
	AgentInstanceID string         `json:"agent_instance_id"`
	Input           models.JSONMap `json:"input"`
}

func (s *Server) handleStartRunImpl(w http.ResponseWriter, r *http.Request) {
	project, _ := projectFromContext(r.Context())
	user, _ := auth.UserFromContext(r.Context())

	var req startRunRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	if req.AgentInstanceID == "" {
		writeError(w, http.StatusBadRequest, "agent_instance_id is required")
		return
	}

	instanceID, err := uuid.Parse(req.AgentInstanceID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid agent_instance_id")
		return
	}

	// Fetch the agent instance (must belong to this project)
	var instance models.AgentInstance
	err = s.db.QueryRowContext(r.Context(),
		`SELECT id, project_id, spec_id, name, spend_cents, is_active, created_at, updated_at
		 FROM agent_instances WHERE id=$1 AND project_id=$2 AND is_active=true`,
		instanceID, project.ID).Scan(
		&instance.ID, &instance.ProjectID, &instance.SpecID, &instance.Name,
		&instance.SpendCents, &instance.IsActive, &instance.CreatedAt, &instance.UpdatedAt)
	if err != nil {
		writeError(w, http.StatusNotFound, "agent instance not found")
		return
	}

	// Fetch the spec
	var spec models.AgentSpec
	err = s.db.QueryRowContext(r.Context(),
		`SELECT id, project_id, name, slug, version, role, goal, system_instructions,
		        provider_config_id, model, budget_cents, tool_grants, guardrails
		 FROM agent_specs WHERE id=$1`,
		instance.SpecID).Scan(
		&spec.ID, &spec.ProjectID, &spec.Name, &spec.Slug, &spec.Version,
		&spec.Role, &spec.Goal, &spec.SystemInstructions,
		&spec.ProviderConfigID, &spec.Model, &spec.BudgetCents, &spec.ToolGrants, &spec.Guardrails)
	if err != nil {
		log.Error().Err(err).Msg("handleStartRun: fetch spec")
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if req.Input == nil {
		req.Input = models.JSONMap{}
	}

	run := &models.Run{
		ID:              uuid.New(),
		ProjectID:       project.ID,
		AgentInstanceID: &instance.ID,
		Input:           req.Input,
		Status:          models.RunStatusPending,
		CreatedAt:       time.Now(),
		InitiatedBy:     &user.ID,
	}

	if err := s.runtime.CreateRun(r.Context(), run); err != nil {
		log.Error().Err(err).Msg("handleStartRun: create run")
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Launch agent asynchronously
	go func() {
		provider, ok := s.providerRegistry.Default()
		if !ok {
			log.Error().Msg("handleStartRun: no default provider")
			return
		}

		cfg := agent.RunConfig{
			Run:      run,
			Spec:     &spec,
			Instance: &instance,
			Provider: provider,
			// Tools will be resolved from the tool gateway in Part 2
			// Memory context will be injected in Part 3
		}
		if err := s.runtime.Execute(r.Context(), cfg); err != nil {
			log.Error().Err(err).Str("run_id", run.ID.String()).Msg("agent run error")
		}
	}()

	writeJSON(w, http.StatusAccepted, run)
}

// GET /v1/projects/{projectID}/runs/{runID}
func (s *Server) handleGetRunImpl(w http.ResponseWriter, r *http.Request) {
	project, _ := projectFromContext(r.Context())
	runIDStr := chi.URLParam(r, "runID")
	runID, err := uuid.Parse(runIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid run ID")
		return
	}

	run, err := s.runtime.GetRun(r.Context(), runID)
	if err != nil {
		log.Error().Err(err).Msg("handleGetRun: DB error")
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if run == nil || run.ProjectID != project.ID {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// GET /v1/projects/{projectID}/runs/{runID}/events
func (s *Server) handleGetRunEventsImpl(w http.ResponseWriter, r *http.Request) {
	project, _ := projectFromContext(r.Context())
	runIDStr := chi.URLParam(r, "runID")
	runID, err := uuid.Parse(runIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid run ID")
		return
	}

	// Verify run belongs to project
	run, err := s.runtime.GetRun(r.Context(), runID)
	if err != nil || run == nil || run.ProjectID != project.ID {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}

	events, err := s.runtime.GetRunEvents(r.Context(), runID)
	if err != nil {
		log.Error().Err(err).Msg("handleGetRunEvents: DB error")
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if events == nil {
		events = []models.Event{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}
