# TheCompany — AI Agent Orchestration Platform

TheCompany is an open platform for building, deploying, and managing autonomous AI agents and multi-agent workflows. It gives teams a single system to design intelligent pipelines visually, connect any major AI provider, enforce governance and budgets, and observe every execution in real time — without stitching together a dozen separate tools.

---

## What Is It For?

| Use Case | How TheCompany Helps |
|---|---|
| **Autonomous research pipelines** | Chain Researcher → Analyst → Writer agents in a DAG workflow that runs on a schedule or webhook |
| **Customer support automation** | Deploy the Support blueprint to handle, escalate, and log tickets with human approval gates |
| **Internal knowledge management** | Store and query agent-generated knowledge across five memory types with semantic vector search |
| **Multi-model experimentation** | Swap between Claude, GPT-4o, Llama (Ollama), or 200+ OpenRouter models per agent, per run |
| **Regulated / enterprise workflows** | Enforce per-agent spend caps, hash-chained audit logs, PII redaction, and anomaly detection |
| **Developer tooling & DevOps** | Use the DevOps Release blueprint for automated deploy pipelines with approval gates |

---

## Features

### Workflow Orchestration
- Visual drag-and-drop **Workflow Builder** (React + xyflow canvas)
- DAG-based execution engine with fan-in, fan-out, and conditional branching
- Node types: Trigger, Agent, Tool, Memory, Approval Gate, Condition, Sub-workflow, Output
- Crash-resume via event sourcing — interrupted runs replay from their last checkpoint
- Cron schedules and webhook triggers (HMAC-verified)

### Agent Factory
- Create, version, and template AI agent specifications
- Built-in templates: Researcher, Engineer, Designer, Marketer, Support
- LLM-powered agent synthesis — describe what you need in plain language
- Immutable versioning: every update creates a new version, old versions stay for audit

### Multi-Provider AI
- **Anthropic Claude** (Opus, Sonnet, Haiku)
- **OpenAI** (gpt-4o, gpt-4o-mini, gpt-4-turbo, o1, o1-mini)
- **OpenRouter** — 200+ models through a single API key
- **Ollama** — local inference, no API key required
- Unified adapter interface — switch providers per agent without changing workflow logic

### Shared Memory Fabric
- Five memory types: working, episodic, semantic, procedural, graph
- Five scopes: private, agent, workflow, project, global
- Vector + keyword semantic search powered by **pgvector**
- Knowledge graph with entity relationships and BFS traversal
- Automatic curation: confidence decay, deduplication, expiry (hourly Librarian goroutine)

### Governance & Security
- **Budget enforcement** — soft warnings and hard caps per agent, workflow, or project
- **Spend metering** — every token counted, atomically recorded, shown in Mission Control
- **Audit log** — hash-chained (SHA-256) per-org append-only log, verifiable offline
- **Approval gates** — pause a run for human sign-off with configurable expiry
- **PII / secret redaction** — strips emails, SSNs, credit cards, and API keys from outputs
- **Anomaly detection** — rate limiting, prompt injection signals, scope escalation alerts

### Blueprints & Marketplace
Ready-made solution templates that spin up all required agents and workflows atomically:
- Customer Support
- Employee Onboarding
- Security Incident Response
- DevOps Release Pipeline
- Market Research

### Real-Time Monitoring
- **Mission Control** — live KPIs: active runs, spend, success rate, queue depth
- **Trace Viewer** — per-run event waterfall with live SSE streaming
- **Memory Explorer** — semantic search + interactive force-directed knowledge graph
- Run history with timing, spend, and status for every execution

---

## Tech Stack

**Backend:** Go 1.22 · chi router · PostgreSQL 16 · pgvector · golang-migrate · zerolog · OpenTelemetry

**Frontend:** React 18 · TypeScript · Vite · xyflow · TanStack Query · Zustand · Tailwind CSS

**Database:** PostgreSQL 16 with pgvector extension (20+ tables, event-sourced run log, SKIP LOCKED job queue)

**AI Providers:** Anthropic SDK · OpenAI · OpenRouter · Ollama

**Infrastructure:** Docker Compose · Makefile

---

## Quick Start

### Prerequisites
- Go 1.22+
- Node.js 20+
- Docker & Docker Compose

### 1. Clone and configure

```bash
git clone https://github.com/alonshalev/TheCompany.git
cd TheCompany
cp .env.example .env
# Edit .env — add at minimum your ANTHROPIC_API_KEY
```

### 2. Start the database

```bash
make docker-up
```

### 3. Run the backend

```bash
make run
# Server starts on http://localhost:8080
# Migrations apply automatically on first run
```

### 4. Run the frontend

```bash
make web-install
make web-dev
# UI available at http://localhost:5173
```

Or run everything in one command:

```bash
make dev-full
```

### 5. Initialize your organization

```bash
curl -X POST http://localhost:8080/v1/setup/init \
  -H "Content-Type: application/json" \
  -d '{"org_name": "My Org", "owner_email": "you@example.com"}'
```

This returns your API key. Open `http://localhost:5173`, enter the key, and you're in.

---

## Project Structure

```
thecompany/
├── cmd/symbiont/        # Entry point (server + migrate subcommands)
├── internal/
│   ├── adapter/         # AI provider adapters (Anthropic, OpenAI, OpenRouter, Ollama)
│   ├── agent/           # Agent runtime
│   ├── api/             # HTTP handlers (agents, workflows, runs, memory, governance, …)
│   ├── auth/            # API key authentication + middleware
│   ├── config/          # Environment-based configuration
│   ├── db/              # Database connection pool
│   ├── factory/         # Agent spec creation and LLM synthesis
│   ├── governance/      # Budgets, audit chain, approval, redaction, anomaly detection
│   ├── memory/          # Memory store, knowledge graph, librarian curation
│   ├── models/          # Shared data models and JSON types
│   ├── orchestration/   # DAG engine, broadcaster (SSE pub/sub), node runners, triggers
│   └── queue/           # Durable Postgres job queue
├── migrations/          # SQL up/down migration files
├── blueprints/          # Pre-built solution blueprint definitions
├── deployments/docker/  # Docker Compose for Postgres + pgAdmin
├── web/                 # React frontend (Vite, xyflow, Tailwind)
└── Makefile
```

---

## Makefile Reference

| Command | Description |
|---|---|
| `make run` | Build and start the API server |
| `make dev` | docker-up then run (development shortcut) |
| `make dev-full` | docker + Go server + Vite frontend in parallel |
| `make docker-up` | Start Postgres + pgvector via Docker Compose |
| `make docker-down` | Stop and remove containers |
| `make migrate-up` | Apply all pending DB migrations |
| `make migrate-down` | Roll back the last migration |
| `make web-install` | Install frontend npm dependencies |
| `make web-dev` | Start Vite dev server (port 5173) |
| `make web-build` | Build frontend for production |
| `make test` | Run Go tests |
| `make lint` | Run golangci-lint |
| `make fmt` | Format Go code |

---

## Environment Variables

Copy `.env.example` to `.env` and fill in the values you need:

| Variable | Required | Description |
|---|---|---|
| `DATABASE_URL` | Yes | PostgreSQL connection string |
| `ANTHROPIC_API_KEY` | For Claude | Anthropic API key |
| `OPENAI_API_KEY` | For OpenAI | OpenAI API key |
| `OPENROUTER_API_KEY` | For OpenRouter | OpenRouter API key |
| `OLLAMA_BASE_URL` | For Ollama | Local Ollama server URL |
| `PORT` | No | HTTP server port (default: 8080) |
| `LOG_LEVEL` | No | Log level: debug, info, warn, error |

---

## Roadmap

- [ ] Run cancellation
- [ ] Advanced run filtering and search
- [ ] MCP (Model Context Protocol) tool registration
- [ ] Docker multi-stage production build
- [ ] CI/CD pipeline
- [ ] agentskills.io marketplace compatibility
- [ ] Full API documentation (OpenAPI spec)
- [ ] Redis/NATS for distributed SSE broadcasting

---

## License

Private — All rights reserved. © TheCompany.
