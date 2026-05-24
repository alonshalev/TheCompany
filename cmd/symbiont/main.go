// Symbiont — main binary entry point.
// Subcommands: serve, migrate up, migrate down
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/symbiont-ai/symbiont/internal/adapter"
	anthropicadapter "github.com/symbiont-ai/symbiont/internal/adapter/anthropic"
	ollamaadapter "github.com/symbiont-ai/symbiont/internal/adapter/ollama"
	openaiadapter "github.com/symbiont-ai/symbiont/internal/adapter/openai"
	openrouteradapter "github.com/symbiont-ai/symbiont/internal/adapter/openrouter"
	"github.com/symbiont-ai/symbiont/internal/agent"
	"github.com/symbiont-ai/symbiont/internal/api"
	"github.com/symbiont-ai/symbiont/internal/config"
	"github.com/symbiont-ai/symbiont/internal/db"
	"github.com/symbiont-ai/symbiont/internal/governance"
	"github.com/symbiont-ai/symbiont/internal/memory"
	"github.com/symbiont-ai/symbiont/internal/orchestration"
	"github.com/symbiont-ai/symbiont/internal/queue"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: symbiont <serve|migrate [up|down]>")
		os.Exit(1)
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	setupLogger(cfg)

	switch os.Args[1] {
	case "serve":
		runServer(cfg)
	case "migrate":
		direction := "up"
		if len(os.Args) >= 3 {
			direction = os.Args[2]
		}
		runMigrate(cfg, direction)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}

func runServer(cfg *config.Config) {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// ── Database ──────────────────────────────────────────────
	database, err := db.Connect(cfg.Database)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to database")
	}
	defer database.Close()

	if err := database.Migrate(); err != nil {
		log.Fatal().Err(err).Msg("database migration failed")
	}

	// ── Provider registry ─────────────────────────────────────
	registry := adapter.NewRegistry()

	// ── Anthropic ─────────────────────────────────────────────
	if cfg.Providers.AnthropicAPIKey != "" {
		anthro, err := anthropicadapter.New(cfg.Providers.AnthropicAPIKey)
		if err != nil {
			log.Warn().Err(err).Msg("failed to initialise Anthropic adapter")
		} else {
			registry.Register(anthro)
			log.Info().Msg("Anthropic adapter registered")
		}
	} else {
		log.Warn().Msg("ANTHROPIC_API_KEY not set — Claude adapter not registered")
	}

	// ── OpenAI ────────────────────────────────────────────────
	if cfg.Providers.OpenAIAPIKey != "" {
		oai, err := openaiadapter.New(cfg.Providers.OpenAIAPIKey, "")
		if err != nil {
			log.Warn().Err(err).Msg("failed to initialise OpenAI adapter")
		} else {
			registry.Register(oai)
			log.Info().Msg("OpenAI adapter registered")
		}
	}

	// ── OpenRouter ────────────────────────────────────────────
	if cfg.Providers.OpenRouterAPIKey != "" {
		ortr, err := openrouteradapter.New(cfg.Providers.OpenRouterAPIKey, "Symbiont")
		if err != nil {
			log.Warn().Err(err).Msg("failed to initialise OpenRouter adapter")
		} else {
			registry.Register(ortr)
			log.Info().Msg("OpenRouter adapter registered")
		}
	}

	// ── Ollama (always registered; fails gracefully if server not running) ──
	{
		ollama := ollamaadapter.New(cfg.Providers.OllamaBaseURL)
		registry.Register(ollama)
		log.Info().Str("base_url", cfg.Providers.OllamaBaseURL).Msg("Ollama adapter registered (local inference)")
	}

	if p, ok := registry.Get(cfg.Providers.DefaultProvider); ok {
		registry.SetDefault(p.Name())
		log.Info().Str("provider", p.Name()).Msg("default provider set")
	}

	// ── Orchestration layer ───────────────────────────────────
	broadcaster := orchestration.NewBroadcaster()

	jobQueue := queue.New(database,
		queue.WithConcurrency(4),
		queue.WithPollInterval(2*time.Second),
	)

	agentRuntime := agent.NewRuntime(database, registry)

	engine := orchestration.NewEngine(database, jobQueue, agentRuntime, registry, broadcaster)
	scheduler := orchestration.NewScheduler(database, engine)

	// ── Shared Memory Fabric ──────────────────────────────────
	memStore := memory.New(database)
	librarian := memory.NewLibrarian(memStore)

	// ── Governance layer ──────────────────────────────────────
	approvalResolver := governance.NewApprovalResolver(database.DB)
	budgetMgr := governance.NewBudgetManager(database.DB)

	// ── HTTP server ───────────────────────────────────────────
	server := api.New(cfg, database, registry, engine, scheduler, broadcaster, memStore)

	// ── Start background services ─────────────────────────────
	go func() {
		log.Info().Msg("starting job queue workers")
		jobQueue.Start(ctx)
	}()

	go func() {
		log.Info().Msg("starting trigger scheduler")
		scheduler.Start(ctx)
	}()

	go func() {
		log.Info().Msg("starting memory librarian")
		librarian.Start(ctx)
	}()

	go func() {
		log.Info().Msg("starting approval expiry worker")
		approvalResolver.StartExpiryWorker(ctx)
	}()

	go func() {
		log.Info().Msg("starting budget reset worker")
		budgetMgr.RunBudgetResets(ctx)
	}()

	go func() {
		log.Info().Str("addr", cfg.Addr()).Msg("symbiont running — press Ctrl+C to stop")
		if err := server.Start(); err != nil {
			log.Error().Err(err).Msg("server stopped")
		}
	}()

	// Wait for shutdown signal
	<-ctx.Done()

	log.Info().Msg("shutting down gracefully...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("shutdown error")
	}
	log.Info().Msg("goodbye")
}

func runMigrate(cfg *config.Config, direction string) {
	database, err := db.Connect(cfg.Database)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to database")
	}
	defer database.Close()

	switch direction {
	case "up":
		if err := database.Migrate(); err != nil {
			log.Fatal().Err(err).Msg("migration up failed")
		}
		log.Info().Msg("migrations applied")
	case "down":
		if err := database.MigrateDown(); err != nil {
			log.Fatal().Err(err).Msg("migration down failed")
		}
		log.Info().Msg("one migration rolled back")
	default:
		log.Fatal().Str("direction", direction).Msg("unknown direction (use 'up' or 'down')")
	}
}

func setupLogger(cfg *config.Config) {
	if cfg.Log.Format == "pretty" {
		log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})
	}
	level, err := zerolog.ParseLevel(cfg.Log.Level)
	if err != nil {
		level = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(level)
}
