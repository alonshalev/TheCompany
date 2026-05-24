// Package openrouter provides a Symbiont model adapter for OpenRouter.ai.
// OpenRouter exposes an OpenAI-compatible Chat Completions API that routes
// to 200+ models (GPT-4o, Claude, Gemini, Mistral, LLaMA, etc.).
//
// The adapter reuses the OpenAI wire format; the only differences are:
//   - Base URL: https://openrouter.ai/api/v1
//   - Extra headers: HTTP-Referer, X-Title (for OpenRouter analytics)
//   - Model names use "provider/model" format (e.g. "anthropic/claude-3-5-sonnet")
package openrouter

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
	DefaultModel   = "anthropic/claude-sonnet-4-6"
	defaultBaseURL = "https://openrouter.ai/api/v1"
)

// pricingCentsPerMillionTokens — a best-effort snapshot for popular models.
// OpenRouter pricing varies by model; unknown models fall back to "default".
var pricingCentsPerMillionTokens = map[string][2]int{
	"anthropic/claude-sonnet-4-6":    {300, 1500},
	"anthropic/claude-opus-4-6":      {1500, 7500},
	"openai/gpt-4o":                   {500, 1500},
	"openai/gpt-4o-mini":              {15, 60},
	"google/gemini-1.5-pro":           {350, 1050},
	"google/gemini-1.5-flash":         {35, 105},
	"meta-llama/llama-3.1-70b-instruct": {59, 79},
	"mistralai/mistral-large":          {200, 600},
	"default":                          {200, 600},
}

// Adapter implements adapter.Provider for OpenRouter.
type Adapter struct {
	apiKey  string
	baseURL string
	appName string // sent as X-Title for OpenRouter dashboard
	client  *http.Client
}

// New creates a new OpenRouter adapter.
// appName is shown in the OpenRouter usage dashboard; pass "" to default to "Symbiont".
func New(apiKey, appName string) (*Adapter, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("openrouter: API key is required")
	}
	if appName == "" {
		appName = "Symbiont"
	}
	return &Adapter{
		apiKey:  apiKey,
		baseURL: defaultBaseURL,
		appName: appName,
		client:  &http.Client{Timeout: 120 * time.Second},
	}, nil
}

func (a *Adapter) Name() string         { return "openrouter" }
func (a *Adapter) DefaultModel() string { return DefaultModel }
func (a *Adapter) AvailableModels() []string {
	return []string{
		"anthropic/claude-sonnet-4-6",
		"anthropic/claude-opus-4-6",
		"openai/gpt-4o",
		"openai/gpt-4o-mini",
		"google/gemini-1.5-pro",
		"google/gemini-1.5-flash",
		"meta-llama/llama-3.1-70b-instruct",
		"mistralai/mistral-large",
		// Many more available via OpenRouter — use any model slug from openrouter.ai/models
	}
}

// ── Wire types (OpenAI-compatible) ────────────────────────────

type chatMessage struct {
	Role       string     `json:"role"`
	Content    any        `json:"content"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type toolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}

type toolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type toolDefinition struct {
	Type     string         `json:"type"`
	Function toolFuncSchema `json:"function"`
}

type toolFuncSchema struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type chatRequest struct {
	Model       string           `json:"model"`
	Messages    []chatMessage    `json:"messages"`
	Tools       []toolDefinition `json:"tools,omitempty"`
	MaxTokens   int              `json:"max_tokens,omitempty"`
	Temperature *float64         `json:"temperature,omitempty"`
	Stream      bool             `json:"stream"`
	Stop        []string         `json:"stop,omitempty"`
}

type chatResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message      chatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

type streamChunk struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Role      string     `json:"role"`
			Content   string     `json:"content"`
			ToolCalls []toolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// ── Complete ──────────────────────────────────────────────────

func (a *Adapter) Complete(ctx context.Context, req adapter.CompletionRequest) (*adapter.CompletionResponse, error) {
	body, err := a.buildRequest(req, false)
	if err != nil {
		return nil, fmt.Errorf("openrouter: build request: %w", err)
	}

	resp, err := a.post(ctx, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("openrouter: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openrouter: API error %d: %s", resp.StatusCode, string(raw))
	}

	var chat chatResponse
	if err := json.Unmarshal(raw, &chat); err != nil {
		return nil, fmt.Errorf("openrouter: decode response: %w", err)
	}

	return a.parseChatResponse(&chat), nil
}

// ── Stream ────────────────────────────────────────────────────

func (a *Adapter) Stream(ctx context.Context, req adapter.CompletionRequest) (<-chan adapter.StreamEvent, error) {
	body, err := a.buildRequest(req, true)
	if err != nil {
		return nil, fmt.Errorf("openrouter: build stream request: %w", err)
	}

	resp, err := a.post(ctx, body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("openrouter: API error %d: %s", resp.StatusCode, string(raw))
	}

	ch := make(chan adapter.StreamEvent, 64)

	go func() {
		defer close(ch)
		defer resp.Body.Close()

		var accumulated string

		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				break
			}

			var chunk streamChunk
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				continue
			}

			for _, choice := range chunk.Choices {
				if choice.Delta.Content != "" {
					accumulated += choice.Delta.Content
					ch <- adapter.StreamEvent{Type: "delta", Content: choice.Delta.Content}
				}
			}

			if chunk.Usage != nil {
				usage := a.computeUsage(chunk.Model, chunk.Usage.PromptTokens, chunk.Usage.CompletionTokens)
				ch <- adapter.StreamEvent{Type: "done", Content: accumulated, FinalUsage: &usage}
				return
			}
		}

		if err := scanner.Err(); err != nil {
			if ctx.Err() == nil {
				log.Error().Err(err).Msg("openrouter: stream scan error")
			}
			ch <- adapter.StreamEvent{Type: "error", Error: err.Error()}
			return
		}

		usage := adapter.Usage{}
		ch <- adapter.StreamEvent{Type: "done", Content: accumulated, FinalUsage: &usage}
	}()

	return ch, nil
}

// ── Helpers ───────────────────────────────────────────────────

func (a *Adapter) buildRequest(req adapter.CompletionRequest, stream bool) ([]byte, error) {
	model := req.Model
	if model == "" {
		model = DefaultModel
	}

	var messages []chatMessage
	if req.System != "" {
		messages = append(messages, chatMessage{Role: "system", Content: req.System})
	}

	for _, m := range req.Messages {
		switch m.Role {
		case "user":
			if len(m.ToolResults) > 0 {
				for _, tr := range m.ToolResults {
					messages = append(messages, chatMessage{
						Role:       "tool",
						Content:    tr.Content,
						ToolCallID: tr.ToolCallID,
					})
				}
			} else {
				messages = append(messages, chatMessage{Role: "user", Content: m.Content})
			}
		case "assistant":
			msg := chatMessage{Role: "assistant", Content: m.Content}
			for _, tc := range m.ToolCalls {
				argsJSON, _ := json.Marshal(tc.Input)
				msg.ToolCalls = append(msg.ToolCalls, toolCall{
					ID:   tc.ID,
					Type: "function",
					Function: toolFunction{
						Name:      tc.Name,
						Arguments: string(argsJSON),
					},
				})
			}
			messages = append(messages, msg)
		}
	}

	cr := chatRequest{
		Model:       model,
		Messages:    messages,
		Stream:      stream,
		Temperature: req.Temperature,
		Stop:        req.StopSequences,
	}
	if req.MaxTokens > 0 {
		cr.MaxTokens = req.MaxTokens
	}
	for _, t := range req.Tools {
		cr.Tools = append(cr.Tools, toolDefinition{
			Type: "function",
			Function: toolFuncSchema{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}

	return json.Marshal(cr)
}

func (a *Adapter) parseChatResponse(chat *chatResponse) *adapter.CompletionResponse {
	if len(chat.Choices) == 0 {
		return &adapter.CompletionResponse{ID: chat.ID, Model: chat.Model}
	}
	choice := chat.Choices[0]
	msg := choice.Message

	var toolCalls []adapter.ToolCall
	for _, tc := range msg.ToolCalls {
		var input map[string]any
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &input); err != nil {
			input = map[string]any{"_raw": tc.Function.Arguments}
		}
		toolCalls = append(toolCalls, adapter.ToolCall{
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: input,
		})
	}

	content, _ := msg.Content.(string)
	stopReason := choice.FinishReason
	if stopReason == "tool_calls" {
		stopReason = "tool_use"
	}

	usage := a.computeUsage(chat.Model, chat.Usage.PromptTokens, chat.Usage.CompletionTokens)

	return &adapter.CompletionResponse{
		ID:         chat.ID,
		Model:      chat.Model,
		Content:    content,
		ToolCalls:  toolCalls,
		StopReason: stopReason,
		Usage:      usage,
	}
}

func (a *Adapter) post(ctx context.Context, body []byte) (*http.Response, error) {
	url := a.baseURL + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openrouter: create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+a.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("HTTP-Referer", "https://github.com/symbiont-ai/symbiont")
	httpReq.Header.Set("X-Title", a.appName)

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openrouter: HTTP error: %w", err)
	}
	return resp, nil
}

func (a *Adapter) computeUsage(model string, inputTokens, outputTokens int) adapter.Usage {
	pricing, ok := pricingCentsPerMillionTokens[model]
	if !ok {
		pricing = pricingCentsPerMillionTokens["default"]
	}
	inputCost := (inputTokens * pricing[0]) / 1_000_000
	outputCost := (outputTokens * pricing[1]) / 1_000_000
	return adapter.Usage{
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		CostCents:    inputCost + outputCost,
	}
}
