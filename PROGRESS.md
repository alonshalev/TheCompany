# Symbiont — Build Progress

## ✅ PART 1 (M0 — Foundations) — COMPLETE

### Files created
| File | Purpose |
|------|---------|
| `go.mod` | Go module, all dependencies declared |
| `Makefile` | `make build`, `run`, `test`, `docker-up`, `dev`, `migrate-up/down` |
| `.env.example` | Every env var documented |
| `.gitignore` | Standard Go + Node ignores |
| `deployments/docker/docker-compose.yml` | Postgres + pgvector local dev |
| `migrations/001_init.up.sql` | Full schema: all 20 tables, enums, indexes, append-only rules |
| `migrations/001_init.down.sql` | Full rollback |
| `internal/config/config.go` | Typed config loading from env |
| `internal/models/models.go` | All Go structs (Organization → AuditEvent) |
| `internal/models/json_types.go` | JSONMap / JSONSlice for JSONB columns |
| `internal/db/db.go` | DB connect + migrate (golang-migrate, embedded migrations) |
| `internal/auth/auth.go` | API key generation, hashing |
| `internal/auth/middleware.go` | Bearer auth middleware, RequireRole |
| `internal/api/server.go` | chi router, CORS, all route groups wired |
| `internal/api/handlers_projects.go` | CRUD for Projects, user lookup |
| `internal/api/handlers_misc.go` | Health, readiness, setup/init, stubs for all other routes |
| `internal/api/handlers_runs.go` | Start run (async), get run, get run events |
| `internal/adapter/adapter.go` | Provider interface, Message/ToolCall types, Registry |
| `internal/adapter/anthropic/anthropic.go` | Full Claude adapter (complete + stream) |
| `internal/agent/runtime.go` | Plan/act/observe loop, budget checks, event recording, spend metering |
| `cmd/symbiont/main.go` | Binary entry point: `serve` + `migrate` subcommands |

### What works now
- `make docker-up` → Postgres + pgvector running
- `make run` → API server starts, migrations auto-apply
- `POST /v1/setup/init` → bootstrap org + owner + API key
- `POST /v1/projects` + `GET /v1/projects` → project CRUD
- `POST /v1/projects/{id}/runs` → fires an agent run against Claude (async)
- `GET /v1/projects/{id}/runs/{runID}` → run status
- `GET /v1/projects/{id}/runs/{runID}/events` → full event log
- All other routes return 501 with a clear "Part N" label

---

## ✅ PART 2 (M1 — Orchestration Engine) — COMPLETE

### Files created / updated
| File | Purpose |
|------|---------|
| `migrations/002_job_queue.up.sql` | `jobs` table, `job_status` enum, SKIP LOCKED index |
| `migrations/002_job_queue.down.sql` | Rollback |
| `internal/orchestration/node.go` | NodeKind constants, WorkflowDefinition, edge/node types |
| `internal/orchestration/engine.go` | BFS DAG executor, fan-in, crash-resume via event replay |
| `internal/orchestration/triggers.go` | Cron + webhook scheduler, `parseCronInterval` |
| `internal/orchestration/broadcaster.go` | In-process pub/sub for SSE delivery |
| `internal/queue/queue.go` | Durable Postgres job queue, SKIP LOCKED, exponential backoff, reaper |
| `internal/api/handlers_workflows.go` | Workflow CRUD, trigger creation, webhook ingest, start run |
| `internal/api/handlers_sse.go` | SSE streaming (`GET /runs/{id}/stream`), heartbeat, historical replay |
| `internal/api/server.go` | Updated: added engine/scheduler/broadcaster; WriteTimeout=0 for SSE |
| `internal/api/handlers_misc.go` | Stubs delegate to real implementations |
| `cmd/symbiont/main.go` | Wires queue, engine, scheduler, broadcaster |

### What works now (all of Part 1 +)
- `POST /v1/projects/{id}/workflows` → create workflow
- `GET/PUT /v1/projects/{id}/workflows/{wid}` → CRUD
- `POST /v1/projects/{id}/workflows/{wid}/run` → fire workflow via scheduler
- `POST /v1/projects/{id}/workflows/{wid}/triggers` → create cron/webhook trigger
- `POST /webhooks/{triggerPath}` → ingest webhook (HMAC-verified)
- `GET /v1/projects/{id}/runs/{runID}/stream` → SSE live run log
- Durable BFS engine with crash-resume, fan-in, conditional branching, approval gates

---

## ✅ PART 3 (M2 — Shared Memory Fabric) — COMPLETE

### Files created / updated
| File | Purpose |
|------|---------|
| `internal/memory/store.go` | Write, Query (vector+keyword), Quarantine, Promote, GetByID |
| `internal/memory/graph.go` | UpsertEntity, AddRelation, GraphSearch (BFS to depth N), FindEntity |
| `internal/memory/librarian.go` | Background curation: decay, dedup, expiry (runs every 1h) |
| `internal/api/handlers_memory.go` | POST /memory, GET /memory, GET /memory/graph |
| `internal/api/server.go` | Added `memStore *memory.Store`; updated `New()` signature |
| `internal/api/handlers_misc.go` | Memory stubs delegate to real implementations |
| `cmd/symbiont/main.go` | Creates `memory.Store` + `memory.Librarian`, starts librarian goroutine |

### What works now (all of Part 2 +)
- `POST /v1/projects/{id}/memory` → write a memory record (type/scope/trust/confidence)
- `GET /v1/projects/{id}/memory?q=...&type=...&scope=...&min_trust=...` → semantic+keyword query
- `GET /v1/projects/{id}/memory/graph?entity_type=...&depth=2` → knowledge graph traversal
- Librarian runs every hour: expires stale records, decays confidence, deduplicates
- All five memory types: working / episodic / semantic / procedural / graph
- Five scopes: private / agent / workflow / project / global
- Trust tiers: verified / observed / untrusted (untrusted auto-quarantined on write)

---

## ✅ PART 4 (M3 — Agent Factory) — COMPLETE

### Files created / updated
| File | Purpose |
|------|---------|
| `internal/factory/architect.go` | `Architect.Synthesize(ctx, projectID, prompt)` → LLM meta-prompt → draft AgentSpec JSON; `SanitizeSlug()` exported helper |
| `internal/factory/templates.go` | 5 built-in templates: Researcher, Engineer, Designer, Marketer, Support; `Templates()`, `TemplateByID()`, `InstantiateTemplate()` |
| `internal/api/handlers_agents.go` | Full AgentSpec CRUD + synthesize + instantiate + template endpoints; versioning via new-row-with-version+1; lenient JSON decoder for draft round-trip |
| `internal/api/server.go` | Added `architect *factory.Architect`; wired in `New()`; updated agent routes with templates sub-routes |
| `internal/api/handlers_misc.go` | Removed agent stubs — now delegated to handlers_agents.go |

### What works now (all of Part 3 +)
- `GET  /v1/projects/{id}/agents` → list latest version of every active AgentSpec
- `POST /v1/projects/{id}/agents` → create AgentSpec (lenient body — accepts full draft)
- `POST /v1/projects/{id}/agents/synthesize` → Architect LLM synthesis → draft spec (or save directly with `save_direct: true`)
- `GET  /v1/projects/{id}/agents/{id}` → fetch spec by UUID
- `PUT  /v1/projects/{id}/agents/{id}` → version bump: deactivates old row, inserts version+1; `_prev_id` + `_prev_version` embedded in guardrails for audit
- `POST /v1/projects/{id}/agents/{id}/instantiate` → create AgentInstance
- `GET  /v1/projects/{id}/agents/templates` → list 5 built-in templates
- `POST /v1/projects/{id}/agents/templates/{templateID}/instantiate` → create bound spec from template

### Key design decisions
- **Draft-first synthesis**: `POST /synthesize` returns an unrecorded draft by default; operator reviews and POSTs to `/agents` to save. `save_direct: true` skips review.
- **Versioning**: UPDATE creates a new row (version N+1), not an in-place mutation. Old rows persist with `is_active=false`. GET by UUID always returns the exact version requested.
- **PostgreSQL enum arrays**: `memory_scope[]` columns round-trip via `pq.StringArray` with `::memory_scope[]` cast in INSERT SQL.
- **Model selection guidance**: Architect meta-prompt instructs the LLM to pick Opus only for complex reasoning; Sonnet for most tasks; Haiku for high-volume simple tasks.

---

## ✅ PART 5 (M4 — React Frontend) — COMPLETE

### Files created
| File | Purpose |
|------|---------|
| `web/package.json` | React 18 + TypeScript + Vite + @xyflow/react ^12.3.0 + @tanstack/react-query + zustand + react-router-dom |
| `web/vite.config.ts` | Vite config with `@` path alias; dev proxy: `/v1`, `/webhooks`, `/healthz` → `localhost:8080` |
| `web/tailwind.config.ts` | Custom dark theme: canvas (`#0d0f14`), accent (`#6366f1`), success/warning/danger/muted tokens |
| `web/postcss.config.js` | Tailwind + autoprefixer |
| `web/tsconfig.json` + `web/tsconfig.node.json` | TypeScript strict mode, `@/*` alias |
| `web/index.html` | HTML entry, dark background, Vite module script |
| `web/src/main.tsx` | React 18 `createRoot`, `QueryClientProvider`, `BrowserRouter` |
| `web/src/index.css` | Tailwind directives + custom utility classes |
| `web/src/api/client.ts` | Typed fetch wrapper: `ApiError`, all domain types, `api.*` surface, `getStoredApiKey` / `setStoredApiKey` / `clearStoredApiKey` (sessionStorage) |
| `web/src/hooks/useAPIKey.ts` | Auth state hook: `isAuthenticated`, `setApiKey`, `clearApiKey` |
| `web/src/hooks/useRunStream.ts` | SSE consumer via manual `fetch` + `ReadableStream` (supports Bearer auth); returns `events[]`, `status`, `error` |
| `web/src/App.tsx` | Full app shell: `LoginPage`, `ProjectSelector`, `AppShell`, react-router routes |
| `web/src/pages/MissionControl.tsx` | Dashboard: KPI cards, SpendGauge, active runs list, runs table (5s poll), relative timestamps |
| `web/src/pages/WorkflowBuilder.tsx` | xyflow canvas: node palette, `defToFlow`/`flowToDef` helpers, Inspector panel, WorkflowList sidebar, NewWorkflowModal, Save + Run mutations |
| `web/src/pages/TraceViewer.tsx` | Per-run event waterfall: RunList sidebar, run header (status/timing/spend), EventRow with expandable JSON payload, live stream indicator |
| `web/src/pages/MemoryExplorer.tsx` | Semantic search + filters (type/scope/trust), MemoryCard results grid, WriteMemoryModal, force-directed KnowledgeGraph on HTML5 canvas (draggable nodes) |
| `web/src/nodes/AgentNode.tsx` | xyflow node: indigo border, Bot icon, status ring animation (running = pulse) |
| `web/src/nodes/ConditionNode.tsx` | Branch node: warning border, GitBranch icon, dual source handles (true/false) |
| `web/src/nodes/ApprovalNode.tsx` | Gate node: muted border, ShieldCheck icon, pending/approved/rejected colour badge |
| `web/src/nodes/TriggerNode.tsx` | Entry node: success border, Clock/Zap icon, no target handle |

### What works now (all of Part 4 +)
- `make web-install` → installs npm deps in `web/`
- `make web-dev` → Vite dev server at `http://localhost:5173` with HMR; API calls proxied to `:8080`
- `make web-build` → production bundle → `web/dist/`
- `make dev-full` → docker + Go server + Vite in parallel (one terminal)
- Login page validates API key via `GET /v1/projects`; authenticated session persists in sessionStorage
- Project selector populates from `GET /v1/projects`; selected project scoped to all pages
- Mission Control shows live run status with 5s polling and spend tracking
- Workflow Builder: drag-and-drop canvas, node palette, edge connection, inspector panel, save/run
- Trace Viewer: run list sidebar + event waterfall with live SSE tail for active runs; REST fallback for completed runs
- Memory Explorer: semantic search with debounce, filter chips, memory cards with confidence/trust badges, knowledge graph with force-directed layout + drag

### How to start the frontend
```bash
# Terminal 1: backend
make docker-up && make run

# Terminal 2: frontend
make web-install
make web-dev
# → open http://localhost:5173
# → enter your API key from POST /v1/setup/init
```

---

## ✅ PART 6 (M5 — Governance & Security) — COMPLETE

### Files created / updated
| File | Purpose |
|------|---------|
| `internal/governance/budget.go` | `BudgetManager`: multi-level spend enforcement (agent → workflow → project), `RecordSpend` (atomic ledger + denormalised counters), `UpsertBudget`, `RunBudgetResets` background worker |
| `internal/governance/audit.go` | `AuditWriter`: hash-chained append-only audit log; SHA-256 chain per org; `VerifyChain` for tamper detection; `ListAuditEvents` paginated query |
| `internal/governance/approval.go` | `ApprovalResolver`: create/resolve approval requests, pause/resume runs, `StartExpiryWorker` goroutine that expires timed-out requests every minute |
| `internal/governance/redactor.go` | `Redactor`: 12 regex patterns covering emails, phone numbers, SSNs, credit cards, Anthropic/OpenAI/AWS/GitHub keys, JWTs, PEM private keys, generic secrets; `LooksLikeInjection` prompt-injection heuristics |
| `internal/governance/anomaly.go` | `AnomalyDetector`: rate limiting (30 writes/min per agent), prompt-injection detection, scope escalation, confidence manipulation, oversized payload; returns `AnomalyReport` with quarantine recommendation |
| `internal/api/handlers_governance.go` | HTTP handlers: `GET/POST/PUT /budgets`, `GET /approvals`, `POST /approvals/{id}/resolve`, `GET /audit`, `GET /audit/verify`; `mustProject` helper; `writeAudit` convenience wrapper |
| `internal/api/server.go` | Added `budgetMgr`, `auditWriter`, `approvalResolver`, `redactor`, `anomalyDetector` fields; governance package import; new routes wired |
| `internal/auth/middleware.go` | Added `Actor` struct + `ActorFromContext` helper for audit entry construction |
| `cmd/symbiont/main.go` | Wired approval expiry worker + budget reset worker goroutines |
| `web/src/api/client.ts` | Fixed `Run.finished_at` → `Run.completed_at` (aligns with Go model) |
| `web/src/pages/MissionControl.tsx` | Updated to use `run.completed_at` |
| `web/src/pages/TraceViewer.tsx` | Updated to use `run.completed_at` |

### What works now (all of Part 5 +)
- `GET  /v1/projects/{id}/budgets` → list budgets
- `POST /v1/projects/{id}/budgets` → create budget (agent/workflow/project scope)
- `PUT  /v1/projects/{id}/budgets/{budgetID}` → update limit/warning/reset
- `GET  /v1/projects/{id}/approvals` → list pending (or all with `?all=true`)
- `POST /v1/projects/{id}/approvals/{approvalID}/resolve` → approve or reject
- `GET  /v1/projects/{id}/audit` → paginated audit log
- `GET  /v1/projects/{id}/audit/verify` → verify hash chain integrity
- Budget enforcement: `CheckAndReserve` checks agent→workflow→project in order; returns `BudgetResult` with soft warning + hard exceeded flags
- Spend metering: `RecordSpend` atomically updates `spend_ledger`, `runs.spend_cents`, `agent_instances.spend_cents`, `projects.spend_cents` in one transaction
- Audit chain: every write hashes `prevHash || extID || action || unixTimestamp`; chain is per-org and verifiable offline
- PII/secret redaction: call `redactor.Redact(content)` before writing to memory to replace matched patterns with `[REDACTED:LABEL]`
- Anomaly detection: integrated in `memory.Store.Write` path via `AnomalyDetector.Inspect`
- Approval expiry: background worker expires timed-out requests every minute and fails the paused runs
- Budget period resets: background worker resets monthly/weekly budgets when `next_reset_at` elapses

### Key design decisions
- **No new migration**: all governance tables (`budgets`, `spend_ledger`, `approval_requests`, `audit_events`) were already in migration 001.
- **Hash chain per org, not global**: allows multi-org deployments without cross-org lock contention on writes.
- **`UpsertBudget` uses `ON CONFLICT (id)`**: new budgets get a fresh UUID (INSERT path), existing budgets are updated (DO UPDATE path).
- **Soft-warn vs. hard-cap**: `BudgetResult.SoftWarning` lets callers emit a `budget_warning` event without blocking; `HardExceeded` blocks the request entirely.

---

## ✅ PART 7 (M6–M7 — Blueprints + Ecosystem) — COMPLETE

### Files created / updated
| File | Purpose |
|------|---------|
| `internal/adapter/openai/openai.go` | Full OpenAI adapter (complete + stream); raw HTTP, no SDK; gpt-4o / gpt-4o-mini + others |
| `internal/adapter/openrouter/openrouter.go` | OpenRouter adapter (OpenAI-compatible); routes to 200+ models; `HTTP-Referer` + `X-Title` headers |
| `internal/adapter/ollama/ollama.go` | Ollama adapter for local inference; `/api/chat` wire format; zero cost accounting |
| `migrations/003_blueprints.up.sql` | `blueprints`, `blueprint_instances`, `marketplace_plugins` tables; 5 built-in blueprints + 8 marketplace plugins seeded |
| `migrations/003_blueprints.down.sql` | Full rollback |
| `internal/api/handlers_blueprints.go` | Blueprint CRUD, instantiation (creates agent specs + workflows in a transaction), marketplace list + detail |
| `internal/api/handlers_misc.go` | Fixed: removed duplicate governance stubs; implemented `handleListRuns` DB query; added `handleListBlueprints` delegate |
| `internal/api/server.go` | Added blueprint routes (`GET/POST /v1/blueprints`, `GET /v1/blueprints/{id}`, `POST /{projectID}/blueprints/instantiate`); marketplace routes (`GET/GET /{slug} /v1/marketplace`) |
| `cmd/symbiont/main.go` | Registered OpenAI, OpenRouter, Ollama adapters; conditional registration based on env vars |

### What works now (all of Part 6 +)
- `GET  /v1/blueprints` → list system + org blueprints (5 built-in: Support, Onboarding, Security IR, DevOps Release, Market Research)
- `POST /v1/blueprints` → create custom blueprint scoped to org
- `GET  /v1/blueprints/{id}` → fetch blueprint by UUID
- `POST /v1/projects/{id}/blueprints/instantiate` → create all agent specs + workflows from a blueprint in one atomic transaction; records `blueprint_instances` row
- `GET  /v1/marketplace` → list marketplace plugins (filterable by `?category=`)
- `GET  /v1/marketplace/{slug}` → get plugin manifest + metadata
- OpenAI `gpt-4o`, `gpt-4o-mini`, `gpt-4-turbo`, `o1`, `o1-mini` — set `DEFAULT_PROVIDER=openai` + `OPENAI_API_KEY`
- OpenRouter 200+ models (`anthropic/claude-sonnet-4-6`, `openai/gpt-4o`, `google/gemini-1.5-pro`, ...) — set `DEFAULT_PROVIDER=openrouter` + `OPENROUTER_API_KEY`
- Ollama local inference — always registered; set `OLLAMA_BASE_URL` (default `http://localhost:11434`); use `ollama pull llama3.1` to download models
- `GET  /v1/projects/{id}/runs` → now returns real DB results (was returning `[]` stub)

### Key design decisions
- **Adapters use raw `net/http`**: No new SDK dependencies. OpenAI + OpenRouter share the same OpenAI wire format but live in separate packages so their defaults, pricing tables, and headers remain independent.
- **Ollama always registered**: Ollama is free and local — registration never fails even if the server isn't running. Errors surface only on first model call.
- **Blueprint instantiation is transactional**: All agent specs and workflows from a blueprint are created in a single `BEGIN…COMMIT` block. If any INSERT fails, the whole instantiation rolls back cleanly.
- **`pqStringArray` scanner**: PostgreSQL `text[]` arrays are parsed in-package via a lightweight `parsePGArray` function to avoid importing `lib/pq` array helpers into every new handler file.
- **Marketplace is read-only for now**: Plugin install tracking (`install_count`) and project-level plugin activation are reserved for the 1.0 release.

---

## 🔲 PART 8 (1.0 Release) — TODO
- agentskills.io skill compatibility layer
- `GET /v1/projects/{id}/runs` full filtering (status, date range, agent)
- Cancel run implementation (`POST /v1/projects/{id}/runs/{runID}/cancel`)
- MCP server registration + tool gateway
- Full documentation site + OpenAPI spec generation
- Docker multi-stage production image
- 1.0 release checklist + GitHub Actions CI
