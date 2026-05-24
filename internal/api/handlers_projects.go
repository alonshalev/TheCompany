package api

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/symbiont-ai/symbiont/internal/auth"
	"github.com/symbiont-ai/symbiont/internal/models"
)

// ── Context injection ─────────────────────────────────────────

type contextProjectKey struct{}

// projectCtx middleware injects the Project into request context.
func (s *Server) projectCtx(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idStr := chi.URLParam(r, "projectID")
		id, err := uuid.Parse(idStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid project ID")
			return
		}

		user, _ := auth.UserFromContext(r.Context())
		project, err := s.getProjectByID(r.Context(), user.OrgID, id)
		if err != nil {
			if err == sql.ErrNoRows {
				writeError(w, http.StatusNotFound, "project not found")
				return
			}
			log.Error().Err(err).Msg("projectCtx: DB error")
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		ctx := context.WithValue(r.Context(), contextProjectKey{}, project)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func projectFromContext(ctx context.Context) (*models.Project, bool) {
	p, ok := ctx.Value(contextProjectKey{}).(*models.Project)
	return p, ok
}

// ── Handlers ─────────────────────────────────────────────────

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())

	rows, err := s.db.QueryContext(r.Context(),
		`SELECT id, org_id, name, slug, description, budget_cents, spend_cents, settings, created_at, updated_at, archived_at
		 FROM projects WHERE org_id = $1 AND archived_at IS NULL ORDER BY created_at DESC`,
		user.OrgID)
	if err != nil {
		log.Error().Err(err).Msg("listProjects: query")
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer rows.Close()

	var projects []models.Project
	for rows.Next() {
		var p models.Project
		if err := rows.Scan(&p.ID, &p.OrgID, &p.Name, &p.Slug, &p.Description,
			&p.BudgetCents, &p.SpendCents, &p.Settings, &p.CreatedAt, &p.UpdatedAt, &p.ArchivedAt); err != nil {
			log.Error().Err(err).Msg("listProjects: scan")
			continue
		}
		projects = append(projects, p)
	}
	if projects == nil {
		projects = []models.Project{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": projects})
}

type createProjectRequest struct {
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Description string `json:"description"`
	BudgetCents int64  `json:"budget_cents"`
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())

	var req createProjectRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req.Name == "" || req.Slug == "" {
		writeError(w, http.StatusBadRequest, "name and slug are required")
		return
	}

	p := &models.Project{
		ID:          uuid.New(),
		OrgID:       user.OrgID,
		Name:        req.Name,
		Slug:        req.Slug,
		BudgetCents: req.BudgetCents,
		Settings:    models.JSONMap{},
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if req.Description != "" {
		p.Description = &req.Description
	}

	_, err := s.db.ExecContext(r.Context(),
		`INSERT INTO projects (id, org_id, name, slug, description, budget_cents, settings, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		p.ID, p.OrgID, p.Name, p.Slug, p.Description, p.BudgetCents, p.Settings, p.CreatedAt, p.UpdatedAt)
	if err != nil {
		log.Error().Err(err).Msg("createProject: insert")
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	p, _ := projectFromContext(r.Context())
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleUpdateProject(w http.ResponseWriter, r *http.Request) {
	p, _ := projectFromContext(r.Context())

	var req createProjectRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Name != "" {
		p.Name = req.Name
	}
	if req.Description != "" {
		p.Description = &req.Description
	}
	if req.BudgetCents > 0 {
		p.BudgetCents = req.BudgetCents
	}
	p.UpdatedAt = time.Now()

	_, err := s.db.ExecContext(r.Context(),
		`UPDATE projects SET name=$1, description=$2, budget_cents=$3, updated_at=$4 WHERE id=$5`,
		p.Name, p.Description, p.BudgetCents, p.UpdatedAt, p.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleArchiveProject(w http.ResponseWriter, r *http.Request) {
	p, _ := projectFromContext(r.Context())
	now := time.Now()
	_, err := s.db.ExecContext(r.Context(),
		`UPDATE projects SET archived_at=$1, updated_at=$1 WHERE id=$2`, now, p.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── DB helpers ────────────────────────────────────────────────

func (s *Server) getProjectByID(ctx context.Context, orgID, projectID uuid.UUID) (*models.Project, error) {
	var p models.Project
	err := s.db.QueryRowContext(ctx,
		`SELECT id, org_id, name, slug, description, budget_cents, spend_cents, settings, created_at, updated_at, archived_at
		 FROM projects WHERE id=$1 AND org_id=$2 AND archived_at IS NULL`,
		projectID, orgID).Scan(
		&p.ID, &p.OrgID, &p.Name, &p.Slug, &p.Description,
		&p.BudgetCents, &p.SpendCents, &p.Settings, &p.CreatedAt, &p.UpdatedAt, &p.ArchivedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Server) lookupUserByAPIKey(ctx context.Context, keyHash string) (*models.User, error) {
	var u models.User
	err := s.db.QueryRowContext(ctx,
		`SELECT id, org_id, email, name, role, api_key_prefix, created_at, updated_at, last_seen_at
		 FROM users WHERE api_key_hash=$1`, keyHash).
		Scan(&u.ID, &u.OrgID, &u.Email, &u.Name, &u.Role,
			&u.APIKeyPrefix, &u.CreatedAt, &u.UpdatedAt, &u.LastSeenAt)
	if err != nil {
		return nil, err
	}
	// Update last_seen_at (best-effort, don't fail the request)
	now := time.Now()
	go s.db.ExecContext(context.Background(),
		`UPDATE users SET last_seen_at=$1 WHERE id=$2`, now, u.ID)
	return &u, nil
}
