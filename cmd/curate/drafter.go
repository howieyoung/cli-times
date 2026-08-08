package main

import (
	"context"
	"fmt"
)

// Drafter is the ONLY provider-specific seam in the pipeline. Everything that
// matters for safety — sanitize, urlSafe, flagReason, the human review gate, and
// Ed25519 signing — runs on the Drafter's OUTPUT, identically, no matter which
// model produced it. So the model is an untrusted, swappable component: changing
// providers changes nothing a CLI reader ever sees (same feed format, same signing
// key, same binary). This is why we can move from Anthropic to OpenAI freely.
type Drafter interface {
	Name() string   // "openai" | "anthropic" (for logs)
	KeyEnv() string // env var that must hold this provider's API key
	// Draft sends the editorial system+user prompt and returns the model's raw
	// text (expected to contain the JSON items). n is the requested line count;
	// a provider may use it to size the output. Parsing/validation/flagging are
	// done by the caller (parseAndFlag), NOT here — keep providers thin.
	Draft(ctx context.Context, system, user string, n int) (string, error)
}

// newDrafter builds the requested provider. OpenAI is the default (that is where
// our ongoing quota is); anthropic remains available as a fallback. An empty
// model means "use this provider's sensible default".
func newDrafter(provider, model string) (Drafter, error) {
	switch provider {
	case "openai", "":
		return newOpenAIDrafter(model), nil
	case "anthropic":
		return newAnthropicDrafter(model), nil
	default:
		return nil, fmt.Errorf("unknown -provider %q (want openai|anthropic)", provider)
	}
}
