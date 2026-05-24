// Package api wires up the HTTP server, router, and all handlers.
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/rs/zerolog/log"
	"github.com/symbiont-ai/symbiont/internal/adapter"
	"github.com/symbiont-ai/symbiont/internal/agent"
	"github.com/symbiont-ai/symbiont/internal/auth"
	"github.com/symbiont-ai/symbiont/internal/config"
	"github.com/symbiont-ai/symbiont/internal/db"
	"github.com/symbiont-ai/symbiont/internal/factory"
	"github.com/symbiont-ai/symbiont/internal/governance"
	"github.com/symbiont-ai/symbiont/internal/memory"
	"github.com/symbiont-ai/symbiont/internal/orchestration"
)

// Server is the HTTP API server.
type Server struct {
	cfg              *config.Config
	db               *db.DB
	router           *chi.Mux
	http             *http.Server
	runtime          *agent.Runtime
	providerRegistry *adapter.Registry
	engine           *orchestration.Engine
	scheduler        *orchestration.Scheduler
	broadcaster      *orchestration.Broadcaster
	memStore         *memory.Store
	architect        *factory.Architect
	// ── Governance (Part 6) ──────────────────────────────────
	budgetMgr        *governance.BudgetManager
	auditWriter      *governance.AuditWriter
	approvalResolver *governance.ApprovalResolver
	redactor         *governance.Redactor
	anomalyDetector  *governance.AnomalyDetector
}

// New creates a configured Server and wires all routes.
func New(
	cfg *config.Config,
	database *db.DB,
	registry *adapter.Registry,
	engine *orchestration.Engine,
	scheduler *orchestration.Scheduler,
	broadcaster *orchestration.Broadcaster,
	memStore *memory.Store,
) *Server {
	runtime := agent.NewRuntime(database, registry)
	redactor := governance.NewRedactor()
	s := &Server{
		cfg:              cfg,
		db:               database,
		runtime:          runtime,
		providerRegistry: registry,
		engine:           engine,
		scheduler:        scheduler,
		broadcaster:      broadcaster,
		memStore:         memStore,
		architect:        factory.NewArchitect(registry),
		budgetMgr:        governance.NewBudgetManager(database.DB),
		auditWriter:      governance.NewAuditWriter(database.DB),
		approvalResolver: governance.NewApprovalResolver(database.DB),
		redactor:         redactor,
		anomalyDetector:  governance.NewAnomalyDetector(database.DB, redactor),
	}
	s.router = s.buildRouter()
	s.http = &http.Server{
		Addr:         cfg.Addr(),
		Handler:      s.router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 0, // 0 = no timeout (SSE streams are long-lived)
		IdleTimeout:  120 * time.Second,
	}
	return s
}

func (s *Server) buildRouter() *chi.Mux {
	r := chi.NewRouter()

	// ── Global middleware ─────────────────────────────────────
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(requestLogger)
	r.Use(middleware.Recoverer)

	// ── CORS ──────────────────────────────────────────────────
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"http://localhost:3000", "http://localhost:5173"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-Request-ID"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	// ── Health ────────────────────────────────────────────────
	r.Get("/healthz", s.handleHealth)
	r.Get("/readyz", s.handleReady)

	// ── API v1 — public setup (no auth) ──────────────────────
	r.Post("/v1/setup/init", s.handleSetupInit)

	// ── API v1 — authenticated ─────────────────────────────────
	lookupFn := auth.UserLookupFn(s.lookupUserByAPIKey)

	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAPIKey(lookupFn))

		// Projects
		r.Route("/v1/projects", func(r chi.Router) {
			r.Get("/", s.handleListProjects)
			r.Post("/", s.handleCreateProject)
			r.Route("/{projectID}", func(r chi.Router) {
				r.Use(s.projectCtx)
				r.Get("/", s.handleGetProject)
				r.Put("/", s.handleUpdateProject)
				r.Delete("/", s.handleArchiveProject)

				// Agent specs — fully implemented in Part 4
				r.Route("/agents", func(r chi.Router) {
					r.Get("/", s.handleListAgentSpecsImpl)
					r.Post("/", s.handleCreateAgentSpecImpl)
					r.Post("/synthesize", s.handleSynthesizeAgentSpecImpl)
					// Templates (read-only; instantiate creates a project-scoped copy)
					r.Get("/templates", s.handleListAgentTemplates)
					r.Post("/templates/{templateID}/instantiate", s.handleInstantiateTemplate)
					r.Route("/{agentSpecID}", func(r chi.Router) {
						r.Get("/", s.handleGetAgentSpecImpl)
						r.Put("/", s.handleUpdateAgentSpecImpl)
						r.Post("/instantiate", s.handleInstantiateAgentImpl)
					})
				})

				// Workflows — fully implemented in Part 2
				r.Route("/workflows", func(r chi.Router) {
					r.Get("/", s.handleListWorkflowsImpl)
					r.Post("/", s.handleCreateWorkflowImpl)
					r.Route("/{workflowID}", func(r chi.Router) {
						r.Get("/", s.handleGetWorkflowImpl)
						r.Put("/", s.handleUpdateWorkflowImpl)
						r.Post("/run", s.handleStartWorkflowRun)
						// Triggers
						r.Post("/triggers", s.handleCreateTrigger)
					})
				})

				// Runs
				r.Route("/runs", func(r chi.Router) {
					r.Get("/", s.handleListRuns)
					r.Post("/", s.handleStartRun)
					r.Route("/{runID}", func(r chi.Router) {
						r.Get("/", s.handleGetRun)
						r.Get("/events", s.handleGetRunEvents)
						r.Post("/cancel", s.handleCancelRun)
						r.Get("/stream", s.handleStreamRunImpl) // SSE — fully implemented
					})
				})

				// Memory (Part 3)
				r.Route("/memory", func(r chi.Router) {
					r.Get("/", s.handleQueryMemory)
					r.Post("/", s.handleWriteMemory)
					r.Get("/graph", s.handleGetKnowledgeGraph)
				})

				// Budgets (Part 6)
				r.Route("/budgets", func(r chi.Router) {
					r.Get("/", s.handleGetBudgets)
					r.Post("/", s.handleCreateBudget)
					r.Put("/{budgetID}", s.handleUpdateBudget)
				})

				// Approvals (Part 6)
				r.Route("/approvals", func(r chi.Router) {
					r.Get("/", s.handleListApprovals)
					r.Post("/{approvalID}/resolve", s.handleResolveApproval)
				})

				// Audit log (Part 6)
				r.Route("/audit", func(r chi.Router) {
					r.Get("/", s.handleListAuditEvents)
					r.Get("/verify", s.handleVerifyAuditChain)
				})

				// Blueprint instantiation (Part 7)
				r.Post("/blueprints/instantiate", s.handleInstantiateBlueprint)
			})
		})

		// Tools / MCP servers (org-scoped)
		r.Route("/v1/tools", func(r chi.Router) {
			r.Get("/", s.handleListMCPServers)
			r.Post("/", s.handleRegisterMCPServer)
		})

		// Blueprints (org-scoped, Part 7)
		r.Route("/v1/blueprints", func(r chi.Router) {
			r.Get("/", s.handleListBlueprintsImpl)
			r.Post("/", s.handleCreateBlueprint)
			r.Get("/{blueprintID}", s.handleGetBlueprint)
		})

		// Marketplace (Part 7)
		r.Route("/v1/marketplace", func(r chi.Router) {
			r.Get("/", s.handleListMarketplacePlugins)
			r.Get("/{pluginSlug}", s.handleGetMarketplacePlugin)
		})
	})

	// Blueprint instantiation is project-scoped — wired inside /{projectID}
	// (handled by the r.Route("/{projectID}"...) block above via s.handleInstantiateBlueprint)

	// ── Webhook trigger endpoint (no auth — public, HMAC-verified) ──
	r.Post("/webhooks/{triggerPath}", s.handleWebhookTriggerImpl)

	return r
}

// Start begins serving HTTP requests.
func (s *Server) Start() error {
	log.Info().Str("addr", s.cfg.Addr()).Str("env", s.cfg.Server.Env).Msg("symbiont API server starting")
	if err := s.http.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}

// ── Utility ───────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		log.Error().Err(err).Msg("writeJSON encode error")
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decodeJSON(r *http.Request