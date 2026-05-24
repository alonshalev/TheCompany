package api

import (
	"encoding/json"
	"net/http"

	"github.com/rs/zerolog/log"
	"github.com/symbiont-ai/symbiont/internal/memory"
	"github.com/symbiont-ai/symbiont/internal/models"
)

// POST /v1/projects/{projectID}/memory
type writeMemoryRequest struct {
	Type      string         `json:"type"`       // working|episodic|semantic|procedural
	Scope     string         `json:"scope"`      // private|agent|workflow|project|global
	Content   string         `json:"content"`
	Metadata  map[string]any `json:"metadata"`
	TrustTier string         `json:"trust_tier"` // verified|observed|untrusted
	Confidence float64       `json:"confidence"`
}

func (s *Server) handleWriteMemoryImpl(w http.ResponseWriter, r *http.Request) {
	project, _ := projectFromContext(r.Context())

	var req writeMemoryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	if req.Content == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return
	}

	memType := models.MemoryType(req.Type)
	if memType == "" {
		memType = models.MemoryTypeSemantic
	}
	scope := models.MemoryScope(req.Scope)
	if scope == "" {
		scope = models.MemoryScopeProject
	}
	trust := models.TrustTier(req.TrustTier)
	if trust == "" {
		trust = models.TrustTierObserved
	}
	conf := req.Confidence
	if conf == 0 {
		conf = 0.8
	}

	record, err := s.memStore.Write(r.Context(), memory.WriteRequest{
		ProjectID:  project.ID,
		MemoryType: memType,
		Scope:      scope,
		Content:    req.Content,
		Metadata:   req.Metadata,
		TrustTier:  trust,
		Confidence: conf,
	})
	if err != nil {
		log.Error().Err(err).Msg("handleWriteMemory: store error")
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusCreated, record)
}

// GET /v1/projects/{projectID}/memory?q=...&type=...&scope=...&min_trust=...
func (s *Server) handleQueryMemoryImpl(w http.ResponseWriter, r *http.Request) {
	project, _ := projectFromContext(r.Context())
	q := r.URL.Query()

	req := memory.QueryRequest{
		ProjectID:          project.ID,
		Query:              q.Get("q"),
		Limit:              20,
		ExcludeQuarantined: true,
	}

	if t := q.Get("type"); t != "" {
		req.Types = []models.MemoryType{models.MemoryType(t)}
	}
	if sc := q.Get("scope"); sc != "" {
		req.Scopes = []models.MemoryScope{models.MemoryScope(sc)}
	}
	if mt := q.Get("min_trust"); mt != "" {
		req.MinTrust = models.TrustTier(mt)
	}

	results, err := s.memStore.Query(r.Context(), req)
	if err != nil {
		log.Error().Err(err).Msg("handleQueryMemory: store error")
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if results == nil {
		results = []memory.QueryResult{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results, "count": len(results)})
}

// GET /v1/projects/{projectID}/memory/graph?entity_type=...&from_id=...&depth=2
func (s *Server) handleGetKnowledgeGraphImpl(w http.ResponseWriter, r *http.Request) {
	project, _ := projectFromContext(r.Context())
	q := r.URL.Query()

	gq := memory.GraphQuery{
		ProjectID:    project.ID,
		EntityType:   q.Get("entity_type"),
		RelationType: q.Get("relation_type"),
		Depth:        2,
		Limit:        100,
	}

	result, err := s.memStore.GraphSearch(r.Context(), gq)
	if err != nil {
		log.Error().Err(err).Msg("handleGetKnowledgeGraph: graph search error")
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, result)
}
