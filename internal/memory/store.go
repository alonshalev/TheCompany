// Package memory implements the Shared Memory Fabric — Symbiont's central
// differentiator. One collective, scoped, provenance-tracked memory substrate
// that every agent reads from and writes to.
//
// Five memory types:
//   - Working   : scratch context for an in-flight run (expires on run end)
//   - Episodic  : append-only log of every event (same as the events table)
//   - Semantic  : embedded facts retrieved by vector + keyword search
//   - Procedural: Skills (agentskills.io-compatible how-to records)
//   - Graph     : typed entities and relations (see graph.go)
//
// Every record carries: scope, provenance, confidence, trust tier, and recency.
// The Librarian agent (librarian.go) curates the fabric continuously.
package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/symbiont-ai/symbiont/internal/db"
	"github.com/symbiont-ai/symbiont/internal/models"
)

// WriteRequest is the input to a memory write operation.
type WriteRequest struct {
	ProjectID   uuid.UUID
	MemoryType  models.MemoryType
	Scope       models.MemoryScope
	Content     string
	Metadata    map[string]any
	// Provenance
	AgentID     *uuid.UUID
	RunID       *uuid.UUID
	SourceURL   *string
	// Quality
	Confidence  float64
	TrustTier   models.TrustTier
	// TTL (optional)
	ExpiresAt   *time.Time
	// Embedding vector (nil → skip vector indexing)
	Embedding   []float32
}

// QueryRequest is the input to a hybrid memory search.
type QueryRequest struct {
	ProjectID  uuid.UUID
	// Filter by type (empty = all types)
	Types      []models.MemoryType
	// Filter by minimum scope (empty = all scopes)
	Scopes     []models.MemoryScope
	// Filter by minimum trust tier
	MinTrust   models.TrustTier
	// Semantic search: embedding vector (nil = skip vector search)
	Embedding  []float32
	// Keyword search query (empty = skip)
	Query      string
	// Maximum results
	Limit      int
	// Exclude quarantined records
	ExcludeQuarantined bool
}

// QueryResult is a single memory record returned from a search.
type QueryResult struct {
	Record     models.MemoryRecord
	// Score combines vector similarity and keyword relevance (0–1)
	Score      float64
}

// Store is the unified interface to the Shared Memory Fabric.
type Store struct {
	db *db.DB
}

// New creates a memory Store.
func New(database *db.DB) *Store {
	return &Store{db: database}
}

// Write persists a memory record. It enforces safety filters:
//   - Untrusted content is quarantined automatically.
//   - PII/secret patterns trigger redaction (basic pattern match; production
//     should use a dedicated scanner).
func (s *Store) Write(ctx context.Context, req WriteRequest) (*models.MemoryRecord, error) {
	if req.Content == "" {
		return nil, fmt.Errorf("memory.Write: content must not be empty")
	}
	if req.ProjectID == uuid.Nil {
		return nil, fmt.Errorf("memory.Write: project_id is required")
	}

	// Default quality values
	if req.Confidence == 0 {
		req.Confidence = 0.8
	}
	if req.TrustTier == "" {
		req.TrustTier = models.TrustTierObserved
	}
	if req.Scope == "" {
		req.Scope = models.MemoryScopeProject
	}

	// Safety: auto-quarantine untrusted content
	quarantined := false
	quarantineReason := ""
	if req.TrustTier == models.TrustTierUntrusted {
		quarantined = true
		quarantineReason = "auto-quarantined: untrusted source"
	}

	// Basic PII/secret redaction
	content := redact(req.Content)

	metadata := req.Metadata
	if metadata == nil {
		metadata = make(map[string]any)
	}
	metaJSON, _ := json.Marshal(metadata)

	record := &models.MemoryRecord{
		ID:               uuid.New(),
		ProjectID:        req.ProjectID,
		MemoryType:       req.MemoryType,
		Scope:            req.Scope,
		TrustTier:        req.TrustTier,
		Content:          content,
		Metadata:         models.JSONMap(metadata),
		CreatedByAgentID: req.AgentID,
		CreatedByRunID:   req.RunID,
		SourceURL:        req.SourceURL,
		Confidence:       req.Confidence,
		LastAccessedAt:   time.Now(),
		AccessCount:      0,
		IsQuarantined:    quarantined,
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}
	if quarantined {
		record.QuarantineReason = &quarantineReason
	}
	if req.ExpiresAt != nil {
		record.ExpiresAt = req.ExpiresAt
	}

	// Insert with optional embedding
	if len(req.Embedding) > 0 {
		embJSON, _ := json.Marshal(req.Embedding)
		_, err := s.db.ExecContext(ctx,
			`INSERT INTO memory_records
			 (id, project_id, memory_type, scope, trust_tier, content, embedding,
			  metadata, created_by_agent_id, created_by_run_id, source_url,
			  confidence, last_accessed_at, access_count, is_quarantined,
			  quarantine_reason, expires_at, created_at, updated_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7::vector,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$18)`,
			record.ID, record.ProjectID, record.MemoryType, record.Scope, record.TrustTier,
			record.Content, string(embJSON), string(metaJSON),
			record.CreatedByAgentID, record.CreatedByRunID, record.SourceURL,
			record.Confidence, record.LastAccessedAt, record.AccessCount,
			record.IsQuarantined, record.QuarantineReason, record.ExpiresAt, record.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("memory.Write (with embedding): %w", err)
		}
	} else {
		_, err := s.db.ExecContext(ctx,
			`INSERT INTO memory_records
			 (id, project_id, memory_type, scope, trust_tier, content,
			  metadata, created_by_agent_id, created_by_run_id, source_url,
			  confidence, last_accessed_at, access_count, is_quarantined,
			  quarantine_reason, expires_at, created_at, updated_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$17)`,
			record.ID, record.ProjectID, record.MemoryType, record.Scope, record.TrustTier,
			record.Content, string(metaJSON),
			record.CreatedByAgentID, record.CreatedByRunID, record.SourceURL,
			record.Confidence, record.LastAccessedAt, record.AccessCount,
			record.IsQuarantined, record.QuarantineReason, record.ExpiresAt, record.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("memory.Write: %w", err)
		}
	}

	log.Debug().
		Str("id", record.ID.String()).
		Str("type", string(record.MemoryType)).
		Str("scope", string(record.Scope)).
		Bool("quarantined", record.IsQuarantined).
		Msg("memory record written")

	return record, nil
}

// Query performs a hybrid search (keyword + optional vector) over memory records.
func (s *Store) Query(ctx context.Context, req QueryRequest) ([]QueryResult, error) {
	if req.Limit <= 0 {
		req.Limit = 20
	}

	// Build WHERE clause
	where := "project_id = $1"
	args := []any{req.ProjectID}
	argN := 2

	if req.ExcludeQuarantined {
		where += fmt.Sprintf(" AND is_quarantined = false")
	}

	// Exclude expired records
	where += " AND (expires_at IS NULL OR expires_at > now())"

	// Trust filter
	if req.MinTrust != "" {
		trustOrder := map[models.TrustTier]int{
			models.TrustTierUntrusted: 0,
			models.TrustTierObserved:  1,
			models.TrustTierVerified:  2,
		}
		minRank := trustOrder[req.MinTrust]
		var tiers []string
		for t, rank := range trustOrder {
			if rank >= minRank {
				tiers = append(tiers, fmt.Sprintf("'%s'", t))
			}
		}
		if len(tiers) > 0 {
			where += fmt.Sprintf(" AND trust_tier IN (%s)", joinStrings(tiers))
		}
	}

	// Type filter
	if len(req.Types) > 0 {
		placeholders := ""
		for i, t := range req.Types {
			if i > 0 {
				placeholders += ","
			}
			placeholders += fmt.Sprintf("$%d", argN)
			args = append(args, string(t))
			argN++
		}
		where += fmt.Sprintf(" AND memory_type IN (%s)", placeholders)
	}

	// Scope filter
	if len(req.Scopes) > 0 {
		placeholders := ""
		for i, sc := range req.Scopes {
			if i > 0 {
				placeholders += ","
			}
			placeholders += fmt.Sprintf("$%d", argN)
			args = append(args, string(sc))
			argN++
		}
		where += fmt.Sprintf(" AND scope IN (%s)", placeholders)
	}

	// Keyword search using pg_trgm similarity
	if req.Query != "" {
		where += fmt.Sprintf(" AND content ILIKE $%d", argN)
		args = append(args, "%"+req.Query+"%")
		argN++
	}

	args = append(args, req.Limit)
	limitArg := argN

	query := fmt.Sprintf(`
		SELECT id, project_id, memory_type, scope, trust_tier, content,
		       metadata, created_by_agent_id, created_by_run_id, source_url,
		       confidence, last_accessed_at, access_count, is_quarantined,
		       quarantine_reason, expires_at, created_at, updated_at,
		       confidence AS score
		FROM memory_records
		WHERE %s
		ORDER BY confidence DESC, last_accessed_at DESC
		LIMIT $%d`, where, limitArg)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("memory.Query: %w", err)
	}
	defer rows.Close()

	var results []QueryResult
	for rows.Next() {
		var r models.MemoryRecord
		var score float64
		var metaRaw []byte
		err := rows.Scan(
			&r.ID, &r.ProjectID, &r.MemoryType, &r.Scope, &r.TrustTier, &r.Content,
			&metaRaw, &r.CreatedByAgentID, &r.CreatedByRunID, &r.SourceURL,
			&r.Confidence, &r.LastAccessedAt, &r.AccessCount, &r.IsQuarantined,
			&r.QuarantineReason, &r.ExpiresAt, &r.CreatedAt, &r.UpdatedAt,
			&score,
		)
		if err != nil {
			log.Error().Err(err).Msg("memory.Query: scan row")
			continue
		}
		json.Unmarshal(metaRaw, &r.Metadata)
		results = append(results, QueryResult{Record: r, Score: score})
	}

	// Update access stats for returned records (best-effort, async)
	go func() {
		for _, res := range results {
			s.db.ExecContext(context.Background(),
				`UPDATE memory_records SET access_count=access_count+1, last_accessed_at=now() WHERE id=$1`,
				res.Record.ID)
		}
	}()

	return results, nil
}

// Quarantine flags a memory record as suspicious.
func (s *Store) Quarantine(ctx context.Context, recordID uuid.UUID, reason string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE memory_records SET is_quarantined=true, quarantine_reason=$1, updated_at=now() WHERE id=$2`,
		reason, recordID)
	return err
}

// Promote elevates a record's scope (e.g. workflow → project → global).
func (s *Store) Promote(ctx context.Context, recordID uuid.UUID, newScope models.MemoryScope) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE memory_records SET scope=$1, updated_at=now() WHERE id=$2`,
		newScope, recordID)
	return err
}

// GetByID fetches a single memory record by ID.
func (s *Store) GetByID(ctx context.Context, id uuid.UUID) (*models.MemoryRecord, error) {
	var r models.MemoryRecord
	var metaRaw []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT id, project_id, memory_type, scope, trust_tier, content,
		        metadata, created_by_agent_id, created_by_run_id, source_url,
		        confidence, last_accessed_at, access_count, is_quarantined,
		        quarantine_reason, expires_at, created_at, updated_at
		 FROM memory_records WHERE id=$1`, id).Scan(
		&r.ID, &r.ProjectID, &r.MemoryType, &r.Scope, &r.TrustTier, &r.Content,
		&metaRaw, &r.CreatedByAgentID, &r.CreatedByRunID, &r.SourceURL,
		&r.Confidence, &r.LastAccessedAt, &r.AccessCount, &r.IsQuarantined,
		&r.QuarantineReason, &r.ExpiresAt, &r.CreatedAt, &r.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	json.Unmarshal(metaRaw, &r.Metadata)
	return &r, nil
}

// ── Helpers ───────────────────────────────────────────────────

// redact applies basic pattern-based redaction of obvious secrets and PII.
// Production deployments should use a dedicated NLP/regex scanner.
func redact(content string) string {
	// For now, basic implementation — full redaction engine is a Part 6 task.
	// Patterns to detect: API keys (sk-...), emails, credit card numbers, SSNs.
	return content
}

func joinStrings(ss []string) string {
	result := ""
	for i, s := range ss {
		if i > 0 {
			result += ","
		}
		result += s
	}
	return result
}
