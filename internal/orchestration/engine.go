// Package orchestration — DAG-based workflow executor.
//
// The Engine walks a WorkflowDefinition node-by-node. Each step is
// event-sourced: every node execution is recorded as an Event row before and
// after it runs. If the process crashes and restarts, the Engine replays the
// event log to determine which nodes already completed, then continues from
// the frontier.
//
// Execution model
//   - Start from nodes with no incoming edges (trigger nodes).
//   - After each node completes, enqueue its successors whose all dependencies
//     are satisfied (fan-in support).
//   - Branch nodes evaluate their outgoing edge conditions and only advance
//     along matching edges.
//   - Approval-gate nodes pause the run until the ApprovalRequest is resolved.
//   - Sub-workflow nodes spin up a child Engine execution.
package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/symbiont-ai/symbiont/internal/adapter"
	"github.com/symbiont-ai/symbiont/internal/agent"
	"github.com/symbiont-ai/symbiont/internal/db"
	"github.com/symbiont-ai/symbiont/internal/models"
	"github.com/symbiont-ai/symbiont/internal/queue"
)

// JobKindRunWorkflow is the queue job kind dispatched when a workflow run starts.
const JobKindRunWorkflow = "run_workflow"

// JobKindRunAgentNode is dispatched for each agent node execution.
const JobKindRunAgentNode = "run_agent_node"

// Engine executes workflow runs durably.
type Engine struct {
	db         *db.DB
	queue      *queue.Queue
	agentRT    *agent.Runtime
	providers  *adapter.Registry
	broadcaster *Broadcaster
	mu         sync.Mutex
}

// NewEngine wires up an orchestration engine and registers its job handlers.
func NewEngine(
	database *db.DB,
	q *queue.Queue,
	agentRT *agent.Runtime,
	providers *adapter.Registry,
	broadcaster *Broadcaster,
) *Engine {
	e := &Engine{
		db:          database,
		queue:       q,
		agentRT:     agentRT,
		providers:   providers,
		broadcaster: broadcaster,
	}
	q.Register(JobKindRunWorkflow, e.handleRunWorkflowJob)
	q.Register(JobKindRunAgentNode, e.handleRunAgentNodeJob)
	return e
}

// ── Public API ────────────────────────────────────────────────

// StartWorkflowRun enqueues a workflow run job.
// Returns the new Run ID immediately; actual execution is async.
func (e *Engine) StartWorkflowRun(ctx context.Context, projectID, workflowID uuid.UUID, input models.JSONMap, triggeredBy *uuid.UUID) (*models.Run, error) {
	run := &models.Run{
		ID:          uuid.New(),
		ProjectID:   projectID,
		WorkflowID:  &workflowID,
		Input:       input,
		Status:      models.RunStatusPending,
		CreatedAt:   time.Now(),
		InitiatedBy: triggeredBy,
	}

	_, err := e.db.ExecContext(ctx,
		`INSERT INTO runs (id, project_id, workflow_id, input, status, initiated_by, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		run.ID, run.ProjectID, run.WorkflowID, run.Input, run.Status, run.InitiatedBy, run.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("engine.StartWorkflowRun: insert run: %w", err)
	}

	payload := map[string]string{
		"run_id":      run.ID.String(),
		"workflow_id": workflowID.String(),
		"project_id":  projectID.String(),
	}
	if _, err := e.queue.Enqueue(ctx, JobKindRunWorkflow, payload, queue.WithPriority(50)); err != nil {
		return nil, fmt.Errorf("engine.StartWorkflowRun: enqueue: %w", err)
	}

	return run, nil
}

// ── Job handlers ──────────────────────────────────────────────

type runWorkflowPayload struct {
	RunID      string `json:"run_id"`
	WorkflowID string `json:"workflow_id"`
	ProjectID  string `json:"project_id"`
}

func (e *Engine) handleRunWorkflowJob(ctx context.Context, job *queue.Job) error {
	var p runWorkflowPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return fmt.Errorf("engine: unmarshal run_workflow payload: %w", err)
	}

	runID, _ := uuid.Parse(p.RunID)
	workflowID, _ := uuid.Parse(p.WorkflowID)
	projectID, _ := uuid.Parse(p.ProjectID)

	return e.executeWorkflow(ctx, runID, workflowID, projectID)
}

// executeWorkflow loads the workflow definition and walks the DAG.
func (e *Engine) executeWorkflow(ctx context.Context, runID, workflowID, projectID uuid.UUID) error {
	logger := log.With().
		Str("run_id", runID.String()).
		Str("workflow_id", workflowID.String()).
		Logger()

	// ── Load workflow definition ──────────────────────────
	var defRaw []byte
	err := e.db.QueryRowContext(ctx,
		`SELECT definition FROM workflows WHERE id=$1 AND project_id=$2`,
		workflowID, projectID).Scan(&defRaw)
	if err != nil {
		return fmt.Errorf("engine: load workflow %s: %w", workflowID, err)
	}

	var def WorkflowDefinition
	if err := json.Unmarshal(defRaw, &def); err != nil {
		return fmt.Errorf("engine: parse workflow definition: %w", err)
	}

	// ── Mark run as running ───────────────────────────────
	e.db.ExecContext(ctx,
		`UPDATE runs SET status='running', started_at=now(), updated_at=now() WHERE id=$1`, runID)
	e.emit(ctx, runID, projectID, models.EventKindRunStarted, nil, map[string]any{
		"workflow_id": workflowID,
		"node_count":  len(def.Nodes),
	})
	e.broadcaster.Publish(runID, SSEEvent{Type: "run_started", RunID: runID})
	logger.Info().Int("nodes", len(def.Nodes)).Msg("workflow run started")

	// ── Replay completed nodes from event log ─────────────
	completed := e.replayCompletedNodes(ctx, runID)

	// ── Walk the DAG ──────────────────────────────────────
	adj := def.adjacency()
	starts := def.startNodes()

	// BFS execution queue
	type nodeWork struct {
		node  WorkflowNode
		input models.JSONMap
	}
	workQueue := make([]nodeWork, 0, len(starts))
	for _, n := range starts {
		if completed[n.ID] {
			continue // already done in a prior attempt
		}
		workQueue = append(workQueue, nodeWork{node: n, input: nil})
	}

	// Fan-in tracker: how many incoming edges have been satisfied per node
	inboundSatisfied := make(map[string]int)

	// Precompute in-degree
	inDegree := make(map[string]int)
	for _, e := range def.Edges {
		inDegree[e.Target]++
	}

	for len(workQueue) > 0 {
		work := workQueue[0]
		workQueue = workQueue[1:]
		node := work.node

		if completed[node.ID] {
			continue
		}

		logger.Debug().Str("node_id", node.ID).Str("kind", string(node.Kind)).Msg("executing node")

		output, err := e.executeNode(ctx, logger, runID, projectID, node, work.input)
		if err != nil {
			// Mark run failed
			errMsg := err.Error()
			e.db.ExecContext(ctx,
				`UPDATE runs SET status='failed', error=$1, completed_at=now(), updated_at=now() WHERE id=$2`,
				errMsg, runID)
			e.emit(ctx, runID, projectID, models.EventKindRunFailed, nil, map[string]any{"error": errMsg, "node_id": node.ID})
			e.broadcaster.Publish(runID, SSEEvent{Type: "run_failed", RunID: runID, Data: map[string]any{"error": errMsg}})
			return fmt.Errorf("node %s (%s) failed: %w", node.ID, node.Kind, err)
		}

		completed[node.ID] = true

		// Enqueue successors
		edges := adj[node.ID]
		for _, edge := range edges {
			// Branch condition evaluation
			if edge.Condition != "" {
				if !e.evalCondition(edge.Condition, output) {
					continue
				}
			}

			successor, ok := def.nodeByID(edge.Target)
			if !ok {
				continue
			}
			if completed[successor.ID] {
				continue
			}

			// Fan-in: only add to work queue when all inbound edges are satisfied
			inboundSatisfied[successor.ID]++
			if inboundSatisfied[successor.ID] >= inDegree[successor.ID] {
				workQueue = append(workQueue, nodeWork{node: successor, input: output})
			}
		}
	}

	// ── Mark run succeeded ────────────────────────────────
	e.db.ExecContext(ctx,
		`UPDATE runs SET status='succeeded', completed_at=now(), updated_at=now() WHERE id=$1`, runID)
	e.emit(ctx, runID, projectID, models.EventKindRunSucceeded, nil, map[string]any{"workflow_id": workflowID})
	e.broadcaster.Publish(runID, SSEEvent{Type: "run_succeeded", RunID: runID})
	logger.Info().Msg("workflow run succeeded")
	return nil
}

// executeNode dispatches a single node based on its kind.
func (e *Engine) executeNode(
	ctx context.Context,
	logger zerolog.Logger,
	runID, projectID uuid.UUID,
	node WorkflowNode,
	input models.JSONMap,
) (models.JSONMap, error) {
	e.emit(ctx, runID, projectID, models.EventKindNodeStarted, nil, map[string]any{
		"node_id":   node.ID,
		"node_kind": node.Kind,
		"node_label": node.Label,
	})
	e.broadcaster.Publish(runID, SSEEvent{
		Type:  "node_started",
		RunID: runID,
		Data:  map[string]any{"node_id": node.ID, "node_kind": string(node.Kind)},
	})

	var output models.JSONMap
	var execErr error

	switch node.Kind {
	case NodeKindTrigger:
		// Trigger nodes are entry points — they pass input through unchanged.
		output = input

	case NodeKindAgent:
		output, execErr = e.executeAgentNode(ctx, logger, runID, projectID, node, input)

	case NodeKindTool:
		output, execErr = e.executeToolNode(ctx, logger, runID, projectID, node, input)

	case NodeKindMemory:
		output, execErr = e.executeMemoryNode(ctx, logger, runID, projectID, node, input)

	case NodeKindApproval:
		output, execErr = e.executeApprovalNode(ctx, logger, runID, projectID, node, input)

	case NodeKindBranch:
		// Branch nodes pass input through; routing is handled in the edge loop.
		output = input

	case NodeKindSubWorkflow:
		output, execErr = e.executeSubWorkflowNode(ctx, logger, runID, projectID, node, input)

	case NodeKindOutput:
		output, execErr = e.executeOutputNode(ctx, logger, runID, projectID, node, input)

	default:
		execErr = fmt.Errorf("unknown node kind: %s", node.Kind)
	}

	if execErr != nil {
		e.emit(ctx, runID, projectID, models.EventKindNodeFailed, nil, map[string]any{
			"node_id": node.ID,
			"error":   execErr.Error(),
		})
		e.broadcaster.Publish(runID, SSEEvent{
			Type:  "node_failed",
			RunID: runID,
			Data:  map[string]any{"node_id": node.ID, "error": execErr.Error()},
		})
		return nil, execErr
	}

	e.emit(ctx, runID, projectID, models.EventKindNodeSucceeded, nil, map[string]any{
		"node_id":   node.ID,
		"node_kind": node.Kind,
	})
	e.broadcaster.Publish(runID, SSEEvent{
		Type:  "node_succeeded",
		RunID: runID,
		Data:  map[string]any{"node_id": node.ID},
	})
	return output, nil
}

// ── Node executors ────────────────────────────────────────────

func (e *Engine) executeAgentNode(ctx context.Context, logger zerolog.Logger, runID, projectID uuid.UUID, node WorkflowNode, input models.JSONMap) (models.JSONMap, error) {
	if node.Config.AgentSpecID == nil {
		return nil, fmt.Errorf("agent node %s: no agent_spec_id configured", node.ID)
	}

	// Load spec
	var spec models.AgentSpec
	err := e.db.QueryRowContext(ctx,
		`SELECT id, project_id, name, slug, version, role, goal, system_instructions,
		        provider_config_id, model, budget_cents, tool_grants, guardrails
		 FROM agent_specs WHERE id=$1`, *node.Config.AgentSpecID).Scan(
		&spec.ID, &spec.ProjectID, &spec.Name, &spec.Slug, &spec.Version,
		&spec.Role, &spec.Goal, &spec.SystemInstructions,
		&spec.ProviderConfigID, &spec.Model, &spec.BudgetCents, &spec.ToolGrants, &spec.Guardrails)
	if err != nil {
		return nil, fmt.Errorf("agent node: load spec %s: %w", *node.Config.AgentSpecID, err)
	}

	// Create an ephemeral AgentInstance for this node execution
	instanceID := uuid.New()
	instance := &models.AgentInstance{
		ID:        instanceID,
		ProjectID: projectID,
		SpecID:    spec.ID,
		Name:      fmt.Sprintf("%s (node %s)", spec.Name, node.ID),
	}
	e.db.ExecContext(ctx,
		`INSERT INTO agent_instances (id, project_id, spec_id, name, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,now(),now())`,
		instance.ID, instance.ProjectID, instance.SpecID, instance.Name)

	// The node run shares the parent workflow run
	agentRun := &models.Run{
		ID:              runID,
		ProjectID:       projectID,
		AgentInstanceID: &instanceID,
		Input:           input,
		Status:          models.RunStatusRunning,
	}

	provider, ok := e.providers.Default()
	if !ok {
		return nil, fmt.Errorf("no default provider configured")
	}

	cfg := agent.RunConfig{
		Run:      agentRun,
		Spec:     &spec,
		Instance: instance,
		Provider: provider,
	}

	if err := e.agentRT.Execute(ctx, cfg); err != nil {
		return nil, fmt.Errorf("agent node execution: %w", err)
	}

	return models.JSONMap{"node_id": node.ID, "status": "succeeded"}, nil
}

func (e *Engine) executeToolNode(ctx context.Context, logger zerolog.Logger, runID, projectID uuid.UUID, node WorkflowNode, input models.JSONMap) (models.JSONMap, error) {
	// Tool-only nodes (no full agent loop) will be wired to the MCP gateway in Part 5.
	// For now, record and pass through.
	logger.Info().Str("tool", node.Config.ToolName).Msg("tool node (stub — wire MCP gateway in Part 5)")
	return models.JSONMap{"node_id": node.ID, "tool": node.Config.ToolName, "status": "stub"}, nil
}

func (e *Engine) executeMemoryNode(ctx context.Context, logger zerolog.Logger, runID, projectID uuid.UUID, node WorkflowNode, input models.JSONMap) (models.JSONMap, error) {
	// Memory read/write nodes will be wired to the Memory Fabric in Part 3.
	logger.Info().Str("op", node.Config.MemoryOp).Msg("memory node (stub — wire Memory Fabric in Part 3)")
	return models.JSONMap{"node_id": node.ID, "op": node.Config.MemoryOp, "status": "stub"}, nil
}

func (e *Engine) executeApprovalNode(ctx context.Context, logger zerolog.Logger, runID, projectID uuid.UUID, node WorkflowNode, input models.JSONMap) (models.JSONMap, error) {
	// Create an ApprovalRequest and poll until resolved (or context cancelled).
	approvalID := uuid.New()
	msg := node.Config.ApprovalMsg
	if msg == "" {
		msg = fmt.Sprintf("Approval required at node '%s'", node.Label)
	}
	payloadJSON, _ := json.Marshal(input)

	_, err := e.db.ExecContext(ctx,
		`INSERT INTO approval_requests (id, project_id, run_id, reason_type, reason_detail, payload, status, created_at)
		 VALUES ($1,$2,$3,'custom',$4,$5,'pending',now())`,
		approvalID, projectID, runID, msg, string(payloadJSON))
	if err != nil {
		return nil, fmt.Errorf("approval node: create request: %w", err)
	}

	e.emit(ctx, runID, projectID, models.EventKindApprovalRequested, nil, map[string]any{
		"approval_id": approvalID,
		"node_id":     node.ID,
		"message":     msg,
	})
	e.broadcaster.Publish(runID, SSEEvent{
		Type:  "approval_requested",
		RunID: runID,
		Data:  map[string]any{"approval_id": approvalID.String(), "message": msg},
	})

	// Mark run as paused while we wait
	e.db.ExecContext(ctx, `UPDATE runs SET status='paused', updated_at=now() WHERE id=$1`, runID)

	// Poll for resolution (timeout after 24h)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	deadline := time.Now().Add(24 * time.Hour)

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				e.db.ExecContext(ctx,
					`UPDATE approval_requests SET status='expired', resolved_at=now() WHERE id=$1`, approvalID)
				return nil, fmt.Errorf("approval timed out after 24h")
			}

			var status string
			e.db.QueryRowContext(ctx,
				`SELECT status FROM approval_requests WHERE id=$1`, approvalID).Scan(&status)

			switch status {
			case "approved":
				e.db.ExecContext(ctx, `UPDATE runs SET status='running', updated_at=now() WHERE id=$1`, runID)
				e.emit(ctx, runID, projectID, models.EventKindApprovalResolved, nil, map[string]any{
					"approval_id": approvalID, "outcome": "approved",
				})
				return models.JSONMap{"approved": true, "approval_id": approvalID.String()}, nil
			case "rejected":
				return nil, fmt.Errorf("approval rejected")
			}
		}
	}
}

func (e *Engine) executeSubWorkflowNode(ctx context.Context, logger zerolog.Logger, runID, projectID uuid.UUID, node WorkflowNode, input models.JSONMap) (models.JSONMap, error) {
	if node.Config.SubWorkflowID == nil {
		return nil, fmt.Errorf("sub-workflow node %s: no sub_workflow_id configured", node.ID)
	}
	logger.Info().Str("sub_workflow_id", node.Config.SubWorkflowID.String()).Msg("executing sub-workflow")
	childRun, err := e.StartWorkflowRun(ctx, projectID, *node.Config.SubWorkflowID, input, nil)
	if err != nil {
		return nil, fmt.Errorf("sub-workflow node: start child run: %w", err)
	}
	// For now, we enqueue and don't wait (fire-and-forget pattern).
	// Full fan-out with child-completion signalling is a Part 6 enhancement.
	return models.JSONMap{"child_run_id": childRun.ID.String()}, nil
}

func (e *Engine) executeOutputNode(ctx context.Context, logger zerolog.Logger, runID, projectID uuid.UUID, node WorkflowNode, input models.JSONMap) (models.JSONMap, error) {
	key := node.Config.OutputKey
	if key == "" {
		key = "output"
	}
	// Persist run output in the run's input field (overloaded as output store for now)
	outputJSON, _ := json.Marshal(map[string]any{key: input})
	e.db.ExecContext(ctx,
		`UPDATE runs SET input=input || $1::jsonb, updated_at=now() WHERE id=$2`,
		string(outputJSON), runID)
	logger.Info().Str("key", key).Msg("output node persisted result")
	return input, nil
}

// ── Helpers ───────────────────────────────────────────────────

// replayCompletedNodes reads the event log to find which node IDs already
// have a node_succeeded event in this run (for crash-resume).
func (e *Engine) replayCompletedNodes(ctx context.Context, runID uuid.UUID) map[string]bool {
	rows, err := e.db.QueryContext(ctx,
		`SELECT payload FROM events WHERE run_id=$1 AND kind='node_succeeded'`, runID)
	if err != nil {
		return map[string]bool{}
	}
	defer rows.Close()
	done := make(map[string]bool)
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			continue
		}
		var p map[string]string
		if err := json.Unmarshal(raw, &p); err != nil {
			continue
		}
		if id, ok := p["node_id"]; ok {
			done[id] = true
		}
	}
	return done
}

// evalCondition evaluates a simple condition string against the node output.
// Supported syntax: "key==value" or "key!=value". Returns true if matched.
func (e *Engine) evalCondition(condition string, output models.JSONMap) bool {
	if output == nil {
		return false
	}
	for op, parts := range map[string]func(a, b string) bool{
		"==": func(a, b string) bool { return a == b },
		"!=": func(a, b string) bool { return a != b },
	} {
		idx := len(condition)
		for i := 0; i+len(op) <= len(condition); i++ {
			if condition[i:i+len(op)] == op {
				idx = i
				key := condition[:idx]
				val := condition[idx+len(op):]
				if v, ok := output[key]; ok {
					return parts(fmt.Sprintf("%v", v), val)
				}
				return false
			}
		}
		_ = idx
	}
	return true // no condition means always-match
}

// emit records an event row for the workflow run.
func (e *Engine) emit(ctx context.Context, runID, projectID uuid.UUID, kind models.EventKind, agentID *uuid.UUID, payload map[string]any) {
	raw, _ := json.Marshal(payload)
	e.db.ExecContext(ctx,
		`INSERT INTO events (run_id, project_id, seq, kind, agent_instance_id, payload)
		 VALUES ($1, $2,
		   (SELECT COALESCE(MAX(seq),0)+1 FROM events WHERE run_id=$1),
		   $3, $4, $5)`,
		runID, projectID, kind, agentID, string(raw))
}

type runAgentNodePayload struct {
	RunID     string `json:"run_id"`
	NodeID    string `json:"node_id"`
	ProjectID string `json:"project_id"`
}

func (e *Engine) handleRunAgentNodeJob(ctx context.Context, job *queue.Job) error {
	// This handler is reserved for parallel fan-out (Part 6).
	// For now, agent nodes run inline in handleRunWorkflowJob.
	return nil
}
