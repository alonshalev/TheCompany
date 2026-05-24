-- ============================================================
-- Symbiont — Migration 001: Core schema
-- ============================================================

-- Extensions
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pg_trgm;   -- for keyword search on memory

-- ── Enumerations ─────────────────────────────────────────────

CREATE TYPE user_role AS ENUM ('owner', 'admin', 'member', 'viewer');
CREATE TYPE run_status AS ENUM ('pending', 'running', 'paused', 'succeeded', 'failed', 'cancelled');
CREATE TYPE event_kind AS ENUM (
    'run_started', 'run_succeeded', 'run_failed', 'run_cancelled',
    'node_started', 'node_succeeded', 'node_failed',
    'agent_step', 'model_call', 'tool_call', 'tool_result',
    'memory_write', 'memory_read',
    'approval_requested', 'approval_resolved',
    'spend_metered', 'budget_warning', 'budget_exceeded'
);
CREATE TYPE memory_type AS ENUM ('working', 'episodic', 'semantic', 'procedural');
CREATE TYPE memory_scope AS ENUM ('private', 'agent', 'workflow', 'project', 'global');
CREATE TYPE trust_tier AS ENUM ('verified', 'observed', 'untrusted');
CREATE TYPE approval_status AS ENUM ('pending', 'approved', 'rejected', 'expired');
CREATE TYPE provider_name AS ENUM ('anthropic', 'openai', 'openrouter', 'ollama', 'custom');
CREATE TYPE node_type AS ENUM (
    'trigger', 'agent', 'tool', 'memory', 'approval_gate',
    'branch', 'sub_workflow', 'output'
);
CREATE TYPE trigger_kind AS ENUM ('cron', 'webhook', 'internal_event', 'manual');

-- ── Organizations & Users ────────────────────────────────────

CREATE TABLE organizations (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name        TEXT NOT NULL,
    slug        TEXT NOT NULL UNIQUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE users (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    org_id          UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    email           TEXT NOT NULL,
    name            TEXT NOT NULL,
    role            user_role NOT NULL DEFAULT 'member',
    -- For OIDC: external provider subject
    oidc_subject    TEXT,
    oidc_provider   TEXT,
    -- For API key auth (hashed)
    api_key_hash    TEXT,
    api_key_prefix  TEXT,  -- e.g. "sym_" + first 8 chars for display
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at    TIMESTAMPTZ,
    UNIQUE (org_id, email)
);

CREATE INDEX idx_users_api_key_hash ON users (api_key_hash) WHERE api_key_hash IS NOT NULL;
CREATE INDEX idx_users_oidc ON users (oidc_provider, oidc_subject) WHERE oidc_subject IS NOT NULL;

-- ── Projects ─────────────────────────────────────────────────

CREATE TABLE projects (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    org_id          UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    slug            TEXT NOT NULL,
    description     TEXT,
    -- Budget in USD cents (0 = unlimited)
    budget_cents    BIGINT NOT NULL DEFAULT 0,
    spend_cents     BIGINT NOT NULL DEFAULT 0,
    settings        JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    archived_at     TIMESTAMPTZ,
    UNIQUE (org_id, slug)
);

-- ── Provider Configs ──────────────────────────────────────────

CREATE TABLE provider_configs (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    org_id          UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    provider        provider_name NOT NULL,
    -- Never stores the actual key — references a secret by name
    secret_ref      TEXT NOT NULL,
    -- Extra settings: base_url, model_overrides, etc.
    settings        JSONB NOT NULL DEFAULT '{}',
    is_default      BOOLEAN NOT NULL DEFAULT false,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_provider_configs_org ON provider_configs (org_id);

-- ── Agent Specs ───────────────────────────────────────────────

CREATE TABLE agent_specs (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    project_id          UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name                TEXT NOT NULL,
    slug                TEXT NOT NULL,
    -- Semantic versioning: 1, 2, 3 ...
    version             INT NOT NULL DEFAULT 1,
    -- Role / goal / instructions
    role                TEXT NOT NULL,
    goal                TEXT NOT NULL,
    system_instructions TEXT NOT NULL,
    -- Provider / model
    provider_config_id  UUID REFERENCES provider_configs(id),
    model               TEXT NOT NULL DEFAULT 'claude-opus-4-6',
    -- Memory scopes this agent may read/write (array of memory_scope enum values)
    memory_read_scopes  memory_scope[] NOT NULL DEFAULT ARRAY['project']::memory_scope[],
    memory_write_scopes memory_scope[] NOT NULL DEFAULT ARRAY['workflow']::memory_scope[],
    -- Budget in USD cents
    budget_cents        BIGINT NOT NULL DEFAULT 500,   -- $5 default
    -- Tool grants stored as JSON array of MCP server tool names
    tool_grants         JSONB NOT NULL DEFAULT '[]',
    -- Guardrails
    guardrails          JSONB NOT NULL DEFAULT '{}',
    -- Factory metadata (null if hand-authored)
    synthesized_from    TEXT,
    template_id         UUID,
    -- Status
    is_active           BOOLEAN NOT NULL DEFAULT true,
    created_by          UUID REFERENCES users(id),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, slug, version)
);

CREATE INDEX idx_agent_specs_project ON agent_specs (project_id);

-- ── Agent Instances ───────────────────────────────────────────

CREATE TABLE agent_instances (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    spec_id         UUID NOT NULL REFERENCES agent_specs(id),
    name            TEXT NOT NULL,
    -- Accumulated spend for this instance
    spend_cents     BIGINT NOT NULL DEFAULT 0,
    is_active       BOOLEAN NOT NULL DEFAULT true,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_agent_instances_project ON agent_instances (project_id);
CREATE INDEX idx_agent_instances_spec ON agent_instances (spec_id);

-- ── MCP Servers & Tool Grants ─────────────────────────────────

CREATE TABLE mcp_servers (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    org_id          UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    description     TEXT,
    -- Transport: stdio | http | sse
    transport       TEXT NOT NULL DEFAULT 'http',
    endpoint_url    TEXT,
    -- Tools discovered from this server (cached)
    discovered_tools JSONB NOT NULL DEFAULT '[]',
    settings        JSONB NOT NULL DEFAULT '{}',
    is_active       BOOLEAN NOT NULL DEFAULT true,
    last_ping_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ── Workflows ─────────────────────────────────────────────────

CREATE TABLE workflows (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    slug            TEXT NOT NULL,
    description     TEXT,
    version         INT NOT NULL DEFAULT 1,
    -- Serialised node-graph; nodes and edges arrays
    definition      JSONB NOT NULL DEFAULT '{"nodes":[],"edges":[]}',
    is_active       BOOLEAN NOT NULL DEFAULT true,
    created_by      UUID REFERENCES users(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, slug)
);

CREATE INDEX idx_workflows_project ON workflows (project_id);

-- ── Triggers ──────────────────────────────────────────────────

CREATE TABLE triggers (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    workflow_id     UUID NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
    kind            trigger_kind NOT NULL,
    -- cron expression (for cron kind)
    cron_expr       TEXT,
    -- webhook path suffix (for webhook kind)
    webhook_path    TEXT,
    -- extra settings
    settings        JSONB NOT NULL DEFAULT '{}',
    is_active       BOOLEAN NOT NULL DEFAULT true,
    last_fired_at   TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_triggers_workflow ON triggers (workflow_id);
CREATE INDEX idx_triggers_webhook_path ON triggers (webhook_path) WHERE webhook_path IS NOT NULL;

-- ── Runs ──────────────────────────────────────────────────────

CREATE TABLE runs (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    -- Either a workflow run or a direct agent run
    workflow_id     UUID REFERENCES workflows(id),
    agent_instance_id UUID REFERENCES agent_instances(id),
    -- Trigger info
    trigger_id      UUID REFERENCES triggers(id),
    trigger_kind    trigger_kind,
    -- Input payload
    input           JSONB NOT NULL DEFAULT '{}',
    -- Status
    status          run_status NOT NULL DEFAULT 'pending',
    -- Spend for this run (USD cents)
    spend_cents     BIGINT NOT NULL DEFAULT 0,
    -- Error message (if failed)
    error           TEXT,
    -- Timestamps
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at      TIMESTAMPTZ,
    completed_at    TIMESTAMPTZ,
    -- Initiated by
    initiated_by    UUID REFERENCES users(id)
);

CREATE INDEX idx_runs_project ON runs (project_id);
CREATE INDEX idx_runs_workflow ON runs (workflow_id) WHERE workflow_id IS NOT NULL;
CREATE INDEX idx_runs_status ON runs (status);
CREATE INDEX idx_runs_created_at ON runs (created_at DESC);

-- ── Events (append-only run log + episodic memory) ────────────

CREATE TABLE events (
    id              BIGSERIAL PRIMARY KEY,
    -- External UUID for API exposure
    ext_id          UUID NOT NULL DEFAULT uuid_generate_v4() UNIQUE,
    run_id          UUID NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    -- Sequence within the run (for ordering)
    seq             INT NOT NULL,
    kind            event_kind NOT NULL,
    -- Agent that produced this event (if applicable)
    agent_instance_id UUID REFERENCES agent_instances(id),
    -- Structured payload
    payload         JSONB NOT NULL DEFAULT '{}',
    -- Token & cost accounting
    input_tokens    INT,
    output_tokens   INT,
    cost_cents      INT,
    -- Timestamp (immutable)
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_events_run ON events (run_id, seq);
CREATE INDEX idx_events_project_time ON events (project_id, created_at DESC);
CREATE INDEX idx_events_kind ON events (kind);

-- Prevent any UPDATE or DELETE on events (append-only enforcement at DB level)
CREATE RULE events_no_update AS ON UPDATE TO events DO INSTEAD NOTHING;
CREATE RULE events_no_delete AS ON DELETE TO events DO INSTEAD NOTHING;

-- ── Memory Records ────────────────────────────────────────────

CREATE TABLE memory_records (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    memory_type     memory_type NOT NULL,
    scope           memory_scope NOT NULL DEFAULT 'project',
    trust_tier      trust_tier NOT NULL DEFAULT 'observed',
    -- The actual content
    content         TEXT NOT NULL,
    -- Vector embedding (1536 dims for ada-002; 3072 for claude-3)
    embedding       vector(1536),
    -- Metadata & provenance
    metadata        JSONB NOT NULL DEFAULT '{}',
    -- Provenance
    created_by_agent_id UUID REFERENCES agent_instances(id),
    created_by_run_id   UUID REFERENCES runs(id),
    source_url      TEXT,
    -- Quality signals
    confidence      FLOAT NOT NULL DEFAULT 0.8 CHECK (confidence >= 0 AND confidence <= 1),
    -- Decay: updated each time the record is accessed / reinforced
    last_accessed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    access_count    INT NOT NULL DEFAULT 0,
    -- Lifecycle
    is_quarantined  BOOLEAN NOT NULL DEFAULT false,
    quarantine_reason TEXT,
    expires_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_memory_project_type ON memory_records (project_id, memory_type);
CREATE INDEX idx_memory_scope ON memory_records (scope);
CREATE INDEX idx_memory_trust ON memory_records (trust_tier);
CREATE INDEX idx_memory_content_trgm ON memory_records USING gin (content gin_trgm_ops);
-- Vector index (IVFFlat — tune lists based on row count at scale)
CREATE INDEX idx_memory_embedding ON memory_records USING ivfflat (embedding vector_cosine_ops)
    WITH (lists = 100);

-- ── Knowledge Graph (entities + relations) ───────────────────

CREATE TABLE memory_entities (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    entity_type     TEXT NOT NULL,   -- e.g. 'customer', 'feature', 'competitor', 'decision'
    name            TEXT NOT NULL,
    properties      JSONB NOT NULL DEFAULT '{}',
    trust_tier      trust_tier NOT NULL DEFAULT 'observed',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, entity_type, name)
);

CREATE INDEX idx_memory_entities_project ON memory_entities (project_id, entity_type);

CREATE TABLE memory_relations (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    from_entity_id  UUID NOT NULL REFERENCES memory_entities(id) ON DELETE CASCADE,
    to_entity_id    UUID NOT NULL REFERENCES memory_entities(id) ON DELETE CASCADE,
    relation_type   TEXT NOT NULL,   -- e.g. 'competes_with', 'requested_by', 'implements'
    properties      JSONB NOT NULL DEFAULT '{}',
    confidence      FLOAT NOT NULL DEFAULT 0.8,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_memory_relations_from ON memory_relations (from_entity_id);
CREATE INDEX idx_memory_relations_to ON memory_relations (to_entity_id);
CREATE INDEX idx_memory_relations_project ON memory_relations (project_id, relation_type);

-- ── Skills (procedural memory) ────────────────────────────────

CREATE TABLE skills (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    slug            TEXT NOT NULL,
    description     TEXT NOT NULL,
    -- agentskills.io compatible content
    content         TEXT NOT NULL,
    version         INT NOT NULL DEFAULT 1,
    -- Which agent authored this skill
    authored_by_agent_id UUID REFERENCES agent_instances(id),
    use_count       INT NOT NULL DEFAULT 0,
    success_rate    FLOAT,
    scope           memory_scope NOT NULL DEFAULT 'project',
    is_active       BOOLEAN NOT NULL DEFAULT true,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, slug)
);

-- ── Budgets & Spend Ledger ────────────────────────────────────

CREATE TABLE budgets (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    -- Scope: agent, workflow, or project level
    scope_type      TEXT NOT NULL CHECK (scope_type IN ('agent', 'workflow', 'project')),
    scope_id        UUID NOT NULL,   -- agent_instance_id or workflow_id or project_id
    -- Limits in USD cents
    limit_cents     BIGINT NOT NULL,
    -- Warning threshold (default 80%)
    warning_pct     INT NOT NULL DEFAULT 80,
    -- Reset period: 'monthly' | 'weekly' | 'never'
    reset_period    TEXT NOT NULL DEFAULT 'monthly',
    next_reset_at   TIMESTAMPTZ,
    is_active       BOOLEAN NOT NULL DEFAULT true,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_budgets_project ON budgets (project_id);
CREATE INDEX idx_budgets_scope ON budgets (scope_type, scope_id);

-- Append-only spend ledger
CREATE TABLE spend_ledger (
    id              BIGSERIAL PRIMARY KEY,
    ext_id          UUID NOT NULL DEFAULT uuid_generate_v4() UNIQUE,
    project_id      UUID NOT NULL REFERENCES projects(id),
    run_id          UUID NOT NULL REFERENCES runs(id),
    event_id        BIGINT REFERENCES events(id),
    agent_instance_id UUID REFERENCES agent_instances(id),
    -- Amount in USD cents
    amount_cents    INT NOT NULL,
    -- Model call details
    provider        provider_name,
    model           TEXT,
    input_tokens    INT,
    output_tokens   INT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_spend_ledger_project ON spend_ledger (project_id);
CREATE INDEX idx_spend_ledger_run ON spend_ledger (run_id);
CREATE RULE spend_ledger_no_update AS ON UPDATE TO spend_ledger DO INSTEAD NOTHING;
CREATE RULE spend_ledger_no_delete AS ON DELETE TO spend_ledger DO INSTEAD NOTHING;

-- ── Approval Requests ─────────────────────────────────────────

CREATE TABLE approval_requests (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    run_id          UUID NOT NULL REFERENCES runs(id),
    -- What triggered this approval request
    reason_type     TEXT NOT NULL,  -- 'agent_instantiation' | 'deployment' | 'spend_threshold' | 'memory_promotion' | 'custom'
    reason_detail   TEXT NOT NULL,
    -- Payload the approver is reviewing
    payload         JSONB NOT NULL DEFAULT '{}',
    status          approval_status NOT NULL DEFAULT 'pending',
    -- Who resolved it
    resolved_by     UUID REFERENCES users(id),
    resolution_note TEXT,
    -- Expires after this time (run stays paused until resolved or expired)
    expires_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at     TIMESTAMPTZ
);

CREATE INDEX idx_approvals_project ON approval_requests (project_id, status);
CREATE INDEX idx_approvals_run ON approval_requests (run_id);

-- ── Audit Log (immutable, hash-chained) ──────────────────────

CREATE TABLE audit_events (
    id              BIGSERIAL PRIMARY KEY,
    ext_id          UUID NOT NULL DEFAULT uuid_generate_v4() UNIQUE,
    org_id          UUID NOT NULL REFERENCES organizations(id),
    project_id      UUID REFERENCES projects(id),
    -- Actor
    actor_type      TEXT NOT NULL,  -- 'user' | 'agent' | 'system'
    actor_id        UUID,
    actor_name      TEXT,
    -- Action
    action          TEXT NOT NULL,
    resource_type   TEXT,
    resource_id     UUID,
    -- Outcome
    outcome         TEXT NOT NULL CHECK (outcome IN ('success', 'failure', 'blocked')),
    detail          JSONB NOT NULL DEFAULT '{}',
    -- Hash chain: SHA-256(prev_hash || id || action || created_at)
    prev_hash       TEXT,
    row_hash        TEXT NOT NULL,
    -- Timestamp (immutable)
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_audit_org ON audit_events (org_id, created_at DESC);
CREATE INDEX idx_audit_project ON audit_events (project_id, created_at DESC) WHERE project_id IS NOT NULL;
CREATE INDEX idx_audit_actor ON audit_events (actor_type, actor_id);
CREATE RULE audit_no_update AS ON UPDATE TO audit_events DO INSTEAD NOTHING;
CREATE RULE audit_no_delete AS ON DELETE TO audit_events DO INSTEAD NOTHING;
