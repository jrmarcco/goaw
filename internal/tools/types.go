package tools

import (
	"context"
	"encoding/json"
	"os"

	"github.com/jrmarcco/goaw/internal/schema"
)

const (
	dirPerm  os.FileMode = 0o755 // 目录权限: rwxr-xr-x
	filePerm os.FileMode = 0o644 // 文件权限: rw-r--r--
)

// Middleware 是工具执行中间件。
type Middleware func(ctx context.Context, tc schema.ToolCall) (approved bool, rejectReason string)

// Registry 定义了工具的注册与分发执行接口。
type Registry interface {
	// Use 注册 Middleware。
	Use(mws ...Middleware)

	// Register 注册一个工具 ( 即挂载工具到系统 )。
	Register(tool Tool) error

	// GetAvailableTools 返回所有当前系统挂在的所有可用的工具。
	GetAvailableTools() []schema.ToolDefinition

	// Execute 执行一个工具调用并返回结果。
	Execute(ctx context.Context, tc schema.ToolCall) schema.ToolCallResult
}

// Tool 工具的通用接口。
type Tool interface {
	// Name 返回工具的全局唯一名称 ( 模型通过 Name 调用工具 )。
	Name() string

	// Definition 返回工具的定义。
	// 包含提交给模型的工具元数据和参数 JSON Schema。
	Definition() schema.ToolDefinition

	// Execute 执行工具并返回结果 ( 参数由模型传入 )。
	// 注意：
	//	参数是 json.RawMessage，反序列化工作用具具体实现决定。
	Execute(ctx context.Context, args json.RawMessage) (string, error)
}
