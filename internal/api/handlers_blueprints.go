package api

// handlers_blueprints.go — Blueprint CRUD, instantiation, and marketplace endpoints.
//
// Wired routes (in server.go):
//   GET    /v1/blueprints                          → list system + org blueprints
//   POST   /v1/blueprints                          → create custom blueprint
//   GET    /v1/blueprints/{blueprintID}             → get blueprint
//   POST   /v1/projects/{projectID}/blueprints/instantiate  → instantiate into project
//   GET    /v1/marketplace                          → list marketplace plugins
//   GET    /v1/marketplace/{pluginSlug}             → get plugin details

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/symbiont-ai/symbiont/internal/auth"
	"github.com/symbiont-ai/symbiont/internal/models"
)

// ── Wire types ────────────────────────────────────────────────

type Blueprint struct {
	ID          uuid.UUID       `json:"id"`
	OrgID       *uuid.UUID      `json:"org_id,omitempty"` // nil = system
	Name        string          `json:"name"`
	Slug        string          `json:"slug"`
	Description string          `json:"description"`
	Category    string          `json:"category"`
	Version     int             `json:"version"`
	Components  json.RawMessage `json:"components"`
	Tags        []string        `json:"tags"`
	IsActive    bool            `json:"is_active"`
	CreatedBy   *uuid.UUID      `json:"created_by,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

type MarketplacePlugin struct {
	ID           uuid.UUID       `json:"id"`
	Name         string          `json:"name"`
	Slug         string          `json:"slug"`
	Description  string          `json:"description"`
	Author       string          `json:"author"`
	Version      string          `json:"version"`
	Category     string          `json:"category"`
	Manifest     json.RawMessage `json:"manifest"`
	IconURL      *string         `json:"icon_url,omitempty"`
	DocsURL      *string         `json:"docs_url,omitempty"`
	IsVerified   bool            `json:"is_verified"`
	InstallCount int             `json:"install_count"`
	CreatedAt    time.Time       `json:"created_at"`
}

// ── Blueprint handlers ────────────────────────────────────────

// GET /v1/blueprints
// Returns system blueprints (org_id IS NULL) + the authenticated org's custom blueprints.
func (s *Server) handleListBlueprintsImpl(w http.ResponseWriter, r *http.Request) {
	actor := auth.ActorFromContext(r.Context())

	rows, err := s.db.QueryContext(r.Context(),
		`SELECT id, org_id, name, slug, description, category, version, components,
		        tags, is_active, created_by, created_at, updated_at
		 FROM blueprints
		 WHERE is_active = true
		   AND (org_id IS NULL OR org_id = $1)
		 ORDER BY org_id NULLS FIRST, category, name`,
		actor.OrgID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	var blueprints []Blueprint
	for rows.Next() {
		var bp Blueprint
		var tagsArr []string
		if err := rows.Scan(
			&bp.ID, &bp.OrgID, &bp.Name, &bp.Slug, &bp.Description,
			&bp.Category, &bp.Version, &bp.Components,
			pqArray(&tagsArr), &bp.IsActive, &bp.CreatedBy,
			&bp.CreatedAt, &bp.UpdatedAt,
		); err != nil {
			log.Error().Err(err).Msg("handleListBlueprints: scan")
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		bp.Tags = tagsArr
		blueprints = append(blueprints, bp)
	}
	if blueprints == nil {
		blueprints = []Blueprint{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"blueprints": blueprints, "count": len(blueprints)})
}

// POST /v1/blueprints
// Creates a custom blueprint scoped to the authenticated org.
func (s *Server) handleCreateBlueprint(w http.ResponseWriter, r *http.Request) {
	actor := auth.ActorFromContext(r.Context())

	var body struct {
		Name        string          `json:"name"`
		Slug        string          `json:"slug"`
		Description string          `json:"description"`
		Category    string          `json:"category"`
		Components  json.RawMessage `json:"components"`
		Tags        []string        `json:"tags"`
	}
	if err := decodeJSONLenient(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Name == "" || body.Slug == "" {
		writeError(w, http.StatusBadRequest, "name and slug are required")
		return
	}
	if body.Category == "" {
		body.Category = "custom"
	}
	if body.Components == nil {
		body.Components = json.RawMessage(`{}`)
	}
	if body.Tags == nil {
		body.Tags = []string{}
	}

	bp := Blueprint{
		ID:          uuid.New(),
		OrgID:       &actor.OrgID,
		Name:        body.Name,
		Slug:        body.Slug,
		Description: body.Description,
		Category:    body.Category,
		Version:     1,
		Components:  body.Components,
		Tags:        body.Tags,
		IsActive:    true,
		CreatedBy:   &actor.UserID,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	_, err := s.db.ExecContext(r.Context(),
		`INSERT INTO blueprints
		   (id, org_id, name, slug, description, category, version, components,
		    tags, is_active, created_by, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::text[],$10,$11,$12,$12)`,
		bp.ID, bp.OrgID, bp.Name, bp.Slug, bp.Description,
		bp.Category, bp.Version, bp.Components,
		pqArray(bp.Tags), bp.IsActive, bp.CreatedBy,
		bp.CreatedAt,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Audit
	_ = s.writeAudit(r, actor, nil, "blueprint.create", "blueprint", &bp.ID, "success", map[string]any{
		"slug": bp.Slug, "category": bp.Category,
	})

	writeJSON(w, http.StatusCreated, bp)
}

// GET /v1/blueprints/{blueprintID}
func (s *Server) handleGetBlueprint(w http.ResponseWriter, r *http.Request) {
	blueprintIDStr := chi.URLParam(r, "blueprintID")
	blueprintID, err := uuid.Parse(blueprintIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid blueprintID")
		return
	}

	var bp Blueprint
	var tagsArr []string
	err = s.db.QueryRowContext(r.Context(),
		`SELECT id, org_id, name, slug, description, category, version, components,
		        tags, is_active, created_by, created_at, updated_at
		 FROM blueprints WHERE id = $1 AND is_active = true`,
		blueprintID,
	).Scan(
		&bp.ID, &bp.OrgID, &bp.Name, &bp.Slug, &bp.Description,
		&bp.Category, &bp.Version, &bp.Components,
		pqArray(&tagsArr), &bp.IsActive, &bp.CreatedBy,
		&bp.CreatedAt, &bp.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "blueprint not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	bp.Tags = tagsArr
	writeJSON(w, http.StatusOK, bp)
}

// POST /v1/projects/{projectID}/blueprints/instantiate
// Instantiates a blueprint into the project: creates agent specs and workflows
// as defined in the blueprint's components field.
func (s *Server) handleInstantiateBlueprint(w http.ResponseWriter, r *http.Request) {
	proj := mustProject(w, r)
	if proj == nil {
		return
	}
	actor := auth.ActorFromContext(r.Context())

	var body struct {
		BlueprintID string `json:"blueprint_id"`
	}
	if err := decodeJSONLenient(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	blueprintID, err := uuid.Parse(body.BlueprintID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid blueprint_id")
		return
	}

	// Load blueprint
	var bp Blueprint
	var tagsArr []string
	err = s.db.QueryRowContext(r.Context(),
		`SELECT id, org_id, name, slug, description, category, version, components,
		        tags, is_active, created_by, created_at, updated_at
		 FROM blueprints WHERE id = $1 AND is_active = true`,
		blueprintID,
	).Scan(
		&bp.ID, &bp.OrgID, &bp.Name, &bp.Slug, &bp.Description,
		&bp.Category, &bp.Version, &bp.Components,
		pqArray(&tagsArr), &bp.IsActive, &bp.CreatedBy,
		&bp.CreatedAt, &bp.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "blueprint not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	bp.Tags = tagsArr

	// Parse components
	var components struct {
		AgentSpecs []map[string]any `json:"agent_specs"`
		Workflows  []map[string]any `json:"workflows"`
	}
	if err := json.Unmarshal(bp.Components, &components); err != nil {
		writeError(w, http.StatusInternalServerError, "invalid blueprint components: "+err.Error())
		return
	}

	var createdAgentSpecIDs []string
	var createdWorkflowIDs []string
	now := time.Now()

	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer tx.Rollback()

	// Create agent specs
	for _, specDef := range components.AgentSpecs {
		specID := uuid.New()
		name, _ := specDef["name"].(string)
		slug, _ := specDef["slug"].(string)
		role, _ := specDef["role"].(string)
		goal, _ := specDef["goal"].(string)
		model, _ := specDef["model"].(string)
		if model == "" {
			model = "claude-sonnet-4-6"
		}

		toolGrants := models.JSONSlice{}
		if tg, ok := specDef["tool_grants"].([]interface{}); ok {
			for _, t := range tg {
				if s, ok := t.(string); ok {
					toolGrants = append(toolGrants, s)
				}
			}
		}

		guardrails := models.JSONMap{"_blueprint_source": bp.ID.String()}

		_, err := tx.ExecContext(r.Context(),
			`INSERT INTO agent_specs
			   (id, project_id, name, slug, version, role, goal, system_instructions,
			    model, memory_read_scopes, memory_write_scopes, budget_cents,
			    tool_grants, guardrails, is_active, created_by, created_at, updated_at)
			 VALUES
			   ($1,$2,$3,$4,1,$5,$6,'',$7,
			    ARRAY['project']::memory_scope[], ARRAY['project']::memory_scope[],
			    0,$8,$9,true,$10,$11,$11)`,
			specID, proj.ID, name, slug, role, goal, model,
			toolGrants, guardrails, actor.UserID, now,
		)
		if err != nil {
			log.Error().Err(err).Str("name", name).Msg("instantiate blueprint: create agent spec")
			writeError(w, http.StatusInternalServerError, "failed to create agent spec: "+err.Error())
			return
		}
		createdAgentSpecIDs = append(createdAgentSpecIDs, specID.String())
	}

	// Create workflows
	for _, wfDef := range components.Workflows {
		wfID := uuid.New()
		name, _ := wfDef["name"].(string)
		slug, _ := wfDef["slug"].(string)
		desc, _ := wfDef["description"].(string)

		defJSON, err := json.Marshal(wfDef["definition"])
		if err != nil {
			defJSON = []byte(`{"nodes":[],"edges":[]}`)
		}

		_, err = tx.ExecContext(r.Context(),
			`INSERT INTO workflows
			   (id, project_id, name, slug, description, version, definition,
			    is_active, created_by, created_at, updated_at)
			 VALUES ($1,$2,$3,$4,$5,1,$6,true,$7,$8,$8)`,
			wfID, proj.ID, name, slug, desc, defJSON, actor.UserID, now,
		)
		if err != nil {
			log.Error().Err(err).Str("name", name).Msg("instantiate blueprint: create workflow")
			writeError(w, http.StatusInternalServerError, "failed to create workflow: "+err.Error())
			return
		}
		createdWorkflowIDs = append(createdWorkflowIDs, wfID.String())
	}

	// Record blueprint instance
	instanceID := uuid.New()
	createdResources := map[string]any{
		"agent_spec_ids": createdAgentSpecIDs,
		"workflow_ids":   createdWorkflowIDs,
	}
	createdResourcesJSON, _ := json.Marshal(createdResources)

	_, err = tx.ExecContext(r.Context(),
		`INSERT INTO blueprint_instances
		   (id, project_id, blueprint_id, blueprint_version, created_resources, instantiated_by, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		instanceID, proj.ID, blueprintID, bp.Version, createdResourcesJSON, actor.UserID, now,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	_ = s.writeAudit(r, actor, &proj.ID, "blueprint.instantiate", "blueprint", &blueprintID, "success", map[string]any{
		"agent_specs_created": len(createdAgentSpecIDs),
		"workflows_created":   len(createdWorkflowIDs),
	})

	writeJSON(w, http.StatusCreated, map[string]any{
		"instance_id":      instanceID,
		"blueprint_id":     blueprintID,
		"blueprint_name":   bp.Name,
		"created_resources": createdResources,
	})
}

// ── Marketplace handlers ──────────────────────────────────────

// GET /v1/marketplace
func (s *Server) handleListMarketplacePlugins(w http.ResponseWriter, r *http.Request) {
	category := r.URL.Query().Get("category")

	query := `SELECT id, name, slug, description, author, version, category, manifest,
	                 icon_url, docs_url, is_verified, install_count, created_at
	          FROM marketplace_plugins
	          WHERE is_active = true`
	args := []any{}

	if category != "" {
		query += ` AND category = $1`
		args = append(args, category)
	}
	query += ` ORDER BY is_verified DESC, install_count DESC, name`

	rows, err := s.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	var plugins []MarketplacePlugin
	for rows.Next() {
		var p MarketplacePlugin
		if err := rows.Scan(
			&p.ID, &p.Name, &p.Slug, &p.Description, &p.Author, &p.Version,
			&p.Category, &p.Manifest, &p.IconURL, &p.DocsURL,
			&p.IsVerified, &p.InstallCount, &p.CreatedAt,
		); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		plugins = append(plugins, p)
	}
	if plugins == nil {
		plugins = []MarketplacePlugin{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"plugins": plugins, "count": len(plugins)})
}

// GET /v1/marketplace/{pluginSlug}
func (s *Server) handleGetMarketplacePlugin(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "pluginSlug")

	var p MarketplacePlugin
	err := s.db.QueryRowContext(r.Context(),
		`SELECT id, name, slug, description, author, version, category, manifest,
		        icon_url, docs_url, is_verified, install_count, created_at
		 FROM marketplace_plugins WHERE slug = $1 AND is_active = true`,
		slug,
	).Scan(
		&p.ID, &p.Name, &p.Slug, &p.Description, &p.Author, &p.Version,
		&p.Category, &p.Manifest, &p.IconURL, &p.DocsURL,
		&p.IsVerified, &p.InstallCount, &p.CreatedAt,
	)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "plugin not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// ── Helper: pqArray wraps a string slice for lib/pq array scanning ──

// pqArray wraps a *[]string for scanning PostgreSQL text[] columns.
// It lives here rather than a shared util to avoid circular imports.
type pqStringArray struct{ dst *[]string }

func pqArray(dst *[]string) *pqStringArray { return &pqStringArray{dst} }

func (a *pqStringArray) Scan(src any) error {
	if src == nil {
		*a.dst = []string{}
		return nil
	}
	switch v := src.(type) {
	case []byte:
		return parsePGArray(string(v), a.dst)
	case string:
		return parsePGArray(v, a.dst)
	}
	return nil
}

// parsePGArray parses a PostgreSQL text array literal like {"a","b","c"}.
func parsePGArray(s string, dst *[]string) error {
	s = strings.TrimSpace(s)
	if s == "{}" || s == "" {
		*dst = []string{}
		return nil
	}
	// Strip outer braces
	if len(s) >= 2 && s[0] == '{' && s[len(s)-1] == '}' {
		s = s[1 : len(s)-1]
	}
	// Split on commas, respecting quoted strings
	var result []string
	var cur []byte
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"' && !inQuote:
			inQuote = true
		case c == '"' && inQuote:
			inQuote = false
		case c == ',' && !inQuote:
			result = append(result, string(cur))
			cur = cur[:0]
		default:
			cur = append(cur, c)
		}
	}
	result = append(result, string(cur))
	*dst = result
	return nil
}
