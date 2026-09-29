package provider

import (
	"context"

	"github.com/jrmarcco/goaw/internal/schema"
)

type LLMProvider interface {
	// Generate 返回的 Generation 除了消息本体，还携带本次调用的真实
	// Token 消耗 ( Usage )，供上层做上下文水位线管理。
	Generate(ctx context.Context, msgs []schema.Message, availableTools []schema.ToolDefinition) (*schema.Generation, error)
}
