// Package factory — Agent Factory.
//
// The Architect is a meta-agent that synthesises AgentSpecs from natural
// language descriptions. It calls the default LLM provider with a structured
// meta-prompt and returns a draft AgentSpec ready for human review before
// being saved to the database.
//
// The Architect itself does NOT call tools — it relies entirely on the model's
// reasoning to produce a valid JSON AgentSpec. A future version will add a
// validation loop where the Architect iteratively refines its output.
package factory

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/symbiont-ai/symbiont/internal/adapter"
	"github.com/symbiont-ai/symbiont/internal/models"
)

const (
	architectModel     = "claude-sonnet-4-6"
	architectMaxTokens = 2048
	synthesisTimeout   = 60 * time.Second
)

// synthesisPayload is the JSON structure the Architect produces.
// It maps directly onto AgentSpec fields with string arrays for scopes.
type synthesisPayload struct {
	Name               string         `json:"name"`
	Slug               string         `json:"slug"`
	Role               string         `json:"role"`
	Goal               string         `json:"goal"`
	SystemInstructions string         `json:"system_instructions"`
	Model              string         `json:"model"`
	MemoryReadScopes   []string       `json:"memory_read_scopes"`
	MemoryWriteScopes  []string       `json:"memory_write_scopes"`
	BudgetCents        int64          `json:"budget_cents"`
	ToolGrants         []any          `json:"tool_grants"`
	Guardrails         map[string]any `json:"guardrails"`
}

// Architect is the meta-agent that synthesises AgentSpecs.
type Architect struct {
	registry *adapter.Registry
}

// NewArchitect creates an Architect backed by the given provider registry.
func NewArchitect(registry *adapter.Registry) *Architect {
	return &Architect{registry: registry}
}

// Synthesize takes a natural-language description and returns a draft AgentSpec.
// The spec is NOT saved to the database — the caller must do that if desired.
// projectID is embedded in the spec so it can be inserted immediately after review.
func (a *Architect) Synthesize(ctx context.Context, projectID uuid.UUID, prompt string) (*models.AgentSpec, error) {
	provider, ok := a.registry.Default()
	if !ok {
		return nil, fmt.Errorf("architect: no default LLM provider registered")
	}

	ctx, cancel := context.WithTimeout(ctx, synthesisTimeout)
	defer cancel()

	req := adapter.CompletionRequest{
		Model:     architectModel,
		MaxTokens: architectMaxTokens,
		System:    architectSystemPrompt,
		Messages: []adapter.Message{
			{
				Role:    "user",
				Content: "Design an agent that: " + prompt,
			},
		},
	}

	resp, err := provider.Complete(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("architect: LLM call failed: %w", err)
	}

	payload, err := parseArchitectResponse(resp.Content)
	if err != nil {
		log.Error().Err(err).Str("raw", resp.Content).Msg("architect: failed to parse synthesis response")
		return nil, fmt.Errorf("architect: could not parse model response: %w", err)
	}

	spec := payloadToSpec(projectID, prompt, payload)
	return spec, nil
}

// parseArchitectResponse extracts and unmarshals the JSON from the model's reply.
// The model is instructed to return only JSON, but defensively we also strip
// any markdown code fences the model may wrap it in.
func parseArchitectResponse(raw string) (*synthesisPayload, error) {
	raw = strings.TrimSpace(raw)

	// Strip optional ```json ... ``` fences
	jsonFence := regexp.MustCompile("(?s)```(?:json)?\\s*(\\{.*?\\})\\s*```")
	if m := jsonFence.FindStringSubmatch(raw); len(m) == 2 {
		raw = m[1]
	}

	// Find the outermost JSON object if there's surrounding text
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start >= 0 && end > start {
		raw = raw[start : end+1]
	}

	var p synthesisPayload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &p, nil
}

// payloadToSpec maps a synthesis payload onto a models.AgentSpec with defaults.
func payloadToSpec(projectID uuid.UUID, sourcePrompt string, p *synthesisPayload) *models.AgentSpec {
	model := p.Model
	if !isValidModel(model) {
		model = "claude-sonnet-4-6"
	}

	readScopes := toMemoryScopes(p.MemoryReadScopes, []models.MemoryScope{models.MemoryScopeProject})
	writeScopes := toMemoryScopes(p.MemoryWriteScopes, []models.MemoryScope{models.MemoryScopeWorkflow})

	budgetCents := p.BudgetCents
	if budgetCents <= 0 {
		budgetCents = 500 // $5 default
	}

	toolGrants := models.JSONSlice(p.ToolGrants)
	if toolGrants == nil {
		toolGrants = models.JSONSlice{}
	}

	guardrails := models.JSONMap(p.Guardrails)
	if guardrails == nil {
		guardrails = models.JSONMap{}
	}

	slug := SanitizeSlug(p.Slug)
	if slug == "" {
		slug = SanitizeSlug(p.Name)
	}

	src := sourcePrompt
	now := time.Now()
	return &models.AgentSpec{
		ID:                 uuid.New(),
		ProjectID:          projectID,
		Name:               p.Name,
		Slug:               slug,
		Version:            1,
		Role:               p.Role,
		Goal:               p.Goal,
		SystemInstructions: p.SystemInstructions,
		Model:              model,
		MemoryReadScopes:   readScopes,
		MemoryWriteScopes:  writeScopes,
		BudgetCents:        budgetCents,
		ToolGrants:         toolGrants,
		Guardrails:         guardrails,
		SynthesizedFrom:    &src,
		IsActive:           true,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
}

// isValidModel checks that the model string is one we know about.
func isValidModel(model string) bool {
	known := map[string]bool{
		"claude-opus-4-6":          true,
		"claude-sonnet-4-6":        true,
		"claude-haiku-4-5-20251001": true,
	}
	return known[model]
}

// toMemoryScopes converts string slice to []MemoryScope, using defaults if empty.
func toMemoryScopes(raw []string, defaults []models.MemoryScope) []models.MemoryScope {
	if len(raw) == 0 {
		return defaults
	}
	valid := map[string]bool{
		"private": true, "agent": true, "workflow": true, "project": true, "global": true,
	}
	out := make([]models.MemoryScope, 0, len(raw))
	for _, s := range raw {
		if valid[s] {
			out = append(out, models.MemoryScope(s))
		}
	}
	if len(out) == 0 {
		return defaults
	}
	return out
}

// SanitizeSlug converts a string to a lowercase, hyphen-separated slug.
// Exported so handlers can generate slugs from names without re-implementing the logic.
var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

func SanitizeSlug(s string) string {
	s = strings.ToLower(s)
	s = nonAlnum.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}

// architectSystemPrompt is the meta-prompt injected as the system turn.
const architectSystemPrompt = `You are the Architect — a meta-agent that designs specialised AI agents for SaaS product operations.

Given a natural-language description of what an agent should do, produce a JSON object that matches this schema EXACTLY. Return ONLY the JSON object — no commentary, no markdown fences, no explanation.

Schema:
{
  "name": "<string: Human-readable name, e.g. 'Customer Research Analyst'>",
  "slug": "<string: URL-safe lowercase with hyphens, e.g. 'customer-research-analyst'>",
  "role": "<string: Short role label, e.g. 'Researcher'>",
  "goal": "<string: One sentence describing what this agent achieves for the operator>",
  "system_instructions": "<string: 3-6 paragraph system prompt for the agent. Be specific about persona, capabilities, constraints, and output format>",
  "model": "<string: exactly one of 'claude-opus-4-6' | 'claude-sonnet-4-6' | 'claude-haiku-4-5-20251001'>",
  "memory_read_scopes": ["<string: each must be one of private|agent|workflow|project|global>"],
  "memory_write_scopes": ["<string: each must be one of private|agent|workflow|project|global>"],
  "budget_cents": <integer: USD cents; 100 for trivial tasks, 500 for typical, 2000 for deep research, 10000 for complex multi-step>,
  "tool_grants": ["<string: MCP tool names this agent should have access to, e.g. 'web_search', 'read_file', 'execute_code'>"],
  "guardrails": {
    "max_steps": <integer: 10-100, default 50>,
    "require_approval_for": ["<string: action categories needing human sign-off, e.g. 'send_email', 'write_file', 'external_api'>"]
  }
}

Guidelines:
- Use claude-opus-4-6 ONLY for tasks requiring deep reasoning or complex code generation.
- Use claude-sonnet-4-6 for most tasks (research, writing, analysis, integration work).
- Use claude-haiku-4-5-20251001 for high-volume, simple, or latency-sensitive tasks.
- Set memory_read_scopes to the minimum required: 'workflow' for short-lived context, 'project' for persistent facts, 'global' only for truly cross-project knowledge.
- Set memory_write_scopes conservatively — agents should write to the narrowest scope that meets their needs.
- tool_grants should list only what the agent actually needs.
- system_instructions must be thorough: define persona, capabilities, what the agent must never do, and expected output format.`
