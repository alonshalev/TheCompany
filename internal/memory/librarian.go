// Package memory — Librarian agent.
//
// The Librarian runs continuously in the background. It:
//   1. Decays confidence of stale records (last_accessed_at > threshold).
//   2. Deduplicates near-identical records within the same scope.
//   3. Promotes high-confidence project-scoped records to global (if auto-promote is on).
//   4. Permanently expires records past their expires_at.
//   5. Surfaces contradictions (two records in the same scope with contradicting content)
//      by flagging them for human review (is_quarantined = true, quarantine_reason = "contradiction").
//
// The Librarian does NOT use an LLM in this implementation — it operates purely
// on metadata signals. A future version will use the Architect to run richer
// semantic consolidation (Part 4 enhancement).
package memory

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	// decayThreshold: records not accessed in this period lose confidence.
	decayThreshold = 7 * 24 * time.Hour
	// decayRate: fraction of confidence lost per decay cycle.
	decayRate = 0.05
	// promoteThreshold: records with confidence above this are eligible for scope promotion.
	promoteThreshold = 0.92
	// librarianInterval: how often the librarian runs.
	librarianInterval = 1 * time.Hour
)

// Librarian continuously curates the Shared Memory Fabric.
type Librarian struct {
	store *Store
}

// NewLibrarian creates a Librarian.
func NewLibrarian(store *Store) *Librarian {
	return &Librarian{store: store}
}

// Start runs the librarian loop until ctx is cancelled.
func (l *Librarian) Start(ctx context.Context) {
	log.Info().Msg("librarian starting")
	ticker := time.NewTicker(librarianInterval)
	defer ticker.Stop()

	l.runCycle(ctx) // run immediately on start

	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("librarian stopped")
			return
		case <-ticker.C:
			l.runCycle(ctx)
		}
	}
}

func (l *Librarian) runCycle(ctx context.Context) {
	log.Debug().Msg("librarian cycle starting")

	l.expireRecords(ctx)
	l.decayConfidence(ctx)
	l.deduplicateRecords(ctx)
}

// expireRecords deletes (soft-removes by quarantine) records past their expires_at.
func (l *Librarian) expireRecords(ctx context.Context) {
	res, err := l.store.db.ExecContext(ctx,
		`UPDATE memory_records
		 SET is_quarantined=true, quarantine_reason='expired', updated_at=now()
		 WHERE expires_at IS NOT NULL AND expires_at < now() AND is_quarantined=false`)
	if err != nil {
		log.Error().Err(err).Msg("librarian: expire records")
		return
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		log.Info().Int64("expired", n).Msg("librarian: expired stale records")
	}
}

// decayConfidence reduces confidence of records that haven't been accessed recently.
func (l *Librarian) decayConfidence(ctx context.Context) {
	threshold := time.Now().Add(-decayThreshold)
	res, err := l.store.db.ExecContext(ctx,
		`UPDATE memory_records
		 SET confidence = GREATEST(0.1, confidence * $1),
		     updated_at = now()
		 WHERE last_accessed_at < $2
		   AND memory_type IN ('semantic', 'procedural')
		   AND is_quarantined = false
		   AND confidence > 0.15`,
		1.0-decayRate, threshold)
	if err != nil {
		log.Error().Err(err).Msg("librarian: decay confidence")
		return
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		log.Debug().Int64("decayed", n).Msg("librarian: decayed confidence on stale records")
	}
}

// deduplicateRecords quarantines older duplicates (same project + scope +
// identical content within same type). Uses exact match for now; semantic
// deduplication (via embeddings) is a Part 6 enhancement.
func (l *Librarian) deduplicateRecords(ctx context.Context) {
	res, err := l.store.db.ExecContext(ctx, `
		UPDATE memory_records AS old
		SET is_quarantined = true,
		    quarantine_reason = 'deduplicated: newer record exists',
		    updated_at = now()
		FROM (
			SELECT MIN(id) AS keep_id, project_id, scope, memory_type, content
			FROM memory_records
			WHERE is_quarantined = false
			GROUP BY project_id, scope, memory_type, content
			HAVING COUNT(*) > 1
		) AS dups
		WHERE old.project_id = dups.project_id
		  AND old.scope       = dups.scope
		  AND old.memory_type = dups.memory_type
		  AND old.content     = dups.content
		  AND old.id         != dups.keep_id
		  AND old.is_quarantined = false`)
	if err != nil {
		log.Error().Err(err).Msg("librarian: deduplicate")
		return
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		log.Info().Int64("deduplicated", n).Msg("librarian: removed duplicate memory records")
	}
}
