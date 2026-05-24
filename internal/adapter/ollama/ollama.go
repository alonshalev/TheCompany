// Package ollama provides a Symbiont model adapter for Ollama.
// Ollama runs LLMs locally (LLaMA 3, Mistral, Phi-3, Gemma, Code Llama, etc.)
// and exposes a REST API at http://localhost:11434 by default.
//
// API reference: https://github.com/ollama/ollama/blob/main/docs/api.md
package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/symbiont-ai/symbiont/internal/adapter"
)

const (
	DefaultModel   = "llama3.1"
	defaultBaseURL = "http://localhost:11434"
)

// Adapter implements adapter.Provider for Ollama.
type Adapter struct {
	baseURL string
	client  *http.Client
}

// New creates a new Ollama adapter.
// baseURL should be the Ollama server address, e.g. "http://localhost:11434".
// Pass "" to use the default.
func New(baseURL string) *Adapter {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Adapter{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: 300 * time.Second}, // local inference can be slow
	}
}

func (a *Adapter) Name() string         { return "ollama" }
func (a *Adapter) DefaultModel() string { return DefaultModel }
func (a *Adapter) AvailableModels() []string {
	// These are the models Ollama commonly supports; the actual list depends
	// on what the user has pulled with `ollama pull <model>`.
	return []string{
		"llama3.1",
		"llama3.1:70b",
		"llama3.2",
		"mistral",
		"mistral-nemo",
		"phi3",
		"phi3:medium",
		"gemma2",
		"gemma2:27b",
		"codellama",
		"deepseek-coder-v2",
		"qwen2.5",
		"qwen2.5-coder",
	}
}

// ── Wire types (Ollama /api/chat schema) ──────────────────────

type ollamaMessage struct {
	Role      string        `json:"role"`
	Content   string        `json:"content"`
	ToolCalls []ollamaTC    `json:"tool_calls,omitempty"`
	Images    []string      `json:"images,omitempty"` // base64 (not used here)
}

type ollamaTC struct {
	Function ollamaTCFunc `json:"function"`
}

type ollamaTCFunc struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type ollamaTool struct {
	Type     string          `json:"type"` // "function"
	Function ollamaToolFunc  `json:"function"`
}

type ollamaToolFunc struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type ollamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Stream   bool            `json:"stream"`
	Tools    []ollamaTool    `json:"tools,omitempty"`
	Options  map[string]any  `json:"options,omitempty"`
}

type ollamaChatResponse struct {
	Model              string        `json:"model"`
	Message            ollamaMessage `json:"message"`
	Done               bool          `json:"done"`
	DoneReason         string        `json:"done_reason"` // "stop" | "length" | "tool_calls"
	PromptEvalCount    int           `json:"prompt_eval_count"`
	EvalCount          int           `json:"eval_count"`
	TotalDuration      int64         `json:"total_duration"`
	LoadDuration       int64         `json:"load_duration"`
}

// ── Complete ──────────────────────────────────────────────────

func (a *Adapter) Complete(ctx context.Context, req adapter.CompletionRequest) (*adapter.CompletionResponse, error) {
	body, err := a.buildRequest(req, false)
	if err != nil {
		return nil, fmt.Errorf("ollama: build request: %w", err)
	}

	httpResp, err := a.post(ctx, body)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()

	raw, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("ollama: read response: %w", err)
	}
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama: API error %d: %s", httpResp.StatusCode, string(raw))
	}

	var chat ollamaChatResponse
	if err := json.Unmarshal(raw, &chat); err != nil {
		return nil, fmt.Errorf("ollama: decode response: %w", err)
	}

	return a.parseResponse(req.Model, &chat), nil
}

// ── Stream ────────────────────────────────────────────────────

func (a *Adapter) Stream(ctx context.Context, req adapter.CompletionRequest) (<-chan adapter.StreamEvent, error) {
	body, err := a.buildRequest(req, true)
	if err != nil {
		return nil, fmt.Errorf("ollama: build stream request: %w", err)
	}

	httpResp, err := a.post(ctx, body)
	if err != nil {
		return nil, err
	}
	if httpResp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(httpResp.Body)
		httpResp.Body.Close()
		return nil, fmt.Errorf("ollama: API error %d: %s", httpResp.StatusCode, string(raw))
	}

	ch := make(chan adapter.StreamEvent, 64)

	go func() {
		defer close(ch)
		defer httpResp.Body.Close()

		var accumulated string
		var lastModel string

		// Ollama streams one JSON object per line (not SSE format)
		scanner := bufio.NewScanner(httpResp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}

			var chunk ollamaChatResponse
			if err := json.Unmarshal([]byte(line), &chunk); err != nil {
				log.Warn().Err(err).Msg("ollama: stream parse error")
				continue
			}

			lastModel = chunk.Model

			if chunk.Message.Content != "" {
				accumulated += chunk.Message.Content
				ch <- adapter.StreamEvent{Type: "delta", Content: chunk.Message.Content}
			}

			if chunk.Done {
				usage := adapter.Usage{
					InputTokens:  chunk.PromptEvalCount,
					OutputTokens: chunk.EvalCount,
					// Ollama is free/local — cost is 0
					CostCents: 0,
				}
				// Emit any tool calls from the final message
				if len(chunk.Message.ToolCalls) > 0 {
					tc := chunk.Message.ToolCalls[0]
					ch <- adapter.StreamEvent{
						Type:       "done",
						Content:    accumulated,
						FinalUsage: &usage,
						ToolCall: &adapter.ToolCall{
							ID:    fmt.Sprintf("tc_%s_%d", lastModel, time.Now().UnixMilli()),
							Name:  tc.Function.Name,
							Input: tc.Function.Arguments,
						},
					}
				} else {
					ch <- adapter.StreamEvent{Type: "done", Content: accumulated, FinalUsage: &usage}
				}
				return
			}
		}

		if err := scanner.Err(); err != nil {
			if ctx.Err() == nil {
				log.Error().Err(err).Msg("ollama: stream scan error")
			}
			ch <- adapter.StreamEvent{Type: "error", Error: err.Error()}
		}
	}()

	return ch, nil
}

// ── Helpers ───────────────────────────────────────────────────

func (a *Adapter) buildRequest(req adapter.CompletionRequest, stream bool) ([]byte, error) {
	model := req.Model
	if model == "" {
		model = DefaultModel
	}

	var messages []ollamaMessage

	// Ollama uses "system" role messages natively
	if req.System != "" {
		messages = append(messages, ollamaMessage{Role: "system", Content: req.System})
	}

	for _, m := range req.Messages {
		switch m.Role {
		case "user":
			if len(m.ToolResults) > 0 {
				// Ollama expects tool results as user messages with a specific content
				for _, tr := range m.ToolResults {
					messages = append(messages, ollamaMessage{
						Role:    "tool",
						Content: tr.Content,
					})
				}
			} else {
				messages = append(messages, ollamaMessage{Role: "user", Content: m.Content})
			}
		case "assistant":
			msg := ollamaMessage{Role: "assistant", Content: m.Content}
			for _, tc := range m.ToolCalls {
				msg.ToolCalls = append(msg.ToolCalls, ollamaTC{
					Function: ollamaTCFunc{
						Name:      tc.Name,
						Arguments: tc.Input,
					},
				})
			}
			messages = append(messages, msg)
		}
	}

	cr := ollamaChatRequest{
		Model:    model,
		Messages: messages,
		Stream:   stream,
	}

	if req.MaxTokens > 0 {
		cr.Options = map[string]any{"num_predict": req.MaxTokens}
	}
	if req.Temperature != nil {
		if cr.Options == nil {
			cr.Options = map[string]any{}
		}
		cr.Options["temperature"] = *req.Temperature
	}

	for _, t := range req.Tools {
		cr.Tools = append(cr.Tools, ollamaTool{
			Type: "function",
			Function: ollamaToolFunc{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}

	return json.Marshal(cr)
}

func (a *Adapter) parseResponse(model string, chat *ollamaChatResponse) *adapter.CompletionResponse {
	var toolCalls []adapter.ToolCall
	for i, tc := range chat.Message.ToolCalls {
		toolCalls = append(toolCalls, adapter.ToolCall{
			ID:    fmt.Sprintf("tc_%s_%d", model, i),
			Name:  tc.Function.Name,
			Input: tc.Function.Arguments,
		})
	}

	stopReason := chat.DoneReason
	if stopReason == "" {
		if len(toolCalls) > 0 {
			stopReason = "tool_use"
		} else {
			stopReason = "end_turn"
		}
	}

	return &adapter.CompletionResponse{
		ID:         fmt.Sprintf("ollama-%d", time.Now().UnixMilli()),
		Model:      chat.Model,
		Content:    chat.Message.Content,
		ToolCalls:  toolCalls,
		StopReason: stopReason,
		Usage: adapter.Usage{
			InputTokens:  chat.PromptEvalCount,
			OutputTokens: chat.EvalCount,
			CostCents:    0, // local inference — no cost
		},
	}
}

func (a *Adapter) post(ctx context.Context, body []byte) (*http.Response, error) {
	url := a.baseURL + "/api/chat"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama: HTTP error: %w", err)
	}
	return resp, nil
}
