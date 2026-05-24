package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/rs/zerolog/log"
	"github.com/symbiont-ai/symbiont/internal/auth"
	"github.com/symbiont-ai/symbiont/internal/factory"
	"github.com/symbiont-ai/symbiont/internal/models"
)

// ── AgentSpec CRUD ────────────────────────────────────────────

// GET /v1/projects/{projectID}/agents
// Returns the latest version of every active AgentSpec in the project.
func (s *Server) handleListAgentSpecsImpl(w http.ResponseWriter, r *http.Request) {
	project, _ := projectFromContext(r.Context())

	// DISTINCT ON (slug) + ORDER BY slug, version DESC → latest version per slug
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT DISTINCT ON (slug)
		       id, project_id, name, slug, version, role, goal, system_instructions,
		       provider_config_id, model,
		       memory_read_scopes, memory_write_scopes,
		       budget_cents, tool_grants, guardrails,
		       synthesized_from, template_id,
		       is_active, created_by, created_at, updated_at
		FROM agent_specs
		WHERE project_id = $1 AND is_active = true
		ORDER BY slug, version DESC`, project.ID)
	if err != nil {
		log.Error().Err(err).Msg("listAgentSpecs: query")
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer rows.Close()

	specs := []models.AgentSpec{}
	for rows.Next() {
		spec, err := scanAgentSpec(rows)
		if err != nil {
			log.Error().Err(err).Msg("listAgentSpecs: scan")
			continue
		}
		specs = append(specs, *spec)
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": specs, "count": len(specs)})
}

// POST /v1/projects/{projectID}/agents
type createAgentSpecRequest struct {
	Name               string         `json:"name"`
	Slug               string         `json:"slug"`
	Role               string         `json:"role"`
	Goal               string         `json:"goal"`
	SystemInstructions string         `json:"system_instructions"`
	Model              string         `json:"model"`
	MemoryReadScopes   []string       `json:"memory_read_scopes"`
	MemoryWriteScopes  []string       `json:"memory_write_scopes"`
	BudgetCents        int64          `json:"budget_cents"`
	ToolGrants         []any          `json:"tool_grants"`
	Guardrails         map[string]any `json:"guardrails"`
}

func (s *Server) handleCreateAgentSpecImpl(w http.ResponseWriter, r *http.Request) {
	project, _ := projectFromContext(r.Context())
	user, _ := auth.UserFromContext(r.Context())

	var req createAgentSpecRequest
	if err := decodeJSONLenient(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req.Name == "" || req.Role == "" || req.Goal == "" || req.SystemInstructions == "" {
		writeError(w, http.StatusBadRequest, "name, role, goal, system_instructions are required")
		return
	}
	if req.Slug == "" {
		req.Slug = factory.SanitizeSlug(req.Name)
	}
	if req.Model == "" {
		req.Model = "claude-sonnet-4-6"
	}
	if req.BudgetCents <= 0 {
		req.BudgetCents = 500
	}

	now := time.Now()
	toolGrants := models.JSONSlice(req.ToolGrants)
	if toolGrants == nil {
		toolGrants = models.JSONSlice{}
	}
	guardrails := models.JSONMap(req.Guardrails)
	if guardrails == nil {
		guardrails = models.JSONMap{}
	}

	spec := &models.AgentSpec{
		ID:                 uuid.New(),
		ProjectID:          project.ID,
		Name:               req.Name,
		Slug:               req.Slug,
		Version:            1,
		Role:               req.Role,
		Goal:               req.Goal,
		SystemInstructions: req.SystemInstructions,
		Model:              req.Model,
		MemoryReadScopes:   stringsToMemoryScopes(req.MemoryReadScopes, []models.MemoryScope{models.MemoryScopeProject}),
		MemoryWriteScopes:  stringsToMemoryScopes(req.MemoryWriteScopes, []models.MemoryScope{models.MemoryScopeWorkflow}),
		BudgetCents:        req.BudgetCents,
		ToolGrants:         toolGrants,
		Guardrails:         guardrails,
		IsActive:           true,
		CreatedBy:          &user.ID,
		CreatedAt:          now,
		UpdatedAt:          now,
	}

	if err := agentSpecInsert(r.Context(), s.db.DB, spec); err != nil {
		log.Error().Err(err).Msg("createAgentSpec: insert")
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusCreated, spec)
}

// POST /v1/projects/{projectID}/agents/synthesize
type synthesizeAgentRequest struct {
	Prompt     string `json:"prompt"`
	SaveDirect bool   `json:"save_direct"` // if true, persist immediately without review
}

func (s *Server) handleSynthesizeAgentSpecImpl(w http.ResponseWriter, r *http.Request) {
	project, _ := projectFromContext(r.Context())
	user, _ := auth.UserFromContext(r.Context())

	var req synthesizeAgentRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req.Prompt == "" {
		writeError(w, http.StatusBadRequest, "prompt is required")
		return
	}

	spec, err := s.architect.Synthesize(r.Context(), project.ID, req.Prompt)
	if err != nil {
		log.Error().Err(err).Str("prompt", req.Prompt).Msg("synthesizeAgentSpec: architect error")
		writeError(w, http.StatusInternalServerError, "synthesis failed: "+err.Error())
		return
	}
	spec.CreatedBy = &user.ID

	if req.SaveDirect {
		if err := agentSpecInsert(r.Context(), s.db.DB, spec); err != nil {
			log.Error().Err(err).Msg("synthesizeAgentSpec: insert")
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		writeJSON(w, http.StatusCreated, spec)
		return
	}

	// Default: return the draft for human review
	writeJSON(w, http.StatusOK, map[string]any{
		"draft":   spec,
		"message": "Review the draft and POST it to /agents to save, or set save_direct:true to persist immediately.",
	})
}

// GET /v1/projects/{projectID}/agents/{agentSpecID}
func (s *Server) handleGetAgentSpecImpl(w http.ResponseWriter, r *http.Request) {
	project, _ := projectFromContext(r.Context())
	specID, err := uuid.Parse(chi.URLParam(r, "agentSpecID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid agent spec ID")
		return
	}

	spec, err := agentSpecByID(r.Context(), s.db.DB, specID, project.ID)
	if err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "agent spec not found")
			return
		}
		log.Error().Err(err).Msg("getAgentSpec: query")
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, spec)
}

// PUT /v1/projects/{projectID}/agents/{agentSpecID}
// Versioning strategy: insert a new row with version+1; deactivate the old row.
// History is preserved — every version remains queryable by its UUID.
func (s *Server) handleUpdateAgentSpecImpl(w http.ResponseWriter, r *http.Request) {
	project, _ := projectFromContext(r.Context())
	user, _ := auth.UserFromContext(r.Context())
	specID, err := uuid.Parse(chi.URLParam(r, "agentSpecID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid agent spec ID")
		return
	}

	current, err := agentSpecByID(r.Context(), s.db.DB, specID, project.ID)
	if err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "agent spec not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	var req createAgentSpecRequest
	if err := decodeJSONLenient(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	// Build the updated spec by merging — only provided fields overwrite
	updated := *current
	updated.ID = uuid.New()
	updated.Version = current.Version + 1
	updated.UpdatedAt = time.Now()
	updated.CreatedBy = &user.ID

	if req.Name != "" {
		updated.Name = req.Name
	}
	if req.Role != "" {
		updated.Role = req.Role
	}
	if req.Goal != "" {
		updated.Goal = req.Goal
	}
	if req.SystemInstructions != "" {
		updated.SystemInstructions = req.SystemInstructions
	}
	if req.Model != "" {
		updated.Model = req.Model
	}
	if len(req.MemoryReadScopes) > 0 {
		updated.MemoryReadScopes = stringsToMemoryScopes(req.MemoryReadScopes, current.MemoryReadScopes)
	}
	if len(req.MemoryWriteScopes) > 0 {
		updated.MemoryWriteScopes = stringsToMemoryScopes(req.MemoryWriteScopes, current.MemoryWriteScopes)
	}
	if req.BudgetCents > 0 {
		updated.BudgetCents = req.BudgetCents
	}
	if req.ToolGrants != nil {
		updated.ToolGrants = models.JSONSlice(req.ToolGrants)
	}
	if req.Guardrails != nil {
		updated.Guardrails = models.JSONMap(req.Guardrails)
	}

	// Embed audit trail in guardrails — previous version pointer
	if updated.Guardrails == nil {
		updated.Guardrails = models.JSONMap{}
	}
	updated.Guardrails["_prev_version"] = current.Version
	updated.Guardrails["_prev_id"] = current.ID.String()

	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer tx.Rollback()

	// Deactivate old version
	if _, err := tx.ExecContext(r.Context(),
		`UPDATE agent_specs SET is_active = false, updated_at = now() WHERE id = $1`, current.ID); err != nil {
		log.Error().Err(err).Msg("updateAgentSpec: deactivate old")
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Insert new version
	if err := agentSpecInsertTx(r.Context(), tx, &updated); err != nil {
		log.Error().Err(err).Msg("updateAgentSpec: insert new version")
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, &updated)
}

// POST /v1/projects/{projectID}/agents/{agentSpecID}/instantiate
func (s *Server) handleInstantiateAgentImpl(w http.ResponseWriter, r *http.Request) {
	project, _ := projectFromContext(r.Context())
	specID, err := uuid.Parse(chi.URLParam(r, "agentSpecID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid agent spec ID")
		return
	}

	spec, err := agentSpecByID(r.Context(), s.db.DB, specID, project.ID)
	if err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "agent spec not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	var body struct {
		Name string `json:"name"`
	}
	_ = decodeJSON(r, &body)
	if body.Name == "" {
		body.Name = fmt.Sprintf("%s-instance-%s", spec.Slug, time.Now().Format("20060102-150405"))
	}

	now := time.Now()
	instance := &models.AgentInstance{
		ID:        uuid.New(),
		ProjectID: project.ID,
		SpecID:    spec.ID,
		Name:      body.Name,
		IsActive:  true,
		CreatedAt: now,
		UpdatedAt: now,
	}

	_, err = s.db.ExecContext(r.Context(),
		`INSERT INTO agent_instances (id, project_id, spec_id, name, spend_cents, is_active, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,0,$5,$6,$7)`,
		instance.ID, instance.ProjectID, instance.SpecID, instance.Name,
		instance.IsActive, instance.CreatedAt, instance.UpdatedAt)
	if err != nil {
		log.Error().Err(err).Msg("instantiateAgent: insert")
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusCreated, instance)
}

// GET /v1/projects/{projectID}/agents/templates
func (s *Server) handleListAgentTemplates(w http.ResponseWriter, r *http.Request) {
	tpls := factory.Templates()
	out := make([]map[string]any, len(tpls))
	for i, t := range tpls {
		out[i] = map[string]any{
			"id":          t.ID,
			"description": t.Description,
			"spec":        t.Spec,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": out, "count": len(out)})
}

// POST /v1/projects/{projectID}/agents/templates/{templateID}/instantiate
func (s *Server) handleInstantiateTemplate(w http.ResponseWriter, r *http.Request) {
	project, _ := projectFromContext(r.Context())
	user, _ := auth.UserFromContext(r.Context())
	templateID := chi.URLParam(r, "templateID")

	tpl, ok := factory.TemplateByID(templateID)
	if !ok {
		writeError(w, http.StatusNotFound, "template not found")
		return
	}

	spec := factory.InstantiateTemplate(tpl, project.ID, user.ID)
	if err := agentSpecInsert(r.Context(), s.db.DB, spec); err != nil {
		log.Error().Err(err).Str("template", templateID).Msg("instantiateTemplate: insert")
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusCreated, spec)
}

// ── DB helpers ────────────────────────────────────────────────

// agentSpecScanner is satisfied by both *sql.Row and *sql.Rows.
type agentSpecScanner interface {
	Scan(dest ...any) error
}

// scanAgentSpec scans a single AgentSpec from a row/rows result.
func scanAgentSpec(row agentSpecScanner) (*models.AgentSpec, error) {
	var spec models.AgentSpec
	var readScopes, writeScopes pq.StringArray

	err := row.Scan(
		&spec.ID, &spec.ProjectID, &spec.Name, &spec.Slug, &spec.Version,
		&spec.Role, &spec.Goal, &spec.SystemInstructions,
		&spec.ProviderConfigID, &spec.Model,
		&readScopes, &writeScopes,
		&spec.BudgetCents, &spec.ToolGrants, &spec.Guardrails,
		&spec.SynthesizedFrom, &spec.TemplateID,
		&spec.IsActive, &spec.CreatedBy, &spec.CreatedAt, &spec.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	spec.MemoryReadScopes = stringSliceToMemoryScopes(readScopes)
	spec.MemoryWriteScopes = stringSliceToMemoryScopes(writeScopes)
	return &spec, nil
}

// agentSpecByID fetches a single AgentSpec by its UUID, scoped to a project.
func agentSpecByID(ctx context.Context, db *sql.DB, specID, projectID uuid.UUID) (*models.AgentSpec, error) {
	row := db.QueryRowContext(ctx, `
		SELECT id, project_id, name, slug, version, role, goal, system_instructions,
		       provider_config_id, model,
		       memory_read_scopes, memory_write_scopes,
		       budget_cents, tool_grants, guardrails,
		       synthesized_from, template_id,
		       is_active, created_by, created_at, updated_at
		FROM agent_specs
		WHERE id = $1 AND project_id = $2`, specID, projectID)
	return scanAgentSpec(row)
}

// agentSpecInsert inserts a new AgentSpec into the DB.
func agentSpecInsert(ctx context.Context, db *sql.DB, spec *models.AgentSpec) error {
	readScopes := memoryScapesToStringSlice(spec.MemoryReadScopes)
	writeScopes := memoryScapesToStringSlice(spec.MemoryWriteScopes)

	_, err := db.ExecContext(ctx, `
		INSERT INTO agent_specs (
		    id, project_id, name, slug, version, role, goal, system_instructions,
		    provider_config_id, model,
		    memory_read_scopes, memory_write_scopes,
		    budget_cents, tool_grants, guardrails,
		    synthesized_from, template_id,
		    is_active, created_by, created_at, updated_at
		) VALUES (
		    $1,$2,$3,$4,$5,$6,$7,$8,
		    $9,$10,
		    $11::memory_scope[],$12::memory_scope[],
		    $13,$14,$15,
		    $16,$17,
		    $18,$19,$20,$21
		)`,
		spec.ID, spec.ProjectID, spec.Name, spec.Slug, spec.Version,
		spec.Role, spec.Goal, spec.SystemInstructions,
		spec.ProviderConfigID, spec.Model,
		pq.Array(readScopes), pq.Array(writeScopes),
		spec.BudgetCents, spec.ToolGrants, spec.Guardrails,
		spec.SynthesizedFrom, spec.TemplateID,
		spec.IsActive, spec.CreatedBy, spec.CreatedAt, spec.UpdatedAt,
	)
	return err
}

// agentSpecInsertTx inserts an AgentSpec within an existing transaction.
func agentSpecInsertTx(ctx context.Context, tx *sql.Tx, spec *models.AgentSpec) error {
	readScopes := memoryScapesToStringSlice(spec.MemoryReadScopes)
	writeScopes := memoryScapesToStringSlice(spec.MemoryWriteScopes)

	_, err := tx.ExecContext(ctx, `
		INSERT INTO agent_specs (
		    id, project_id, name, slug, version, role, goal, system_instructions,
		    provider_config_id, model,
		    memory_read_scopes, memory_write_scopes,
		    budget_cents, tool_grants, guardrails,
		    synthesized_from, template_id,
		    is_active, created_by, created_at, updated_at
		) VALUES (
		    $1,$2,$3,$4,$5,$6,$7,$8,
		    $9,$10,
		    $11::memory_scope[],$12::memory_scope[],
		    $13,$14,$15,
		    $16,$17,
		    $18,$19,$20,$21
		)`,
		spec.ID, spec.ProjectID, spec.Name, spec.Slug, spec.Version,
		spec.Role, spec.Goal, spec.SystemInstructions,
		spec.ProviderConfigID, spec.Model,
		pq.Array(readScopes), pq.Array(writeScopes),
		spec.BudgetCents, spec.ToolGrants, spec.Guardrails,
		spec.SynthesizedFrom, spec.TemplateID,
		spec.IsActive, spec.CreatedBy, spec.CreatedAt, spec.UpdatedAt,
	)
	return err
}

// ── Scope conversion helpers ──────────────────────────────────

func stringsToMemoryScopes(raw []string, defaults []models.MemoryScope) []models.MemoryScope {
	valid := map[string]bool{
		"private": true, "agent": true, "workflow": true,
		"project": true, "global": true,
	}
	out := make([]models.MemoryScope, 0, len(raw))
	for _, s := range raw {
		if valid[s] {
			out = append(out, models.MemoryScope(s))
		}
	}
	if len(out) == 0 {
		return defaults
	}
	return out
}

func stringSliceToMemoryScopes(ss []string) []models.MemoryScope {
	out := make([]models.MemoryScope, len(ss))
	for i, s := range ss {
		out[i] = models.MemoryScope(s)
	}
	return out
}

func memoryScapesToStringSlice(scopes []models.MemoryScope) []string {
	out := make([]string, len(scopes))
	for i, s := range scopes {
		out[i] = string(s)
	}
	return out
}

// decodeJSONLenient decodes JSON from the request body without rejecting unknown fields.
// Used for agent create/update endpoints where clients may POST the full synthesized
// spec draft (which includes server-generated fields like id, created_at, etc.).
func decodeJSONLenient(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}
