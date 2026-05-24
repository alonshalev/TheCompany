-- ============================================================
-- Symbiont — Migration 003: Blueprints + Marketplace
-- ============================================================
-- Adds tables for:
--   blueprints           — reusable SaaS scaffold templates (org-scoped)
--   blueprint_instances  — tracking which blueprints a project has instantiated
--   marketplace_plugins  — registry of available skill / tool plugins
-- ============================================================

-- ── Blueprint category enum ───────────────────────────────────
CREATE TYPE blueprint_category AS ENUM (
    'crm',
    'ecommerce',
    'support',
    'devops',
    'analytics',
    'marketing',
    'finance',
    'security',
    'custom'
);

-- ── Blueprints ────────────────────────────────────────────────
-- A blueprint is a versioned, composable SaaS scaffold.
-- Each blueprint contains a set of agent specs, workflows, and
-- optional memory seeds that get instantiated together into a project.
CREATE TABLE blueprints (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    org_id          UUID REFERENCES organizations(id) ON DELETE CASCADE,
    -- org_id NULL  => built-in (system) blueprint available to all orgs
    name            TEXT NOT NULL,
    slug            TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    category        blueprint_category NOT NULL DEFAULT 'custom',
    version         INT  NOT NULL DEFAULT 1,
    -- components holds the full blueprint definition as JSONB:
    --   { agent_specs: [...], workflows: [...], memory_seeds: [...] }
    components      JSONB NOT NULL DEFAULT '{}',
    -- tags for search/filtering
    tags            TEXT[] NOT NULL DEFAULT '{}',
    is_active       BOOL NOT NULL DEFAULT true,
    created_by      UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (slug, COALESCE(org_id, '00000000-0000-0000-0000-000000000000'::UUID))
);

CREATE INDEX idx_blueprints_org_id    ON blueprints(org_id) WHERE org_id IS NOT NULL;
CREATE INDEX idx_blueprints_category  ON blueprints(category);
CREATE INDEX idx_blueprints_is_active ON blueprints(is_active);
CREATE INDEX idx_blueprints_tags      ON blueprints USING GIN(tags);

-- ── Blueprint instances ───────────────────────────────────────
-- Tracks which blueprints have been applied to each project.
CREATE TABLE blueprint_instances (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    blueprint_id    UUID NOT NULL REFERENCES blueprints(id) ON DELETE RESTRICT,
    blueprint_version INT NOT NULL,
    -- created_resources records the IDs of all resources spawned during instantiation
    --   { agent_spec_ids: [...], workflow_ids: [...] }
    created_resources JSONB NOT NULL DEFAULT '{}',
    instantiated_by   UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_blueprint_instances_project    ON blueprint_instances(project_id);
CREATE INDEX idx_blueprint_instances_blueprint  ON blueprint_instances(blueprint_id);

-- ── Marketplace plugins ───────────────────────────────────────
-- Registry of available plugins/integrations that can be added to a project.
CREATE TABLE marketplace_plugins (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name            TEXT NOT NULL,
    slug            TEXT NOT NULL UNIQUE,
    description     TEXT NOT NULL DEFAULT '',
    author          TEXT NOT NULL DEFAULT 'Symbiont',
    version         TEXT NOT NULL DEFAULT '1.0.0',
    category        TEXT NOT NULL DEFAULT 'integration',
    -- manifest is the full plugin spec (tools, config schema, etc.)
    manifest        JSONB NOT NULL DEFAULT '{}',
    icon_url        TEXT,
    docs_url        TEXT,
    is_verified     BOOL NOT NULL DEFAULT false,
    is_active       BOOL NOT NULL DEFAULT true,
    install_count   INT  NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_marketplace_plugins_category ON marketplace_plugins(category);
CREATE INDEX idx_marketplace_plugins_active   ON marketplace_plugins(is_active);

-- ── Seed built-in blueprints ──────────────────────────────────
-- These are system-level blueprints (org_id = NULL) available to all orgs.

INSERT INTO blueprints (id, name, slug, description, category, components, tags, created_at, updated_at)
VALUES
(
    uuid_generate_v4(),
    'Customer Support Agent',
    'customer-support-agent',
    'A complete customer support automation blueprint: triage agent, escalation workflow, and knowledge-base memory seeding.',
    'support',
    '{
        "agent_specs": [
            {
                "name": "Support Triage",
                "slug": "support-triage",
                "role": "Customer Support Triage",
                "goal": "Classify and respond to incoming support requests; escalate complex issues to human agents",
                "model": "claude-sonnet-4-6",
                "tool_grants": ["search_knowledge_base", "create_ticket", "escalate_to_human"]
            }
        ],
        "workflows": [
            {
                "name": "Support Triage Flow",
                "slug": "support-triage-flow",
                "definition": {
                    "nodes": [
                        {"id": "trigger", "kind": "trigger", "label": "Incoming Request", "config": {"kind": "webhook"}},
                        {"id": "triage", "kind": "agent", "label": "Triage Agent"},
                        {"id": "branch", "kind": "branch", "label": "Complex?"},
                        {"id": "auto-reply", "kind": "agent", "label": "Auto Reply"},
                        {"id": "escalate", "kind": "approval_gate", "label": "Escalate to Human"}
                    ],
                    "edges": [
                        {"id": "e1", "source": "trigger", "target": "triage"},
                        {"id": "e2", "source": "triage", "target": "branch"},
                        {"id": "e3", "source": "branch", "target": "auto-reply", "condition": "simple"},
                        {"id": "e4", "source": "branch", "target": "escalate", "condition": "complex"}
                    ]
                }
            }
        ],
        "memory_seeds": []
    }',
    ARRAY['support', 'customer-service', 'triage'],
    now(), now()
),
(
    uuid_generate_v4(),
    'SaaS Onboarding Automation',
    'saas-onboarding',
    'Automates user onboarding: welcome emails, setup guidance, feature discovery, and early-churn detection.',
    'crm',
    '{
        "agent_specs": [
            {
                "name": "Onboarding Concierge",
                "slug": "onboarding-concierge",
                "role": "Onboarding Specialist",
                "goal": "Guide new users through product setup, send personalised tips, and flag early churn signals",
                "model": "claude-sonnet-4-6",
                "tool_grants": ["send_email", "update_crm", "query_usage_events", "create_task"]
            }
        ],
        "workflows": [
            {
                "name": "User Onboarding Flow",
                "slug": "user-onboarding-flow",
                "definition": {
                    "nodes": [
                        {"id": "trigger", "kind": "trigger", "label": "User Signed Up", "config": {"kind": "webhook"}},
                        {"id": "welcome", "kind": "agent", "label": "Send Welcome"},
                        {"id": "day3-check", "kind": "agent", "label": "Day 3 Check-in"},
                        {"id": "churn-risk", "kind": "branch", "label": "Churn Risk?"},
                        {"id": "nurture", "kind": "agent", "label": "Send Nurture"},
                        {"id": "alert", "kind": "approval_gate", "label": "Alert CS Team"}
                    ],
                    "edges": [
                        {"id": "e1", "source": "trigger", "target": "welcome"},
                        {"id": "e2", "source": "welcome", "target": "day3-check"},
                        {"id": "e3", "source": "day3-check", "target": "churn-risk"},
                        {"id": "e4", "source": "churn-risk", "target": "nurture", "condition": "low_risk"},
                        {"id": "e5", "source": "churn-risk", "target": "alert", "condition": "high_risk"}
                    ]
                }
            }
        ],
        "memory_seeds": []
    }',
    ARRAY['saas', 'onboarding', 'crm', 'email'],
    now(), now()
),
(
    uuid_generate_v4(),
    'Security Incident Response',
    'security-incident-response',
    'Automates security incident detection, triage, containment recommendations, and post-incident reporting.',
    'security',
    '{
        "agent_specs": [
            {
                "name": "Security Analyst",
                "slug": "security-analyst",
                "role": "Security Incident Analyst",
                "goal": "Analyse security events, classify severity, recommend containment steps, draft incident reports",
                "model": "claude-opus-4-6",
                "tool_grants": ["query_siem", "lookup_threat_intel", "create_incident_ticket", "notify_slack"]
            }
        ],
        "workflows": [
            {
                "name": "Incident Response Flow",
                "slug": "incident-response-flow",
                "definition": {
                    "nodes": [
                        {"id": "trigger", "kind": "trigger", "label": "Alert Received", "config": {"kind": "webhook"}},
                        {"id": "triage", "kind": "agent", "label": "Triage Severity"},
                        {"id": "severity-branch", "kind": "branch", "label": "Severity?"},
                        {"id": "auto-contain", "kind": "agent", "label": "Auto Contain P3/P4"},
                        {"id": "human-approval", "kind": "approval_gate", "label": "Approve P1/P2 Action"},
                        {"id": "report", "kind": "agent", "label": "Generate Report"}
                    ],
                    "edges": [
                        {"id": "e1", "source": "trigger", "target": "triage"},
                        {"id": "e2", "source": "triage", "target": "severity-branch"},
                        {"id": "e3", "source": "severity-branch", "target": "auto-contain", "condition": "low"},
                        {"id": "e4", "source": "severity-branch", "target": "human-approval", "condition": "high"},
                        {"id": "e5", "source": "auto-contain", "target": "report"},
                        {"id": "e6", "source": "human-approval", "target": "report"}
                    ]
                }
            }
        ],
        "memory_seeds": []
    }',
    ARRAY['security', 'incident-response', 'siem', 'compliance'],
    now(), now()
),
(
    uuid_generate_v4(),
    'Dev Ops Release Agent',
    'devops-release-agent',
    'Automates release readiness checks, changelog generation, deployment approvals, and post-deploy monitoring.',
    'devops',
    '{
        "agent_specs": [
            {
                "name": "Release Manager",
                "slug": "release-manager",
                "role": "Release Automation Agent",
                "goal": "Coordinate release readiness, generate changelogs from commits, manage deployment approvals, and watch post-deploy metrics",
                "model": "claude-sonnet-4-6",
                "tool_grants": ["query_github", "run_ci_check", "post_slack_message", "query_metrics"]
            }
        ],
        "workflows": [
            {
                "name": "Release Flow",
                "slug": "release-flow",
                "definition": {
                    "nodes": [
                        {"id": "trigger", "kind": "trigger", "label": "Release Tag Pushed", "config": {"kind": "webhook"}},
                        {"id": "changelog", "kind": "agent", "label": "Generate Changelog"},
                        {"id": "ci-check", "kind": "agent", "label": "Check CI Status"},
                        {"id": "approve", "kind": "approval_gate", "label": "Release Approval"},
                        {"id": "deploy", "kind": "agent", "label": "Trigger Deploy"},
                        {"id": "monitor", "kind": "agent", "label": "Post-deploy Monitor"}
                    ],
                    "edges": [
                        {"id": "e1", "source": "trigger", "target": "changelog"},
                        {"id": "e2", "source": "changelog", "target": "ci-check"},
                        {"id": "e3", "source": "ci-check", "target": "approve"},
                        {"id": "e4", "source": "approve", "target": "deploy"},
                        {"id": "e5", "source": "deploy", "target": "monitor"}
                    ]
                }
            }
        ],
        "memory_seeds": []
    }',
    ARRAY['devops', 'release', 'ci-cd', 'deployment'],
    now(), now()
),
(
    uuid_generate_v4(),
    'Market Research Assistant',
    'market-research-assistant',
    'Continuously monitors competitors, synthesises market signals, and generates structured research reports.',
    'analytics',
    '{
        "agent_specs": [
            {
                "name": "Market Researcher",
                "slug": "market-researcher",
                "role": "Market Intelligence Analyst",
                "goal": "Monitor competitor activity, collect market signals, and produce weekly intelligence briefs",
                "model": "claude-sonnet-4-6",
                "tool_grants": ["web_search", "scrape_url", "write_memory", "create_report"]
            }
        ],
        "workflows": [
            {
                "name": "Weekly Market Brief",
                "slug": "weekly-market-brief",
                "definition": {
                    "nodes": [
                        {"id": "trigger", "kind": "trigger", "label": "Weekly Cron", "config": {"kind": "cron", "cron_expression": "0 8 * * 1"}},
                        {"id": "gather", "kind": "agent", "label": "Gather Signals"},
                        {"id": "analyse", "kind": "agent", "label": "Analyse & Synthesise"},
                        {"id": "report", "kind": "agent", "label": "Write Brief"}
                    ],
                    "edges": [
                        {"id": "e1", "source": "trigger", "target": "gather"},
                        {"id": "e2", "source": "gather", "target": "analyse"},
                        {"id": "e3", "source": "analyse", "target": "report"}
                    ]
                }
            }
        ],
        "memory_seeds": []
    }',
    ARRAY['analytics', 'market-research', 'competitive-intel'],
    now(), now()
);

-- ── Seed built-in marketplace plugins ────────────────────────

INSERT INTO marketplace_plugins (name, slug, description, author, version, category, manifest, is_verified, created_at, updated_at)
VALUES
(
    'Slack Notifier',
    'slack-notifier',
    'Send messages, create channels, and post rich blocks to Slack.',
    'Symbiont',
    '1.0.0',
    'communication',
    '{"tools": ["send_message", "create_channel", "post_blocks"], "config_schema": {"bot_token": {"type": "string", "secret": true}}}',
    true, now(), now()
),
(
    'GitHub Integration',
    'github-integration',
    'Create issues, PRs, query commits, and manage workflows via the GitHub API.',
    'Symbiont',
    '1.0.0',
    'devops',
    '{"tools": ["create_issue", "create_pr", "list_commits", "trigger_workflow"], "config_schema": {"personal_access_token": {"type": "string", "secret": true}}}',
    true, now(), now()
),
(
    'Jira Connector',
    'jira-connector',
    'Create and update Jira issues, query sprints, and manage project boards.',
    'Symbiont',
    '1.0.0',
    'project-management',
    '{"tools": ["create_issue", "update_issue", "query_sprint", "get_board"], "config_schema": {"base_url": {"type": "string"}, "api_token": {"type": "string", "secret": true}, "email": {"type": "string"}}}',
    true, now(), now()
),
(
    'PagerDuty Alerts',
    'pagerduty-alerts',
    'Trigger incidents, acknowledge alerts, and query on-call schedules via PagerDuty.',
    'Symbiont',
    '1.0.0',
    'ops',
    '{"tools": ["trigger_incident", "acknowledge", "resolve", "get_oncall"], "config_schema": {"api_key": {"type": "string", "secret": true}}}',
    true, now(), now()
),
(
    'Web Search',
    'web-search',
    'Real-time web search via a configurable search engine (Brave, SerpAPI, Bing).',
    'Symbiont',
    '1.0.0',
    'data',
    '{"tools": ["search", "get_page_text"], "config_schema": {"provider": {"type": "string", "enum": ["brave", "serpapi", "bing"]}, "api_key": {"type": "string", "secret": true}}}',
    true, now(), now()
),
(
    'PostgreSQL Query',
    'postgres-query',
    'Execute read-only SQL queries against an external PostgreSQL database.',
    'Symbiont',
    '1.0.0',
    'data',
    '{"tools": ["query", "describe_table", "list_tables"], "config_schema": {"connection_string": {"type": "string", "secret": true}}}',
    true, now(), now()
),
(
    'SendGrid Email',
    'sendgrid-email',
    'Send transactional and marketing emails via SendGrid.',
    'Symbiont',
    '1.0.0',
    'communication',
    '{"tools": ["send_email", "send_template_email", "get_stats"], "config_schema": {"api_key": {"type": "string", "secret": true}}}',
    true, now(), now()
),
(
    'Stripe Payments',
    'stripe-payments',
    'Query customers, invoices, and subscriptions — read-only billing intelligence.',
    'Symbiont',
    '1.0.0',
    'finance',
    '{"tools": ["get_customer", "list_invoices", "get_subscription", "list_charges"], "config_schema": {"secret_key": {"type": "string", "secret": true}}}',
    true, now(), now()
);
