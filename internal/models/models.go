// Package models defines all core domain types for Symbiont.
// These mirror the database schema in migrations/001_init.up.sql.
package models

import (
	"time"

	"github.com/google/uuid"
)

// ── Enumerations ─────────────────────────────────────────────

type UserRole string

const (
	UserRoleOwner  UserRole = "owner"
	UserRoleAdmin  UserRole = "admin"
	UserRoleMember UserRole = "member"
	UserRoleViewer UserRole = "viewer"
)

type RunStatus string

const (
	RunStatusPending   RunStatus = "pending"
	RunStatusRunning   RunStatus = "running"
	RunStatusPaused    RunStatus = "paused"
	RunStatusSucceeded RunStatus = "succeeded"
	RunStatusFailed    RunStatus = "failed"
	RunStatusCancelled RunStatus = "cancelled"
)

type EventKind string

const (
	EventKindRunStarted        EventKind = "run_started"
	EventKindRunSucceeded      EventKind = "run_succeeded"
	EventKindRunFailed         EventKind = "run_failed"
	EventKindRunCancelled      EventKind = "run_cancelled"
	EventKindNodeStarted       EventKind = "node_started"
	EventKindNodeSucceeded     EventKind = "node_succeeded"
	EventKindNodeFailed        EventKind = "node_failed"
	EventKindAgentStep         EventKind = "agent_step"
	EventKindModelCall         EventKind = "model_call"
	EventKindToolCall          EventKind = "tool_call"
	EventKindToolResult        EventKind = "tool_result"
	EventKindMemoryWrite       EventKind = "memory_write"
	EventKindMemoryRead        EventKind = "memory_read"
	EventKindApprovalRequested EventKind = "approval_requested"
	EventKindApprovalResolved  EventKind = "approval_resolved"
	EventKindSpendMetered      EventKind = "spend_metered"
	EventKindBudgetWarning     EventKind = "budget_warning"
	EventKindBudgetExceeded    EventKind = "budget_exceeded"
)

type MemoryType string

const (
	MemoryTypeWorking   MemoryType = "working"
	MemoryTypeEpisodic  MemoryType = "episodic"
	MemoryTypeSemantic  MemoryType = "semantic"
	MemoryTypeProcedural MemoryType = "procedural"
)

type MemoryScope string

const (
	MemoryScopePrivate  MemoryScope = "private"
	MemoryScopeAgent    MemoryScope = "agent"
	MemoryScopeWorkflow MemoryScope = "workflow"
	MemoryScopeProject  MemoryScope = "project"
	MemoryScopeGlobal   MemoryScope = "global"
)

type TrustTier string

const (
	TrustTierVerified  TrustTier = "verified"
	TrustTierObserved  TrustTier = "observed"
	TrustTierUntrusted TrustTier = "untrusted"
)

type ApprovalStatus string

const (
	ApprovalStatusPending  ApprovalStatus = "pending"
	ApprovalStatusApproved ApprovalStatus = "approved"
	ApprovalStatusRejected ApprovalStatus = "rejected"
	ApprovalStatusExpired  ApprovalStatus = "expired"
)

type ProviderName string

const (
	ProviderAnthropic  ProviderName = "anthropic"
	ProviderOpenAI     ProviderName = "openai"
	ProviderOpenRouter ProviderName = "openrouter"
	ProviderOllama     ProviderName = "ollama"
	ProviderCustom     ProviderName = "custom"
)

type TriggerKind string

const (
	TriggerKindCron          TriggerKind = "cron"
	TriggerKindWebhook       TriggerKind = "webhook"
	TriggerKindInternalEvent TriggerKind = "internal_event"
	TriggerKindManual        TriggerKind = "manual"
)

// ── Core Entities ─────────────────────────────────────────────

type Organization struct {
	ID        uuid.UUID `db:"id"         json:"id"`
	Name      string    `db:"name"       json:"name"`
	Slug      string    `db:"slug"       json:"slug"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
	UpdatedAt time.Time `db:"updated_at" json:"updated_at"`
}

type User struct {
	ID             uuid.UUID  `db:"id"              json:"id"`
	OrgID          uuid.UUID  `db:"org_id"          json:"org_id"`
	Email          string     `db:"email"           json:"email"`
	Name           string     `db:"name"            json:"name"`
	Role           UserRole   `db:"role"            json:"role"`
	OIDCSubject    *string    `db:"oidc_subject"    json:"-"`
	OIDCProvider   *string    `db:"oidc_provider"   json:"-"`
	APIKeyHash     *string    `db:"api_key_hash"    json:"-"`
	APIKeyPrefix   *string    `db:"api_key_prefix"  json:"api_key_prefix,omitempty"`
	CreatedAt      time.Time  `db:"created_at"      json:"created_at"`
	UpdatedAt      time.Time  `db:"updated_at"      json:"updated_at"`
	LastSeenAt     *time.Time `db:"last_seen_at"    json:"last_seen_at,omitempty"`
}

type Project struct {
	ID          uuid.UUID  `db:"id"           json:"id"`
	OrgID       uuid.UUID  `db:"org_id"       json:"org_id"`
	Name        string     `db:"name"         json:"name"`
	Slug        string     `db:"slug"         json:"slug"`
	Description *string    `db:"description"  json:"description,omitempty"`
	BudgetCents int64      `db:"budget_cents" json:"budget_cents"`
	SpendCents  int64      `db:"spend_cents"  json:"spend_cents"`
	Settings    JSONMap    `db:"settings"     json:"settings"`
	CreatedAt   time.Time  `db:"created_at"   json:"created_at"`
	UpdatedAt   time.Time  `db:"updated_at"   json:"updated_at"`
	ArchivedAt  *time.Time `db:"archived_at"  json:"archived_at,omitempty"`
}

type ProviderConfig struct {
	ID        uuid.UUID    `db:"id"         json:"id"`
	OrgID     uuid.UUID    `db:"org_id"     json:"org_id"`
	Name      string       `db:"name"       json:"name"`
	Provider  ProviderName `db:"provider"   json:"provider"`
	SecretRef string       `db:"secret_ref" json:"-"` // never expose
	Settings  JSONMap      `db:"settings"   json:"settings"`
	IsDefault bool         `db:"is_default" json:"is_default"`
	CreatedAt time.Time    `db:"created_at" json:"created_at"`
	UpdatedAt time.Time    `db:"updated_at" json:"updated_at"`
}

type AgentSpec struct {
	ID                 uuid.UUID    `db:"id"                   json:"id"`
	ProjectID          uuid.UUID    `db:"project_id"           json:"project_id"`
	Name               string       `db:"name"                 json:"name"`
	Slug               string       `db:"slug"                 json:"slug"`
	Version            int          `db:"version"              json:"version"`
	Role               string       `db:"role"                 json:"role"`
	Goal               string       `db:"goal"                 json:"goal"`
	SystemInstructions string       `db:"system_instructions"  json:"system_instructions"`
	ProviderConfigID   *uuid.UUID   `db:"provider_config_id"   json:"provider_config_id,omitempty"`
	Model              string       `db:"model"                json:"model"`
	MemoryReadScopes   []MemoryScope `db:"memory_read_scopes"  json:"memory_read_scopes"`
	MemoryWriteScopes  []MemoryScope `db:"memory_write_scopes" json:"memory_write_scopes"`
	BudgetCents        int64        `db:"budget_cents"         json:"budget_cents"`
	ToolGrants         JSONSlice    `db:"tool_grants"          json:"tool_grants"`
	Guardrails         JSONMap      `db:"guardrails"           json:"guardrails"`
	SynthesizedFrom    *string      `db:"synthesized_from"     json:"synthesized_from,omitempty"`
	TemplateID         *uuid.UUID   `db:"template_id"          json:"template_id,omitempty"`
	IsActive           bool         `db:"is_active"            json:"is_active"`
	CreatedBy          *uuid.UUID   `db:"created_by"           json:"created_by,omitempty"`
	CreatedAt          time.Time    `db:"created_at"           json:"created_at"`
	UpdatedAt          time.Time    `db:"updated_at"           json:"updated_at"`
}

type AgentInstance struct {
	ID          uuid.UUID `db:"id"           json:"id"`
	ProjectID   uuid.UUID `db:"project_id"   json:"project_id"`
	SpecID      uuid.UUID `db:"spec_id"      json:"spec_id"`
	Name        string    `db:"name"         json:"name"`
	SpendCents  int64     `db:"spend_cents"  json:"spend_cents"`
	IsActive    bool      `db:"is_active"    json:"is_active"`
	CreatedAt   time.Time `db:"created_at"   json:"created_at"`
	UpdatedAt   time.Time `db:"updated_at"   json:"updated_at"`
}

type MCPServer struct {
	ID               uuid.UUID  `db:"id"                json:"id"`
	OrgID            uuid.UUID  `db:"org_id"            json:"org_id"`
	Name             string     `db:"name"              json:"name"`
	Description      *string    `db:"description"       json:"description,omitempty"`
	Transport        string     `db:"transport"         json:"transport"`
	EndpointURL      *string    `db:"endpoint_url"      json:"endpoint_url,omitempty"`
	DiscoveredTools  JSONSlice  `db:"discovered_tools"  json:"discovered_tools"`
	Settings         JSONMap    `db:"settings"          json:"settings"`
	IsActive         bool       `db:"is_active"         json:"is_active"`
	LastPingAt       *time.Time `db:"last_ping_at"      json:"last_ping_at,omitempty"`
	CreatedAt        time.Time  `db:"created_at"        json:"created_at"`
	UpdatedAt        time.Time  `db:"updated_at"        json:"updated_at"`
}

type Workflow struct {
	ID          uuid.UUID  `db:"id"          json:"id"`
	ProjectID   uuid.UUID  `db:"project_id"  json:"project_id"`
	Name        string     `db:"name"        json:"name"`
	Slug        string     `db:"slug"        json:"slug"`
	Description *string    `db:"description" json:"description,omitempty"`
	Version     int        `db:"version"     json:"version"`
	Definition  JSONMap    `db:"definition"  json:"definition"`
	IsActive    bool       `db:"is_active"   json:"is_active"`
	CreatedBy   *uuid.UUID `db:"created_by"  json:"created_by,omitempty"`
	CreatedAt   time.Time  `db:"created_at"  json:"created_at"`
	UpdatedAt   time.Time  `db:"updated_at"  json:"updated_at"`
}

type Trigger struct {
	ID          uuid.UUID   `db:"id"           json:"id"`
	WorkflowID  uuid.UUID   `db:"workflow_id"  json:"workflow_id"`
	Kind        TriggerKind `db:"kind"         json:"kind"`
	CronExpr    *string     `db:"cron_expr"    json:"cron_expr,omitempty"`
	WebhookPath *string     `db:"webhook_path" json:"webhook_path,omitempty"`
	Settings    JSONMap     `db:"settings"     json:"settings"`
	IsActive    bool        `db:"is_active"    json:"is_active"`
	LastFiredAt *time.Time  `db:"last_fired_at" json:"last_fired_at,omitempty"`
	CreatedAt   time.Time   `db:"created_at"   json:"created_at"`
}

type Run struct {
	ID              uuid.UUID   `db:"id"                json:"id"`
	ProjectID       uuid.UUID   `db:"project_id"        json:"project_id"`
	WorkflowID      *uuid.UUID  `db:"workflow_id"       json:"workflow_id,omitempty"`
	AgentInstanceID *uuid.UUID  `db:"agent_instance_id" json:"agent_instance_id,omitempty"`
	TriggerID       *uuid.UUID  `db:"trigger_id"        json:"trigger_id,omitempty"`
	TriggerKind     *TriggerKind `db:"trigger_kind"     json:"trigger_kind,omitempty"`
	Input           JSONMap     `db:"input"             json:"input"`
	Status          RunStatus   `db:"status"            json:"status"`
	SpendCents      int64       `db:"spend_cents"       json:"spend_cents"`
	Error           *string     `db:"error"             json:"error,omitempty"`
	CreatedAt       time.Time   `db:"created_at"        json:"created_at"`
	StartedAt       *time.Time  `db:"started_at"        json:"started_at,omitempty"`
	CompletedAt     *time.Time  `db:"completed_at"      json:"completed_at,omitempty"`
	InitiatedBy     *uuid.UUID  `db:"initiated_by"      json:"initiated_by,omitempty"`
}

type Event struct {
	ID              int64      `db:"id"               json:"id"`
	ExtID           uuid.UUID  `db:"ext_id"           json:"ext_id"`
	RunID           uuid.UUID  `db:"run_id"           json:"run_id"`
	ProjectID       uuid.UUID  `db:"project_id"       json:"project_id"`
	Seq             int        `db:"seq"              json:"seq"`
	Kind            EventKind  `db:"kind"             json:"kind"`
	AgentInstanceID *uuid.UUID `db:"agent_instance_id" json:"agent_instance_id,omitempty"`
	Payload         JSONMap    `db:"payload"          json:"payload"`
	InputTokens     *int       `db:"input_tokens"     json:"input_tokens,omitempty"`
	OutputTokens    *int       `db:"output_tokens"    json:"output_tokens,omitempty"`
	CostCents       *int       `db:"cost_cents"       json:"cost_cents,omitempty"`
	CreatedAt       time.Time  `db:"created_at"       json:"created_at"`
}

type MemoryRecord struct {
	ID                uuid.UUID   `db:"id"                    json:"id"`
	ProjectID         uuid.UUID   `db:"project_id"            json:"project_id"`
	MemoryType        MemoryType  `db:"memory_type"           json:"memory_type"`
	Scope             MemoryScope `db:"scope"                 json:"scope"`
	TrustTier         TrustTier   `db:"trust_tier"            json:"trust_tier"`
	Content           string      `db:"content"               json:"content"`
	// Embedding is excluded from JSON by default; fetch separately when needed
	Metadata          JSONMap     `db:"metadata"              json:"metadata"`
	CreatedByAgentID  *uuid.UUID  `db:"created_by_agent_id"   json:"created_by_agent_id,omitempty"`
	CreatedByRunID    *uuid.UUID  `db:"created_by_run_id"     json:"created_by_run_id,omitempty"`
	SourceURL         *string     `db:"source_url"            json:"source_url,omitempty"`
	Confidence        float64     `db:"confidence"            json:"confidence"`
	LastAccessedAt    time.Time   `db:"last_accessed_at"      json:"last_accessed_at"`
	AccessCount       int         `db:"access_count"          json:"access_count"`
	IsQuarantined     bool        `db:"is_quarantined"        json:"is_quarantined"`
	QuarantineReason  *string     `db:"quarantine_reason"     json:"quarantine_reason,omitempty"`
	ExpiresAt         *time.Time  `db:"expires_at"            json:"expires_at,omitempty"`
	CreatedAt         time.Time   `db:"created_at"            json:"created_at"`
	UpdatedAt         time.Time   `db:"updated_at"            json:"updated_at"`
}

type MemoryEntity struct {
	ID         uuid.UUID `db:"id"          json:"id"`
	ProjectID  uuid.UUID `db:"project_id"  json:"project_id"`
	EntityType string    `db:"entity_type" json:"entity_type"`
	Name       string    `db:"name"        json:"name"`
	Properties JSONMap   `db:"properties"  json:"properties"`
	TrustTier  TrustTier `db:"trust_tier"  json:"trust_tier"`
	CreatedAt  time.Time `db:"created_at"  json:"created_at"`
	UpdatedAt  time.Time `db:"updated_at"  json:"updated_at"`
}

type MemoryRelation struct {
	ID           uuid.UUID `db:"id"             json:"id"`
	ProjectID    uuid.UUID `db:"project_id"     json:"project_id"`
	FromEntityID uuid.UUID `db:"from_entity_id" json:"from_entity_id"`
	ToEntityID   uuid.UUID `db:"to_entity_id"   json:"to_entity_id"`
	RelationType string    `db:"relation_type"  json:"relation_type"`
	Properties   JSONMap   `db:"properties"     json:"properties"`
	Confidence   float64   `db:"confidence"     json:"confidence"`
	CreatedAt    time.Time `db:"created_at"     json:"created_at"`
}

type Skill struct {
	ID               uuid.UUID   `db:"id"                    json:"id"`
	ProjectID        uuid.UUID   `db:"project_id"            json:"project_id"`
	Name             string      `db:"name"                  json:"name"`
	Slug             string      `db:"slug"                  json:"slug"`
	Description      string      `db:"description"           json:"description"`
	Content          string      `db:"content"               json:"content"`
	Version          int         `db:"version"               json:"version"`
	AuthoredByAgentID *uuid.UUID `db:"authored_by_agent_id"  json:"authored_by_agent_id,omitempty"`
	UseCount         int         `db:"use_count"             json:"use_count"`
	SuccessRate      *float64    `db:"success_rate"          json:"success_rate,omitempty"`
	Scope            MemoryScope `db:"scope"                 json:"scope"`
	IsActive         bool        `db:"is_active"             json:"is_active"`
	CreatedAt        time.Time   `db:"created_at"            json:"created_at"`
	UpdatedAt        time.Time   `db:"updated_at"            json:"updated_at"`
}

type Budget struct {
	ID           uuid.UUID  `db:"id"            json:"id"`
	ProjectID    uuid.UUID  `db:"project_id"    json:"project_id"`
	ScopeType    string     `db:"scope_type"    json:"scope_type"`
	ScopeID      uuid.UUID  `db:"scope_id"      json:"scope_id"`
	LimitCents   int64      `db:"limit_cents"   json:"limit_cents"`
	WarningPct   int        `db:"warning_pct"   json:"warning_pct"`
	ResetPeriod  string     `db:"reset_period"  json:"reset_period"`
	NextResetAt  *time.Time `db:"next_reset_at" json:"next_reset_at,omitempty"`
	IsActive     bool       `db:"is_active"     json:"is_active"`
	CreatedAt    time.Time  `db:"created_at"    json:"created_at"`
	UpdatedAt    time.Time  `db:"updated_at"    json:"updated_at"`
}

type ApprovalRequest struct {
	ID             uuid.UUID      `db:"id"              json:"id"`
	ProjectID      uuid.UUID      `db:"project_id"      json:"project_id"`
	RunID          uuid.UUID      `db:"run_id"          json:"run_id"`
	ReasonType     string         `db:"reason_type"     json:"reason_type"`
	ReasonDetail   string         `db:"reason_detail"   json:"reason_detail"`
	Payload        JSONMap        `db:"payload"         json:"payload"`
	Status         ApprovalStatus `db:"status"          json:"status"`
	ResolvedBy     *uuid.UUID     `db:"resolved_by"     json:"resolved_by,omitempty"`
	ResolutionNote *string        `db:"resolution_note" json:"resolution_note,omitempty"`
	ExpiresAt      *time.Time     `db:"expires_at"      json:"expires_at,omitempty"`
	CreatedAt      time.Time      `db:"created_at"      json:"created_at"`
	ResolvedAt     *time.Time     `db:"resolved_at"     json:"resolved_at,omitempty"`
}

type AuditEvent struct {
	ID           int64      `db:"id"            json:"id"`
	ExtID        uuid.UUID  `db:"ext_id"        json:"ext_id"`
	OrgID        uuid.UUID  `db:"org_id"        json:"org_id"`
	ProjectID    *uuid.UUID `db:"project_id"    json:"project_id,omitempty"`
	ActorType    string     `db:"actor_type"    json:"actor_type"`
	ActorID      *uuid.UUID `db:"actor_id"      json:"actor_id,omitempty"`
	ActorName    *string    `db:"actor_name"    json:"actor_name,omitempty"`
	Action       string     `db:"action"        json:"action"`
	ResourceType *string    `db:"resource_type" json:"resource_type,omitempty"`
	ResourceID   *uuid.UUID `db:"resource_id"   json:"resource_id,omitempty"`
	Outcome      string     `db:"outcome"       json:"outcome"`
	Detail       JSONMap    `db:"detail"        json:"detail"`
	PrevHash     *string    `db:"prev_hash"     json:"-"`
	RowHash      string     `db:"row_hash"      json:"-"`
	CreatedAt    time.Time  `db:"created_at"    json:"created_at"`
}
