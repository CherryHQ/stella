package providers

import (
	"github.com/vaayne/anna/internal/ai"
	"github.com/vaayne/anna/internal/ai/providers/anthropic"
	"github.com/vaayne/anna/internal/ai/providers/openai"
	openairesponse "github.com/vaayne/anna/internal/ai/providers/openai-response"
)

// RegisterBuiltins registers first-party providers in a registry.
func RegisterBuiltins(r *ai.Registry) {
	r.Register(openai.New(openai.Config{}))
	r.Register(openairesponse.New(openairesponse.Config{}))
	r.Register(anthropic.New(anthropic.Config{}))
}
