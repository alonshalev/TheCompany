package api

// handlers_governance.go — budget, approval, and audit HTTP handlers.
//
// Wired routes (already declared in server.go):
//   GET  /v1/projects/{projectID}/budgets
//   POST /v1/projects/{projectID}/budgets
//   PUT  /v1/projects/{projectID}/budgets/{budgetID}
//
//   GET  /v1/projects/{projectID}/approvals
//   POST /v1/projects/{projectID}/approvals/{approvalID}/resolve
//
//   GET  /v1/projects/{projectID}/audit
//   GET  /v1/projects/{projectID}/audit/verify

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/symbiont-ai/symbiont/internal/auth"
	"github.com/symbiont-ai/symbiont/internal/governance"
	"github.com/symbiont-ai/symbiont/internal/models"
)

// ── Budget handlers ───────────────────────────────────────────

// mustProject is a convenience helper that retrieves the project from context
// or writes a 500 and returns nil so the caller can bail out.
func mustProject(w http.ResponseWriter, r *http.Request) *models.Project {
	proj, ok := projectFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "project context missing")
		return nil
	}
	return proj
}

// GET /v1/projects/{projectID}/budgets
func (s *Server) handleGetBudgets(w http.ResponseWriter, r *http.Request) {
	proj := mustProject(w, r)
	if proj == nil {
		return
	}
	budgets, err := s.budgetMgr.GetBudgets(r.Context(), proj.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if budgets == nil {
		budgets = []*models.Budget{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"budgets": budgets,
		"count":   len(budgets),
	})
}

// POST /v1/projects/{projectID}/budgets
func (s *Server) handleCreateBudget(w http.ResponseWriter, r *http.Request) {
	proj := mustProject(w, r)
	if proj == nil {
		return
	}

	var body struct {
		ScopeType   string  `json:"scope_type"`    // "agent" | "workflow" | "project"
		ScopeID     string  `json:"scope_id"`
		LimitCents  int64   `json:"limit_cents"`
		WarningPct  int     `json:"warning_pct"`
		ResetPeriod string  `json:"reset_period"`  // "monthly" | "weekly" | "never"
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.ScopeType == "" || body.ScopeID == "" || body.LimitCents <= 0 {
		writeError(w, http.StatusBadRequest, "scope_type, scope_id, and limit_cents are required")
		return
	}

	scopeID, err := uuid.Parse(body.ScopeID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid scope_id: "+err.Error())
		return
	}

	warningPct := body.WarningPct
	if warningPct == 0 {
		warningPct = 80
	}
	resetPeriod := body.ResetPeriod
	if resetPeriod == "" {
		resetPeriod = "monthly"
	}

	b := &models.Budget{
		ID:          uuid.New(),
		ProjectID:   proj.ID,
		ScopeType:   body.ScopeType,
		ScopeID:     scopeID,
		LimitCents:  body.LimitCents,
		WarningPct:  warningPct,
		ResetPeriod: resetPeriod,
		IsActive:    true,
	}

	if err = s.budgetMgr.UpsertBudget(r.Context(), b); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Audit
	actor := auth.ActorFromContext(r.Context())
	_ = s.writeAudit(r, actor, &proj.ID, "budget.create", "budget", &b.ID, "success", map[string]any{
		"scope_type":  b.ScopeType,
		"scope_id":    b.ScopeID,
		"limit_cents": b.LimitCents,
	})

	writeJSON(w, http.StatusCreated, b)
}

// PUT /v1/projects/{projectID}/budgets/{budgetID}
func (s *Server) handleUpdateBudget(w http.ResponseWriter, r *http.Request) {
	proj := mustProject(w, r)
	if proj == nil {
		return
	}
	budgetIDStr := chi.URLParam(r, "budgetID")
	budgetID, err := uuid.Parse(budgetIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid budgetID")
		return
	}

	var body struct {
		LimitCents  *int64  `json:"limit_cents"`
		WarningPct  *int    `json:"warning_pct"`
		ResetPeriod *string `json:"reset_period"`
		IsActive    *bool   `json:"is_active"`
	}
	if err = decodeJSONLenient(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Read existing budget to carry over unchanged fields
	budgets, err := s.budgetMgr.GetBudgets(r.Context(), proj.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var found *models.Budget
	for _, b := range budgets {
		if b.ID == budgetID {
			found = b
			break
		}
	}
	if found == nil {
		writeError(w, http.StatusNotFound, "budget not found")
		return
	}

	if body.LimitCents != nil {
		found.LimitCents = *body.LimitCents
	}
	if body.WarningPct != nil {
		found.WarningPct = *body.WarningPct
	}
	if body.ResetPeriod != nil {
		found.ResetPeriod = *body.ResetPeriod
	}
	if body.IsActive != nil {
		found.IsActive = *body.IsActive
	}

	if err = s.budgetMgr.UpsertBudget(r.Context(), found); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, found)
}

// ── Approval handlers ─────────────────────────────────────────

// GET /v1/projects/{projectID}/approvals
func (s *Server) handleListApprovals(w http.ResponseWriter, r *http.Request) {
	proj := mustProject(w, r)
	if proj == nil {
		return
	}

	all := r.URL.Query().Get("all") == "true"
	var (
		reqs []*models.ApprovalRequest
		err  error
	)
	if all {
		limitStr := r.URL.Query().Get("limit")
		limit := 50
		if n, e := strconv.Atoi(limitStr); e == nil && n > 0 && n <= 200 {
			limit = n
		}
		reqs, err = s.approvalResolver.ListAll(r.Context(), proj.ID, limit)
	} else {
		reqs, err = s.approvalResolver.ListPending(r.Context(), proj.ID)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if reqs == nil {
		reqs = []*models.ApprovalRequest{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"approvals": reqs,
		"count":     len(reqs),
	})
}

// POST /v1/projects/{projectID}/approvals/{approvalID}/resolve
func (s *Server) handleResolveApproval(w http.ResponseWriter, r *http.Request) {
	approvalIDStr := chi.URLParam(r, "approvalID")
	approvalID, err := uuid.Parse(approvalIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid approvalID")
		return
	}

	var body struct {
		Approve bool   `json:"approve"`
		Note    string `json:"note"`
	}
	if err = decodeJSONLenient(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	actor := auth.ActorFromContext(r.Context())
	resolved, err := s.approvalResolver.Resolve(r.Context(), approvalID, actor.UserID, body.Approve, body.Note)
	if err != nil {
		switch err {
		case governance.ErrNotFound:
			writeError(w, http.StatusNotFound, "approval request not found")
		case governance.ErrAlreadyResolved:
			writeError(w, http.StatusConflict, "approval request already resolved")
		case governance.ErrExpired:
			writeError(w, http.StatusGone, "approval request has expired")
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	outcome := "success"
	if !body.Approve {
		outcome = "blocked"
	}
	_ = s.writeAudit(r, actor, &resolved.ProjectID, "approval.resolve", "approval_request", &approvalID, outcome, map[string]any{
		"approve": body.Approve,
		"note":    body.Note,
	})

	writeJSON(w, http.StatusOK, resolved)
}

// ── Audit handlers ────────────────────────────────────────────

// GET /v1/projects/{projectID}/audit
func (s *Server) handleListAuditEvents(w http.ResponseWriter, r *http.Request) {
	proj := mustProject(w, r)
	if proj == nil {
		return
	}
	actor := auth.ActorFromContext(r.Context())

	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")
	limit := 100
	offset := 0
	if n, e := strconv.Atoi(limitStr); e == nil && n > 0 && n <= 500 {
		limit = n
	}
	if n, e := strconv.Atoi(offsetStr); e == nil && n >= 0 {
		offset = n
	}

	events, err := s.auditWriter.ListAuditEvents(r.Context(), actor.OrgID, &proj.ID, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if events == nil {
		events = []*models.AuditEvent{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"events": events,
		"count":  len(events),
	})
}

// GET /v1/projects/{projectID}/audit/verify
func (s *Server) handleVerifyAuditChain(w http.ResponseWriter, r *http.Request) {
	actor := auth.ActorFromContext(r.Context())

	err := s.auditWriter.VerifyChain(r.Context(), actor.OrgID)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ── Audit helper ──────────────────────────────────────────────

// writeAudit is a convenience wrapper used by handlers throughout the API
// to record an audit event without having to construct the full entry.
// projectID may be nil for org-level actions.
func (s *Server) writeAudit(
	r *http.Request,
	actor auth.Actor,
	projectID *uuid.UUID,
	action string,
	resourceType string,
	resourceID *uuid.UUID,
	outcome string,
	detail map[string]any,
) error {
	rt := resourceType
	entry := governance.AuditEntry{
		OrgID:        actor.OrgID,
		ProjectID:    projectID,
		ActorType:    actor.Type,
		ActorID:      &actor.UserID,
		ActorName:    actor.Name,
		Action:       action,
		ResourceType: &rt,
		ResourceID:   resourceID,
		Outcome:      outcome,
		Detail:       detail,
	}
	_, err := s.auditWriter.Write(r.Context(), entry)
	return err
}
