package provider

import (
	"context"

	"github.com/jrmarcco/goaw/internal/schema"
)

type LLMProvider interface {
	Generate(ctx context.Context, msgs []schema.Message, availableTools []schema.ToolDef) (*schema.Message, error)
}
