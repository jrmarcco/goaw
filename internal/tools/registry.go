package tools

import (
	"context"

	"github.com/jrmarcco/goaw/internal/schema"
)

// Registry 定义了工具的注册与分发执行接口。
type Registry interface {
	// GetAvailableTools 返回所有当前系统挂在的所有可用的工具。
	GetAvailableTools() []schema.ToolDef

	// Exec 执行一个工具调用并返回结果。
	Exec(ctx context.Context, call schema.ToolCall) *schema.ToolCallRes
}
