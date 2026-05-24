// Package anthropic provides a Symbiont model adapter for Anthropic Claude.
// It wraps the official anthropic-sdk-go and maps its API to the
// provider-agnostic adapter.Provider interface.
package anthropic

import (
	"context"
	"encoding/json"
	"fmt"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/rs/zerolog/log"
	"github.com/symbiont-ai/symbiont/internal/adapter"
)

const (
	DefaultModel = "claude-opus-4-6"
)

// pricingCentsPerMillionTokens maps model → [input, output] in USD cents per 1M tokens.
// Keep this updated as pricing changes.
var pricingCentsPerMillionTokens = map[string][2]int{
	"claude-opus-4-6":          {1500, 7500},  // $15/$75 per 1M
	"claude-sonnet-4-6":        {300, 1500},   // $3/$15 per 1M
	"claude-haiku-4-5-20251001": {25, 125},    // $0.25/$1.25 per 1M
	// Fallback for unknown models
	"default": {1500, 7500},
}

// Adapter implements adapter.Provider for Anthropic Claude.
type Adapter struct {
	client *anthropicsdk.Client
	apiKey string
}

// New creates a new Anthropic adapter with the given API key.
func New(apiKey string) (*Adapter, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("anthropic: API key is required")
	}
	client := anthropicsdk.NewClient(option.WithAPIKey(apiKey))
	return &Adapter{client: client, apiKey: apiKey}, nil
}

func (a *Adapter) Name() string { return "anthropic" }

func (a *Adapter) DefaultModel() string { return DefaultModel }

func (a *Adapter) AvailableModels() []string {
	return []string{
		"claude-opus-4-6",
		"claude-sonnet-4-6",
		"claude-haiku-4-5-20251001",
	}
}

// Complete sends a request to the Anthropic Messages API and returns the full response.
func (a *Adapter) Complete(ctx context.Context, req adapter.CompletionRequest) (*adapter.CompletionResponse, error) {
	params, err := a.buildParams(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic: build params: %w", err)
	}

	resp, err := a.client.Messages.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("anthropic: messages.new: %w", err)
	}

	return a.parseResponse(resp), nil
}

// Stream sends a request and streams response chunks.
func (a *Adapter) Stream(ctx context.Context, req adapter.CompletionRequest) (<-chan adapter.StreamEvent, error) {
	params, err := a.buildParams(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic: build streaming params: %w", err)
	}

	ch := make(chan adapter.StreamEvent, 64)

	go func() {
		defer close(ch)

		stream := a.client.Messages.NewStreaming(ctx, params)
		var accumulated string
		var toolCallsInProgress map[int]*adapter.ToolCall

		for stream.Next() {
			event := stream.Current()

			switch e := event.AsUnion().(type) {
			case anthropicsdk.ContentBlockDeltaEvent:
				switch d := e.Delta.AsUnion().(type) {
				case anthropicsdk.TextDelta:
					accumulated += d.Text
					ch <- adapter.StreamEvent{
						Type:    "delta",
						Content: d.Text,
					}
				case anthropicsdk.InputJSONDelta:
					// Tool call input accumulating
					if toolCallsInProgress == nil {
						toolCallsInProgress = make(map[int]*adapter.ToolCall)
					}
					idx := int(e.Index)
					if tc, ok := toolCallsInProgress[idx]; ok {
						tc.Input["_raw"] = fmt.Sprintf("%v%s", tc.Input["_raw"], d.PartialJSON)
					}
				}

			case anthropicsdk.MessageStopEvent:
				// Emit final usage
				msg := stream.Message()
				usage := a.computeUsage(msg.Model, int(msg.Usage.InputTokens), int(msg.Usage.OutputTokens))
				ch <- adapter.StreamEvent{
					Type:       "done",
					Content:    accumulated,
					FinalUsage: &usage,
				}
			}
		}

		if err := stream.Err(); err != nil {
			if ctx.Err() == nil { // don't log cancellations
				log.Error().Err(err).Msg("anthropic: stream error")
			}
			ch <- adapter.StreamEvent{Type: "error", Error: err.Error()}
		}
	}()

	return ch, nil
}

// ── Helpers ───────────────────────────────────────────────────

func (a *Adapter) buildParams(req adapter.CompletionRequest) (anthropicsdk.MessageNewParams, error) {
	model := req.Model
	if model == "" {
		model = DefaultModel
	}

	maxTokens := int64(4096)
	if req.MaxTokens > 0 {
		maxTokens = int64(req.MaxTokens)
	}

	// Build messages
	var sdkMessages []anthropicsdk.MessageParam
	for _, m := range req.Messages {
		switch m.Role {
		case "user":
			if len(m.ToolResults) > 0 {
				// Tool results turn
				var parts []anthropicsdk.ContentBlockParamUnion
				for _, tr := range m.ToolResults {
					parts = append(parts, anthropicsdk.ToolResultBlockParam{
						Type:      anthropicsdk.F(anthropicsdk.ToolResultBlockParamTypeToolResult),
						ToolUseID: anthropicsdk.F(tr.ToolCallID),
						Content: anthropicsdk.F([]anthropicsdk.ToolResultBlockParamContentUnion{
							anthropicsdk.TextBlockParam{
								Type: anthropicsdk.F(anthropicsdk.TextBlockParamTypeText),
								Text: anthropicsdk.F(tr.Content),
							},
						}),
						IsError: anthropicsdk.F(tr.IsError),
					})
				}
				sdkMessages = append(sdkMessages, anthropicsdk.UserMessage(parts...))
			} else {
				sdkMessages = append(sdkMessages, anthropicsdk.NewUserMessage(
					anthropicsdk.NewTextBlock(m.Content),
				))
			}

		case "assistant":
			if len(m.ToolCalls) > 0 {
				var parts []anthropicsdk.ContentBlockParamUnion
				if m.Content != "" {
					parts = append(parts, anthropicsdk.NewTextBlock(m.Content))
				}
				for _, tc := range m.ToolCalls {
					inputJSON, _ := json.Marshal(tc.Input)
					parts = append(parts, anthropicsdk.ToolUseBlockParam{
						Type:  anthropicsdk.F(anthropicsdk.ToolUseBlockParamTypeToolUse),
						ID:    anthropicsdk.F(tc.ID),
						Name:  anthropicsdk.F(tc.Name),
						Input: anthropicsdk.Raw[interface{}](string(inputJSON)),
					})
				}
				sdkMessages = append(sdkMessages, anthropicsdk.NewAssistantMessage(parts...))
			} else {
				sdkMessages = append(sdkMessages, anthropicsdk.NewAssistantMessage(
					anthropicsdk.NewTextBlock(m.Content),
				))
			}
		}
	}

	params := anthropicsdk.MessageNewParams{
		Model:     anthropicsdk.F(anthropicsdk.Model(model)),
		MaxTokens: anthropicsdk.F(maxTokens),
		Messages:  anthropicsdk.F(sdkMessages),
	}

	if req.System != "" {
		params.System = anthropicsdk.F([]anthropicsdk.TextBlockParam{
			anthropicsdk.NewTextBlock(req.System),
		})
	}

	if len(req.Tools) > 0 {
		var sdkTools []anthropicsdk.ToolParam
		for _, t := range req.Tools {
			schemaJSON, _ := json.Marshal(t.InputSchema)
			sdkTools = append(sdkTools, anthropicsdk.ToolParam{
				Name:        anthropicsdk.F(t.Name),
				Description: anthropicsdk.F(t.Description),
				InputSchema: anthropicsdk.Raw[interface{}](string(schemaJSON)),
			})
		}
		params.Tools = anthropicsdk.F(sdkTools)
	}

	return params, nil
}

func (a *Adapter) parseResponse(resp *anthropicsdk.Message) *adapter.CompletionResponse {
	var content string
	var toolCalls []adapter.ToolCall

	for _, block := range resp.Content {
		switch b := block.AsUnion().(type) {
		case anthropicsdk.TextBlock:
			content += b.Text
		case anthropicsdk.ToolUseBlock:
			var input map[string]any
			if err := json.Unmarshal([]byte(b.Input.(string)), &input); err != nil {
				input = map[string]any{"_raw": b.Input}
			}
			toolCalls = append(toolCalls, adapter.ToolCall{
				ID:    b.ID,
				Name:  b.Name,
				Input: input,
			})
		}
	}

	usage := a.computeUsage(string(resp.Model), int(resp.Usage.InputTokens), int(resp.Usage.OutputTokens))

	return &adapter.CompletionResponse{
		ID:         resp.ID,
		Model:      string(resp.Model),
		Content:    content,
		ToolCalls:  toolCalls,
		StopReason: string(resp.StopReason),
		Usage:      usage,
	}
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
