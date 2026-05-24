// Package memory — Knowledge Graph operations.
//
// The Knowledge Graph stores typed entities (customers, features, competitors,
// decisions) and the relations between them. Agents can reason over structure
// rather than just embedding similarity.
package memory

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/symbiont-ai/symbiont/internal/models"
)

// UpsertEntity creates or updates a knowledge-graph entity.
// If an entity with the same (project_id, entity_type, name) already exists,
// its properties are merged.
func (s *Store) UpsertEntity(ctx context.Context, projectID uuid.UUID, entityType, name string, properties map[string]any, trust models.TrustTier) (*models.MemoryEntity, error) {
	if trust == "" {
		trust = models.TrustTierObserved
	}
	propsJSON, _ := json.Marshal(properties)

	entity := &models.MemoryEntity{
		ID:         uuid.New(),
		ProjectID:  projectID,
		EntityType: entityType,
		Name:       name,
		Properties: models.JSONMap(properties),
		TrustTier:  trust,
	}

	err := s.db.QueryRowContext(ctx,
		`INSERT INTO memory_entities (id, project_id, entity_type, name, properties, trust_tier, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,now(),now())
		 ON CONFLICT (project_id, entity_type, name)
		 DO UPDATE SET
		   properties = memory_entities.properties || EXCLUDED.properties,
		   trust_tier = EXCLUDED.trust_tier,
		   updated_at = now()
		 RETURNING id, created_at, updated_at`,
		entity.ID, entity.ProjectID, entity.EntityType, entity.Name, string(propsJSON), entity.TrustTier,
	).Scan(&entity.ID, &entity.CreatedAt, &entity.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("memory.UpsertEntity: %w", err)
	}
	return entity, nil
}

// AddRelation creates a directed relation between two entities.
func (s *Store) AddRelation(ctx context.Context, projectID, fromID, toID uuid.UUID, relationType string, properties map[string]any, confidence float64) (*models.MemoryRelation, error) {
	if confidence == 0 {
		confidence = 0.8
	}
	propsJSON, _ := json.Marshal(properties)

	rel := &models.MemoryRelation{
		ID:           uuid.New(),
		ProjectID:    projectID,
		FromEntityID: fromID,
		ToEntityID:   toID,
		RelationType: relationType,
		Properties:   models.JSONMap(properties),
		Confidence:   confidence,
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO memory_relations (id, project_id, from_entity_id, to_entity_id, relation_type, properties, confidence, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,now())
		 ON CONFLICT DO NOTHING`,
		rel.ID, rel.ProjectID, rel.FromEntityID, rel.ToEntityID, rel.RelationType, string(propsJSON), rel.Confidence)
	if err != nil {
		return nil, fmt.Errorf("memory.AddRelation: %w", err)
	}
	return rel, nil
}

// GraphQuery is the input to a knowledge-graph traversal.
type GraphQuery struct {
	ProjectID    uuid.UUID
	// Start from a specific entity ID (nil = return all of entity_type)
	FromEntityID *uuid.UUID
	EntityType   string
	// Depth of traversal (1 = direct neighbours only)
	Depth        int
	// Filter by relation type (empty = all)
	RelationType string
	Limit        int
}

// GraphResult holds a subgraph returned by GraphSearch.
type GraphResult struct {
	Entities  []models.MemoryEntity
	Relations []models.MemoryRelation
}

// GraphSearch traverses the knowledge graph from a starting entity.
func (s *Store) GraphSearch(ctx context.Context, q GraphQuery) (*GraphResult, error) {
	if q.Limit <= 0 {
		q.Limit = 50
	}
	if q.Depth <= 0 {
		q.Depth = 1
	}

	result := &GraphResult{}

	// Start: resolve the anchor entity set
	var anchorIDs []uuid.UUID

	if q.FromEntityID != nil {
		anchorIDs = []uuid.UUID{*q.FromEntityID}
	} else if q.EntityType != "" {
		rows, err := s.db.QueryContext(ctx,
			`SELECT id FROM memory_entities WHERE project_id=$1 AND entity_type=$2 LIMIT $3`,
			q.ProjectID, q.EntityType, q.Limit)
		if err != nil {
			return nil, fmt.Errorf("memory.GraphSearch anchor query: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			rows.Scan(&id)
			anchorIDs = append(anchorIDs, id)
		}
	} else {
		// No filter — return all entities for the project (capped)
		rows, err := s.db.QueryContext(ctx,
			`SELECT id FROM memory_entities WHERE project_id=$1 LIMIT $2`, q.ProjectID, q.Limit)
		if err != nil {
			return nil, fmt.Errorf("memory.GraphSearch all-entities query: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			rows.Scan(&id)
			anchorIDs = append(anchorIDs, id)
		}
	}

	visited := make(map[uuid.UUID]bool)
	frontier := anchorIDs

	for depth := 0; depth < q.Depth && len(frontier) > 0; depth++ {
		// Fetch entities for frontier
		for _, id := range frontier {
			if visited[id] {
				continue
			}
			visited[id] = true

			var e models.MemoryEntity
			var propsRaw []byte
			err := s.db.QueryRowContext(ctx,
				`SELECT id, project_id, entity_type, name, properties, trust_tier, created_at, updated_at
				 FROM memory_entities WHERE id=$1`, id).Scan(
				&e.ID, &e.ProjectID, &e.EntityType, &e.Name, &propsRaw, &e.TrustTier, &e.CreatedAt, &e.UpdatedAt)
			if err != nil {
				continue
			}
			json.Unmarshal(propsRaw, &e.Properties)
			result.Entities = append(result.Entities, e)
		}

		// Expand to neighbours via relations
		var nextFrontier []uuid.UUID
		for _, id := range frontier {
			relWhere := "from_entity_id=$1"
			relArgs := []any{id}
			if q.RelationType != "" {
				relWhere += " AND relation_type=$2"
				relArgs = append(relArgs, q.RelationType)
			}

			rows, err := s.db.QueryContext(ctx,
				fmt.Sprintf(`SELECT id, project_id, from_entity_id, to_entity_id, relation_type, properties, confidence, created_at
				 FROM memory_relations WHERE %s`, relWhere), relArgs...)
			if err != nil {
				continue
			}
			for rows.Next() {
				var rel models.MemoryRelation
				var propsRaw []byte
				if err := rows.Scan(&rel.ID, &rel.ProjectID, &rel.FromEntityID, &rel.ToEntityID,
					&rel.RelationType, &propsRaw, &rel.Confidence, &rel.CreatedAt); err != nil {
					continue
				}
				json.Unmarshal(propsRaw, &rel.Properties)
				result.Relations = append(result.Relations, rel)
				if !visited[rel.ToEntityID] {
					nextFrontier = append(nextFrontier, rel.ToEntityID)
				}
			}
			rows.Close()
		}
		frontier = nextFrontier
	}

	return result, nil
}

// FindEntity looks up a single entity by type + name.
func (s *Store) FindEntity(ctx context.Context, projectID uuid.UUID, entityType, name string) (*models.MemoryEntity, error) {
	var e models.MemoryEntity
	var propsRaw []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT id, project_id, entity_type, name, properties, trust_tier, created_at, updated_at
		 FROM memory_entities WHERE project_id=$1 AND entity_type=$2 AND name=$3`,
		projectID, entityType, name).Scan(
		&e.ID, &e.ProjectID, &e.EntityType, &e.Name, &propsRaw, &e.TrustTier, &e.CreatedAt, &e.UpdatedAt)
	if err != nil {
		return nil, err
	}
	json.Unmarshal(propsRaw, &e.Properties)
	return &e, nil
}
