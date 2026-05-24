// Package orchestration defines the node types and graph model for workflows.
package orchestration

import "github.com/google/uuid"

// NodeKind identifies what a workflow node does.
type NodeKind string

const (
	NodeKindTrigger     NodeKind = "trigger"
	NodeKindAgent       NodeKind = "agent"
	NodeKindTool        NodeKind = "tool"
	NodeKindMemory      NodeKind = "memory"
	NodeKindApproval    NodeKind = "approval_gate"
	NodeKindBranch      NodeKind = "branch"
	NodeKindSubWorkflow NodeKind = "sub_workflow"
	NodeKindOutput      NodeKind = "output"
)

// WorkflowNode is a single node in the workflow graph definition.
// Stored as part of the Workflow.Definition JSONB column.
type WorkflowNode struct {
	ID       string   `json:"id"`     // stable string ID within the workflow (e.g. "agent-1")
	Kind     NodeKind `json:"kind"`
	Label    string   `json:"label"`
	// Config is node-type-specific configuration.
	Config   NodeConfig `json:"config"`
	// Position on the canvas (for the UI)
	Position Position `json:"position"`
}

// WorkflowEdge connects two nodes. Edges may carry a condition for branch nodes.
type WorkflowEdge struct {
	ID         string `json:"id"`
	Source     string `json:"source"`      // WorkflowNode.ID
	Target     string `json:"target"`      // WorkflowNode.ID
	// Condition is evaluated when the source is a Branch node.
	// Empty string means "always" (default edge).
	Condition  string `json:"condition,omitempty"`
}

// NodeConfig holds the type-specific configuration for each node kind.
type NodeConfig struct {
	// ── Agent node ─────────────────────────────────────────
	AgentSpecID      *uuid.UUID `json:"agent_spec_id,omitempty"`
	AgentPrompt      string     `json:"agent_prompt,omitempty"`   // used by Agent Factory

	// ── Tool / MCP node ────────────────────────────────────
	MCPServerID  *uuid.UUID `json:"mcp_server_id,omitempty"`
	ToolName     string     `json:"tool_name,omitempty"`
	ToolInput    any        `json:"tool_input,omitempty"`

	// ── Memory node ────────────────────────────────────────
	MemoryOp     string `json:"memory_op,omitempty"`    // "read" | "write"
	MemoryScope  string `json:"memory_scope,omitempty"`
	MemoryQuery  string `json:"memory_query,omitempty"`

	// ── Approval gate ──────────────────────────────────────
	ApprovalMsg  string `json:"approval_message,omitempty"`

	// ── Branch / Router ────────────────────────────────────
	// Conditions are embedded in edges (Condition field above).

	// ── Sub-workflow ───────────────────────────────────────
	SubWorkflowID *uuid.UUID `json:"sub_workflow_id,omitempty"`

	// ── Trigger ────────────────────────────────────────────
	TriggerKind  string `json:"trigger_kind,omitempty"`
	CronExpr     string `json:"cron_expr,omitempty"`

	// ── Output / Deploy ────────────────────────────────────
	OutputKey    string `json:"output_key,omitempty"`
	BlueprintID  *uuid.UUID `json:"blueprint_id,omitempty"`
}

// Position is an (x, y) canvas coordinate for the UI.
type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// WorkflowDefinition is the full graph parsed from Workflow.Definition.
type WorkflowDefinition struct {
	Nodes []WorkflowNode `json:"nodes"`
	Edges []WorkflowEdge `json:"edges"`
}

// adjacency builds a map of nodeID → [outgoing edges] for graph traversal.
func (d *WorkflowDefinition) adjacency() map[string][]WorkflowEdge {
	out := make(map[string][]WorkflowEdge, len(d.Nodes))
	for _, n := range d.Nodes {
		out[n.ID] = nil
	}
	for _, e := range d.Edges {
		out[e.Source] = append(out[e.Source], e)
	}
	return out
}

// startNodes returns all nodes with no incoming edges (entry points).
func (d *WorkflowDefinition) startNodes() []WorkflowNode {
	hasIncoming := make(map[string]bool)
	for _, e := range d.Edges {
		hasIncoming[e.Target] = true
	}
	var starts []WorkflowNode
	for _, n := range d.Nodes {
		if !hasIncoming[n.ID] {
			starts = append(starts, n)
		}
	}
	return starts
}

// nodeByID returns a node by its string ID.
func (d *WorkflowDefinition) nodeByID(id string) (WorkflowNode, bool) {
	for _, n := range d.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return WorkflowNode{}, false
}
