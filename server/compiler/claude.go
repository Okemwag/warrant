package compiler

import (
	"context"
	"errors"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// DefaultModel is used when WARRANT_LLM_MODEL is unset.
const DefaultModel = "claude-opus-5-5"

// Claude calls the Claude API with structured outputs.
type Claude struct {
	client anthropic.Client
	model  string
}

// NewClaude returns a Claude LLM. apiKey must be set.
func NewClaude(apiKey, model string) *Claude {
	if model == "" {
		model = DefaultModel
	}
	return &Claude{client: anthropic.NewClient(option.WithAPIKey(apiKey)), model: model}
}

// Complete returns the model's JSON output for the schema.
func (c *Claude) Complete(ctx context.Context, system, user string, schema map[string]any) ([]byte, error) {
	msg, err := c.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(c.model),
		MaxTokens: 4000,
		System:    []anthropic.TextBlockParam{{Text: system}},
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(user))},
		OutputConfig: anthropic.OutputConfigParam{
			// Extraction is simple; low effort keeps it fast.
			Effort: anthropic.OutputConfigEffortLow,
			Format: anthropic.JSONOutputFormatParam{Schema: schema},
		},
	},
		// If a safety classifier declines, let the API retry on its
		// recommended fallback model instead of failing.
		option.WithHeaderAdd("anthropic-beta", "server-side-fallback-2026-07-01"),
		option.WithJSONSet("fallbacks", "default"),
	)
	if err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			return nil, fmt.Errorf("compiler: claude api %d: %w", apiErr.StatusCode, err)
		}
		return nil, fmt.Errorf("compiler: claude api: %w", err)
	}
	if msg.StopReason == anthropic.StopReasonRefusal {
		return nil, ErrRefused
	}
	if msg.StopReason == anthropic.StopReasonMaxTokens {
		return nil, errors.New("compiler: model output was cut off")
	}
	for _, block := range msg.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			return []byte(t.Text), nil
		}
	}
	return nil, errors.New("compiler: model returned no text")
}
