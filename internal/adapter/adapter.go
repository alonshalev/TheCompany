// Package adapter defines the model-agnostic interface that all LLM provider
// adapters must implement. This is the narrow seam between the Agent Runtime
// and any specific model provider (Anthropic, OpenAI, Ollama, etc.).
package adapter

import (
	"context"
	"fmt"
)

// Message represents a single turn in a conversation.
type Message struct {
	Role    string      `json:"role"` // "user" | "assistant" | "system"
	Content string      `json:"content"`
	// Tool calls produced by the model (assistant turn only)
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// Tool results being returned (user/tool turn)
	ToolResults []ToolResult `json:"tool_results,omitempty"`
}

// ToolDefinition describes a tool the model may call.
type ToolDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// InputSchema is a JSON Schema object describing the parameters.
	InputSchema map[string]any `json:"input_schema"`
}

// ToolCall is a tool invocation requested by the model.
type ToolCall struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Input    map[string]any `json:"input"`
}

// ToolResult is the response to a tool call.
type ToolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Content    string `json:"content"`
	IsError    bool   `json:"is_error"`
}

// CompletionRequest is the input to a model completion call.
type CompletionRequest struct {
	Model       string           `json:"model"`
	Messages    []Message        `json:"messages"`
	System      string           `json:"system,omitempty"`
	Tools       []ToolDefinition `json:"tools,omitempty"`
	MaxTokens   int              `json:"max_tokens,omitempty"`
	Temperature *float64         `json:"temperature,omitempty"`
	// StopSequences for early termination
	StopSequences []string `json:"stop_sequences,omitempty"`
}

// CompletionResponse is the output from a non-streaming completion.
type CompletionResponse struct {
	ID          string    `json:"id"`
	Model       string    `json:"model"`
	Content     string    `json:"content"`
	ToolCalls   []ToolCall `json:"tool_calls,omitempty"`
	StopReason  string    `json:"stop_reason"` // "end_turn" | "tool_use" | "max_tokens"
	Usage       Usage     `json:"usage"`
}

// Usage records token consumption for cost accounting.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	// CostCents is computed by the adapter based on the provider's pricing.
	CostCents int `json:"cost_cents"`
}

// StreamEvent is a single chunk in a streaming response.
type StreamEvent struct {
	Type        string `json:"type"` // "delta" | "tool_call_delta" | "done" | "error"
	Content     string `json:"content,omitempty"`
	ToolCall    *ToolCall `json:"tool_call,omitempty"`
	FinalUsage  *Usage `json:"final_usage,omitempty"`
	Error       string `json:"error,omitempty"`
}

// Provider is the model-agnostic interface every LLM adapter must satisfy.
type Provider interface {
	// Name returns the provider identifier (e.g. "anthropic", "openai").
	Name() string

	// Complete sends a request and returns the full response (non-streaming).
	Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error)

	// Stream sends a request and streams events to the channel until done or error.
	// The caller is responsible for consuming and closing the channel.
	Stream(ctx context.Context, req CompletionRequest) (<-chan StreamEvent, error)

	// DefaultModel returns the default model identifier for this provider.
	DefaultModel() string

	// AvailableModels returns the models this provider supports.
	AvailableModels() []string
}

// Registry holds all registered provider adapters.
type Registry struct {
	providers map[string]Provider
	def       string
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{providers: make(map[string]Provider)}
}

// Register adds a provider to the registry.
func (r *Registry) Register(p Provider) {
	r.providers[p.Name()] = p
}

// SetDefault sets the default provider name.
func (r *Registry) SetDefault(name string) {
	r.def = name
}

// Get returns the named provider, or an error if not found.
func (r *Registry) Get(name string) (Provider, bool) {
	p, ok := r.providers[name]
	return p, ok
}

// Default returns the default provider.
func (r *Registry) Default() (Provider, bool) {
	return r.Get(r.def)
}

// ErrProviderNotFound is returned when a named provider is not registered.
var ErrProviderNotFound = fmt.Errorf("provider not found in registry")
