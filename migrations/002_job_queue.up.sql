-- ============================================================
-- Symbiont — Migration 002: Durable job queue
-- ============================================================

CREATE TYPE job_status AS ENUM ('pending', 'running', 'succeeded', 'failed', 'dead');

CREATE TABLE jobs (
    id              BIGSERIAL PRIMARY KEY,
    ext_id          UUID NOT NULL DEFAULT uuid_generate_v4() UNIQUE,
    -- Job kind maps to a handler registered in the engine
    kind            TEXT NOT NULL,
    -- Arbitrary JSON payload consumed by the handler
    payload         JSONB NOT NULL DEFAULT '{}',
    status          job_status NOT NULL DEFAULT 'pending',
    -- Priority: lower number = higher priority
    priority        INT NOT NULL DEFAULT 100,
    -- Scheduling
    run_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Retry tracking
    attempt         INT NOT NULL DEFAULT 0,
    max_attempts    INT NOT NULL DEFAULT 3,
    -- Locking: set to now()+timeout when a worker picks the job
    locked_until    TIMESTAMPTZ,
    locked_by       TEXT,   -- worker identity string
    -- Error info on failure
    last_error      TEXT,
    -- Timestamps
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at    TIMESTAMPTZ
);

CREATE INDEX idx_jobs_queue ON jobs (status, priority, run_at)
    WHERE status IN ('pending', 'failed');
CREATE INDEX idx_jobs_ext_id ON jobs (ext_id);
CREATE INDEX idx_jobs_kind ON jobs (kind, status);

-- Periodic cleanup of old completed/dead jobs (run manually or via cron)
-- Jobs completed > 7 days ago can be deleted with:
-- DELETE FROM jobs WHERE status IN ('succeeded','dead') AND completed_at < now() - interval '7 days';
