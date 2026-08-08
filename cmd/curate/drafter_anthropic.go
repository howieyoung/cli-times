package main

import (
	"context"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
)

const defaultAnthropicModel = "claude-haiku-4-5"

type anthropicDrafter struct{ model string }

func newAnthropicDrafter(model string) *anthropicDrafter {
	if model == "" {
		model = defaultAnthropicModel
	}
	return &anthropicDrafter{model: model}
}

func (d *anthropicDrafter) Name() string   { return "anthropic" }
func (d *anthropicDrafter) KeyEnv() string { return "ANTHROPIC_API_KEY" }

func (d *anthropicDrafter) Draft(ctx context.Context, system, user string, n int) (string, error) {
	client := anthropic.NewClient() // reads ANTHROPIC_API_KEY from the environment
	resp, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(d.model),
		MaxTokens: 8192,
		System:    []anthropic.TextBlockParam{{Text: system}},
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(user))},
	})
	if err != nil {
		return "", err
	}
	var text strings.Builder
	for _, block := range resp.Content {
		if b, ok := block.AsAny().(anthropic.TextBlock); ok {
			text.WriteString(b.Text)
		}
	}
	return text.String(), nil
}
