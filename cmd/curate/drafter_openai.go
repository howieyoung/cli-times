package main

import (
	"context"
	"fmt"

	"github.com/openai/openai-go/v3"
)

// defaultOpenAIModel is a stable, cheap, JSON-reliable default. Override with
// -model (e.g. gpt-5-mini) if you have access to a newer one.
const defaultOpenAIModel = "gpt-4o-mini"

type openAIDrafter struct{ model string }

func newOpenAIDrafter(model string) *openAIDrafter {
	if model == "" {
		model = defaultOpenAIModel
	}
	return &openAIDrafter{model: model}
}

func (d *openAIDrafter) Name() string   { return "openai" }
func (d *openAIDrafter) KeyEnv() string { return "OPENAI_API_KEY" }

func (d *openAIDrafter) Draft(ctx context.Context, system, user string, n int) (string, error) {
	client := openai.NewClient() // reads OPENAI_API_KEY from the environment

	// JSON-object response format guarantees syntactically valid JSON; the prompt
	// asks for {"items":[...]} and extractJSONArray pulls the inner array back out.
	resp, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model: openai.ChatModel(d.model),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(system),
			openai.UserMessage(user),
		},
		MaxCompletionTokens: openai.Int(8192),
		ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONObject: &openai.ResponseFormatJSONObjectParam{},
		},
	})
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("openai: empty response (no choices)")
	}
	return resp.Choices[0].Message.Content, nil
}
