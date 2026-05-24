// Package agent implements the Agent Runtime — the plan/act/observe loop
// that executes a single AgentSpec against a set of tools and memory.
package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/symbiont-ai/symbiont/internal/adapter"
	"github.com/symbiont-ai/symbiont/internal/db"
	"github.com/symbiont-ai/symbiont/internal/models"
)

const (
	// MaxSteps is a hard safety limit on the number of plan/act/observe cycles
	// per run to prevent infinite loops.
	MaxSteps = 50
	// DefaultMaxTokens for each model call
	DefaultMaxTokens = 4096
)

// Tool is anything the agent can call. Implementations may wrap MCP servers
// or native tools.
type Tool interface {
	// Definition returns the tool schema shown to the model.
	Definition() adapter.ToolDefinition
	// Execute runs the tool and returns a result string.
	Execute(ctx context.Context, input map[string]any) (string, error)
}

// RunConfig is everything the Runtime needs to execute a single agent run.
type RunConfig struct {
	Run      *models.Run
	Spec     *models.AgentSpec
	Instance *models.AgentInstance
	Tools    []Tool
	// InitialMessages prepopulates the conversation (e.g. with memory context)
	InitialMessages []adapter.Message
	// Provider to use for model calls
	Provider adapter.Provider
}

// Runtime executes agents.
type Runtime struct {
	db       *db.DB
	registry *adapter.Registry
}

// NewRuntime creates a Runtime backed by the given DB and provider registry.
func NewRuntime(database *db.DB, registry *adapter.Registry) *Runtime {
	return &Runtime{db: database, registry: registry}
}

// Execute runs an agent to completion (or until it hits a limit / error).
// It is synchronous — the caller manages goroutines.
func (rt *Runtime) Execute(ctx context.Context, cfg RunConfig) error {
	logger := log.With().
		Str("run_id", cfg.Run.ID.String()).
		Str("agent", cfg.Instance.Name).
		Logger()

	// Mark run as running
	if err := rt.setRunStatus(ctx, cfg.Run.ID, models.RunStatusRunning); err != nil {
		return fmt.Errorf("agent runtime: set running: %w", err)
	}
	cfg.Run.Status = models.RunStatusRunning
	startedAt := time.Now()
	cfg.Run.StartedAt = &startedAt

	logger.Info().Msg("agent run started")
	rt.recordEvent(ctx, cfg, models.EventKindRunStarted, map[string]any{
		"spec_id":   cfg.Spec.ID,
		"spec_name": cfg.Spec.Name,
		"model":     cfg.Spec.Model,
	}, 0, nil)

	err := rt.runLoop(ctx, logger, cfg)

	completedAt := time.Now()
	cfg.Run.CompletedAt = &completedAt

	if err != nil {
		logger.Error().Err(err).Msg("agent run failed")
		errMsg := err.Error()
		cfg.Run.Error = &errMsg
		rt.setRunStatus(ctx, cfg.Run.ID, models.RunStatusFailed)
		rt.recordEvent(ctx, cfg, models.EventKindRunFailed, map[string]any{
			"error": err.Error(),
		}, 0, nil)
		return err
	}

	logger.Info().Msg("agent run succeeded")
	rt.setRunStatus(ctx, cfg.Run.ID, models.RunStatusSucceeded)
	rt.recordEvent(ctx, cfg, models.EventKindRunSucceeded, map[string]any{
		"total_spend_cents": cfg.Run.SpendCents,
	}, 0, nil)
	return nil
}

// runLoop is the inner plan/act/observe cycle.
func (rt *Runtime) runLoop(ctx context.Context, logger zerolog.Logger, cfg RunConfig) error {
	provider := cfg.Provider
	if provider == nil {
		var ok bool
		if cfg.Spec.ProviderConfigID != nil {
			// TODO Part 4: resolve per-agent provider config from DB
		}
		provider, ok = rt.registry.Default()
		if !ok {
			return fmt.Errorf("no default provider configured")
		}
	}

	// Build tool definitions for the model
	var toolDefs []adapter.ToolDefinition
	toolMap := make(map[string]Tool)
	for _, t := range cfg.Tools {
		def := t.Definition()
		toolDefs = append(toolDefs, def)
		toolMap[def.Name] = t
	}

	// Build the initial message list
	messages := make([]adapter.Message, len(cfg.InitialMessages))
	copy(messages, cfg.InitialMessages)

	// Inject task input as the first user message if messages are empty
	if len(messages) == 0 {
		inputJSON, _ := json.Marshal(cfg.Run.Input)
		messages = append(messages, adapter.Message{
			Role:    "user",
			Content: "Your task: " + string(inputJSON),
		})
	}

	seq := 0
	totalSpend := int64(0)

	for step := 0; step < MaxSteps; step++ {
		// ── Budget check ─────────────────────────────────────
		if cfg.Spec.BudgetCents > 0 && totalSpend >= cfg.Spec.BudgetCents {
			rt.recordEvent(ctx, cfg, models.EventKindBudgetExceeded, map[string]any{
				"budget_cents": cfg.Spec.BudgetCents,
				"spend_cents":  totalSpend,
			}, seq, nil)
			return fmt.Errorf("agent budget exceeded: spent %d cents of %d cent limit",
				totalSpend, cfg.Spec.BudgetCents)
		}

		if cfg.Spec.BudgetCents > 0 {
			warningThreshold := cfg.Spec.BudgetCents * 80 / 100
			if totalSpend >= warningThreshold {
				logger.Warn().
					Int64("spend_cents", totalSpend).
					Int64("budget_cents", cfg.Spec.BudgetCents).
					Msg("agent approaching budget limit (80%)")
			}
		}

		seq++
		logger.Debug().Int("step", step+1).Msg("agent step")

		// ── Model call ───────────────────────────────────────
		rt.recordEvent(ctx, cfg, models.EventKindAgentStep, map[string]any{
			"step": step + 1,
			"messages_count": len(messages),
		}, seq, nil)

		req := adapter.CompletionRequest{
			Model:     cfg.Spec.Model,
			System:    cfg.Spec.SystemInstructions,
			Messages:  messages,
			Tools:     toolDefs,
			MaxTokens: DefaultMaxTokens,
		}

		seq++
		resp, err := provider.Complete(ctx, req)
		if err != nil {
			return fmt.Errorf("model call failed at step %d: %w", step+1, err)
		}

		// Record model call event
		rt.recordEvent(ctx, cfg, models.EventKindModelCall, map[string]any{
			"model":         resp.Model,
			"stop_reason":   resp.StopReason,
			"content_len":   len(resp.Content),
			"tool_calls":    len(resp.ToolCalls),
		}, seq, &resp.Usage)

		// Meter spend
		totalSpend += int64(resp.Usage.CostCents)
		cfg.Run.SpendCents = totalSpend
		rt.meterSpend(ctx, cfg, &resp.Usage)

		// Append assistant message
		messages = append(messages, adapter.Message{
			Role:      "assistant",
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})

		// ── Check stop reason ────────────────────────────────
		if resp.StopReason == "end_turn" || resp.StopReason == "stop_sequence" {
			logger.Debug().Str("stop_reason", resp.StopReason).Msg("agent done")
			return nil
		}

		if resp.StopReason == "max_tokens" {
			return fmt.Errorf("model returned max_tokens — response truncated at step %d", step+1)
		}

		// ── Tool calls ───────────────────────────────────────
		if resp.StopReason == "tool_use" || len(resp.ToolCalls) > 0 {
			var toolResults []adapter.ToolResult

			for _, tc := range resp.ToolCalls {
				seq++
				rt.recordEvent(ctx, cfg, models.EventKindToolCall, map[string]any{
					"tool_name":    tc.Name,
					"tool_call_id": tc.ID,
					"input":        tc.Input,
				}, seq, nil)

				tool, exists := toolMap[tc.Name]
				var result string
				var isError bool

				if !exists {
					result = fmt.Sprintf("error: unknown tool %q", tc.Name)
					isError = true
					logger.Warn().Str("tool", tc.Name).Msg("unknown tool requested by model")
				} else {
					var execErr error
					result, execErr = tool.Execute(ctx, tc.Input)
					if execErr != nil {
						result = fmt.Sprintf("error: %s", execErr.Error())
						isError = true
						logger.Warn().Err(execErr).Str("tool", tc.Name).Msg("tool execution error")
					}
				}

				seq++
				rt.recordEvent(ctx, cfg, models.EventKindToolResult, map[string]any{
					"tool_call_id": tc.ID,
					"tool_name":    tc.Name,
					"is_error":     isError,
					"result_len":   len(result),
				}, seq, nil)

				toolResults = append(toolResults, adapter.ToolResult{
					ToolCallID: tc.ID,
					Content:    result,
					IsError:    isError,
				})
			}

			// Append tool results as user turn
			messages = append(messages, adapter.Message{
				Role:        "user",
				ToolResults: toolResults,
			})
		}
	}

	return fmt.Errorf("exceeded maximum steps (%d) — possible loop detected", MaxSteps)
}

// ── DB helpers ────────────────────────────────────────────────

func (rt *Runtime) setRunStatus(ctx context.Context, runID uuid.UUID, status models.RunStatus) error {
	query := `UPDATE runs SET status=$1, updated_at=now() WHERE id=$2`
	if status == models.RunStatusRunning {
		query = `UPDATE runs SET status=$1, started_at=now(), updated_at=now() WHERE id=$2`
	}
	if status == models.RunStatusSucceeded || status == models.RunStatusFailed || status == models.RunStatusCancelled {
		query = `UPDATE runs SET status=$1, completed_at=now(), updated_at=now() WHERE id=$2`
	}
	_, err := rt.db.ExecContext(ctx, query, status, runID)
	return err
}

var seqCounters = make(map[uuid.UUID]int)

func (rt *Runtime) recordEvent(ctx context.Context, cfg RunConfig, kind models.EventKind, payload map[string]any, seq int, usage *adapter.Usage) {
	payloadJSON, _ := json.Marshal(payload)
	var inputTokens, outputTokens, costCents *int
	if usage != nil {
		it := usage.InputTokens
		ot := usage.OutputTokens
		cc := usage.CostCents
		inputTokens = &it
		outputTokens = &ot
		costCents = &cc
	}
	_, err := rt.db.ExecContext(ctx,
		`INSERT INTO events (run_id, project_id, seq, kind, agent_instance_id, payload, input_tokens, output_tokens, cost_cents)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		cfg.Run.ID, cfg.Run.ProjectID, seq, kind, cfg.Instance.ID,
		string(payloadJSON), inputTokens, outputTokens, costCents)
	if err != nil {
		// Non-fatal: log and continue
		log.Error().Err(err).Str("kind", string(kind)).Msg("failed to record event")
	}
}

func (rt *Runtime) meterSpend(ctx context.Context, cfg RunConfig, usage *adapter.Usage) {
	if usage == nil || usage.CostCents == 0 {
		return
	}
	_, err := rt.db.ExecContext(ctx,
		`INSERT INTO spend_ledger (project_id, run_id, agent_instance_id, amount_cents, model, input_tokens, output_tokens)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		cfg.Run.ProjectID, cfg.Run.ID, cfg.Instance.ID,
		usage.CostCents, "unknown", usage.InputTokens, usage.OutputTokens)
	if err != nil {
		log.Error().Err(err).Msg("failed to meter spend")
	}
	// Update run spend total
	rt.db.ExecContext(ctx,
		`UPDATE runs SET spend_cents = spend_cents + $1 WHERE id=$2`,
		usage.CostCents, cfg.Run.ID)
}

// GetRun fetches a run by ID.
func (rt *Runtime) GetRun(ctx context.Context, runID uuid.UUID) (*models.Run, error) {
	var run models.Run
	err := rt.db.QueryRowContext(ctx,
		`SELECT id, project_id, workflow_id, agent_instance_id, trigger_id, trigger_kind,
		        input, status, spend_cents, error, created_at, started_at, completed_at, initiated_by
		 FROM runs WHERE id=$1`, runID).Scan(
		&run.ID, &run.ProjectID, &run.WorkflowID, &run.AgentInstanceID,
		&run.TriggerID, &run.TriggerKind,
		&run.Input, &run.Status, &run.SpendCents, &run.Error,
		&run.CreatedAt, &run.StartedAt, &run.CompletedAt, &run.InitiatedBy)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &run, err
}

// GetRunEvents fetches all events for a run ordered by sequence.
func (rt *Runtime) GetRunEvents(ctx context.Context, runID uuid.UUID) ([]models.Event, error) {
	rows, err := rt.db.QueryContext(ctx,
		`SELECT id, ext_id, run_id, project_id, seq, kind, agent_instance_id,
		        payload, input_tokens, output_tokens, cost_cents, created_at
		 FROM events WHERE run_id=$1 ORDER BY seq ASC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []models.Event
	for rows.Next() {
		var e models.Event
		if err := rows.Scan(&e.ID, &e.ExtID, &e.RunID, &e.ProjectID, &e.Seq, &e.Kind,
			&e.AgentInstanceID, &e.Payload,
			&e.InputTokens, &e.OutputTokens, &e.CostCents, &e.CreatedAt); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, nil
}

// CreateRun inserts a new run record.
func (rt *Runtime) CreateRun(ctx context.Context, run *models.Run) error {
	_, err := rt.db.ExecContext(ctx,
		`INSERT INTO runs (id, project_id, agent_instance_id, input, status, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6)`,
		run.ID, run.ProjectID, run.AgentInstanceID, run.Input, run.Status, run.CreatedAt)
	return err
}
