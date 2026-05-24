// Package queue implements a Postgres-backed durable job queue.
//
// Design goals:
//   - No extra infrastructure — one Postgres table, no Redis, no RabbitMQ.
//   - At-least-once delivery with configurable retries and dead-lettering.
//   - Workers claim jobs using SELECT … FOR UPDATE SKIP LOCKED so multiple
//     workers can compete safely.
//   - Jobs carry an arbitrary JSON payload; handlers are registered by kind.
//   - Crashed workers leave jobs locked; a reaper goroutine reclaims them
//     after their lock timeout expires.
package queue

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/symbiont-ai/symbiont/internal/db"
)

const (
	// DefaultLockTimeout is how long a worker holds a job before it can be
	// reclaimed by the reaper.
	DefaultLockTimeout = 5 * time.Minute
	// DefaultPollInterval is how often idle workers poll for new jobs.
	DefaultPollInterval = 2 * time.Second
	// DefaultMaxAttempts is the default retry limit per job.
	DefaultMaxAttempts = 3
)

// Job represents a single unit of work.
type Job struct {
	ID          int64
	ExtID       uuid.UUID
	Kind        string
	Payload     json.RawMessage
	Status      string
	Priority    int
	RunAt       time.Time
	Attempt     int
	MaxAttempts int
	LockedUntil *time.Time
	LockedBy    *string
	LastError   *string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	CompletedAt *time.Time
}

// HandlerFunc processes a job. Return a non-nil error to trigger a retry.
type HandlerFunc func(ctx context.Context, job *Job) error

// Queue manages enqueuing, polling, and dispatching jobs.
type Queue struct {
	db          *db.DB
	workerID    string
	lockTimeout time.Duration
	pollInterval time.Duration
	handlers    map[string]HandlerFunc
	mu          sync.RWMutex
	concurrency int
}

// New creates a Queue. Call Start() to begin processing.
func New(database *db.DB, opts ...Option) *Queue {
	q := &Queue{
		db:           database,
		workerID:     fmt.Sprintf("worker-%s", uuid.New().String()[:8]),
		lockTimeout:  DefaultLockTimeout,
		pollInterval: DefaultPollInterval,
		handlers:     make(map[string]HandlerFunc),
		concurrency:  4,
	}
	for _, o := range opts {
		o(q)
	}
	return q
}

// Option configures a Queue.
type Option func(*Queue)

func WithConcurrency(n int) Option       { return func(q *Queue) { q.concurrency = n } }
func WithLockTimeout(d time.Duration) Option { return func(q *Queue) { q.lockTimeout = d } }
func WithPollInterval(d time.Duration) Option { return func(q *Queue) { q.pollInterval = d } }

// Register binds a handler to a job kind.
func (q *Queue) Register(kind string, fn HandlerFunc) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.handlers[kind] = fn
}

// Enqueue inserts a new job. Returns the job's external ID.
func (q *Queue) Enqueue(ctx context.Context, kind string, payload any, opts ...EnqueueOption) (uuid.UUID, error) {
	cfg := enqueueConfig{
		priority:    100,
		maxAttempts: DefaultMaxAttempts,
		runAt:       time.Now(),
	}
	for _, o := range opts {
		o(&cfg)
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return uuid.Nil, fmt.Errorf("queue.Enqueue: marshal payload: %w", err)
	}

	var extID uuid.UUID
	err = q.db.QueryRowContext(ctx,
		`INSERT INTO jobs (kind, payload, priority, max_attempts, run_at)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING ext_id`,
		kind, string(raw), cfg.priority, cfg.maxAttempts, cfg.runAt,
	).Scan(&extID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("queue.Enqueue: insert: %w", err)
	}
	return extID, nil
}

// EnqueueOption configures a single enqueue call.
type EnqueueOption func(*enqueueConfig)

type enqueueConfig struct {
	priority    int
	maxAttempts int
	runAt       time.Time
}

func WithPriority(p int) EnqueueOption        { return func(c *enqueueConfig) { c.priority = p } }
func WithMaxAttempts(n int) EnqueueOption     { return func(c *enqueueConfig) { c.maxAttempts = n } }
func WithRunAt(t time.Time) EnqueueOption     { return func(c *enqueueConfig) { c.runAt = t } }

// Start launches worker goroutines and the reaper. Blocks until ctx is cancelled.
func (q *Queue) Start(ctx context.Context) {
	log.Info().
		Int("concurrency", q.concurrency).
		Str("worker_id", q.workerID).
		Msg("job queue starting")

	var wg sync.WaitGroup

	// Reaper: reclaims stale locked jobs
	wg.Add(1)
	go func() {
		defer wg.Done()
		q.runReaper(ctx)
	}()

	// Workers
	sem := make(chan struct{}, q.concurrency)
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(q.pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Try to fill up to concurrency slots
				for {
					job, err := q.claim(ctx)
					if err != nil || job == nil {
						break
					}
					sem <- struct{}{}
					wg.Add(1)
					go func(j *Job) {
						defer wg.Done()
						defer func() { <-sem }()
						q.process(ctx, j)
					}(job)
				}
			}
		}
	}()

	wg.Wait()
	log.Info().Msg("job queue stopped")
}

// claim picks one pending job and locks it. Returns nil if none available.
func (q *Queue) claim(ctx context.Context) (*Job, error) {
	lockUntil := time.Now().Add(q.lockTimeout)
	var job Job
	err := q.db.QueryRowContext(ctx, `
		UPDATE jobs SET
			status       = 'running',
			locked_until = $1,
			locked_by    = $2,
			attempt      = attempt + 1,
			updated_at   = now()
		WHERE id = (
			SELECT id FROM jobs
			WHERE status IN ('pending', 'failed')
			  AND run_at <= now()
			  AND attempt < max_attempts
			ORDER BY priority ASC, run_at ASC
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING id, ext_id, kind, payload, status, priority, run_at,
		          attempt, max_attempts, locked_until, locked_by,
		          last_error, created_at, updated_at, completed_at`,
		lockUntil, q.workerID,
	).Scan(
		&job.ID, &job.ExtID, &job.Kind, &job.Payload, &job.Status,
		&job.Priority, &job.RunAt, &job.Attempt, &job.MaxAttempts,
		&job.LockedUntil, &job.LockedBy, &job.LastError,
		&job.CreatedAt, &job.UpdatedAt, &job.CompletedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("queue.claim: %w", err)
	}
	return &job, nil
}

// process dispatches a claimed job to its handler and records the outcome.
func (q *Queue) process(ctx context.Context, job *Job) {
	q.mu.RLock()
	handler, ok := q.handlers[job.Kind]
	q.mu.RUnlock()

	logger := log.With().
		Str("job_kind", job.Kind).
		Str("job_ext_id", job.ExtID.String()).
		Int("attempt", job.Attempt).
		Logger()

	if !ok {
		// No handler registered — dead-letter immediately
		logger.Error().Msg("no handler registered for job kind")
		q.markDead(ctx, job.ID, "no handler registered")
		return
	}

	logger.Debug().Msg("processing job")
	handlerErr := handler(ctx, job)

	if handlerErr == nil {
		logger.Debug().Msg("job succeeded")
		q.markSucceeded(ctx, job.ID)
		return
	}

	logger.Warn().Err(handlerErr).Msg("job handler returned error")

	if job.Attempt >= job.MaxAttempts {
		logger.Error().Msg("job exhausted retries — dead-lettering")
		q.markDead(ctx, job.ID, handlerErr.Error())
		return
	}

	// Exponential back-off with jitter: 2^attempt * 10s ± 20%
	backoff := time.Duration(1<<uint(job.Attempt)) * 10 * time.Second
	jitter := time.Duration(rand.Int63n(int64(backoff / 5)))
	retryAt := time.Now().Add(backoff + jitter)

	q.markRetry(ctx, job.ID, handlerErr.Error(), retryAt)
	logger.Warn().Time("retry_at", retryAt).Msg("job scheduled for retry")
}

func (q *Queue) markSucceeded(ctx context.Context, id int64) {
	q.db.ExecContext(ctx,
		`UPDATE jobs SET status='succeeded', locked_until=NULL, locked_by=NULL,
		 completed_at=now(), updated_at=now() WHERE id=$1`, id)
}

func (q *Queue) markDead(ctx context.Context, id int64, reason string) {
	q.db.ExecContext(ctx,
		`UPDATE jobs SET status='dead', locked_until=NULL, locked_by=NULL,
		 last_error=$1, completed_at=now(), updated_at=now() WHERE id=$2`, reason, id)
}

func (q *Queue) markRetry(ctx context.Context, id int64, reason string, retryAt time.Time) {
	q.db.ExecContext(ctx,
		`UPDATE jobs SET status='failed', locked_until=NULL, locked_by=NULL,
		 last_error=$1, run_at=$2, updated_at=now() WHERE id=$3`, reason, retryAt, id)
}

// runReaper reclaims jobs whose lock has expired (worker crashed mid-job).
func (q *Queue) runReaper(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			res, err := q.db.ExecContext(ctx,
				`UPDATE jobs SET status='failed', locked_until=NULL, locked_by=NULL,
				 last_error='reclaimed by reaper (lock expired)', updated_at=now()
				 WHERE status='running' AND locked_until < now()`)
			if err != nil {
				log.Error().Err(err).Msg("queue reaper error")
				continue
			}
			if n, _ := res.RowsAffected(); n > 0 {
				log.Warn().Int64("reclaimed", n).Msg("queue reaper reclaimed stale jobs")
			}
		}
	}
}
