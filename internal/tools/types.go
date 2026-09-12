package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jrmarcco/goaw/internal/schema"
)

// Tool 工具的通用接口。
type Tool interface {
	// Name 返回工具的全局唯一名称 ( 大模型通过 Name 调用工具 )。
	Name() string

	// Definition 返回工具的定义。
	// 包含提交给大模型的工具元数据和参数 JSON Schema。
	Definition() schema.ToolDef

	// Execute 执行工具并返回结果 ( 参数由大模型传入 )。
	// 注意：
	//	参数是 json.RawMessage，反序列化工作用具具体实现决定。
	Execute(ctx context.Context, args json.RawMessage) (string, error)
}

// Registry 定义了工具的注册与分发执行接口。
type Registry interface {
	// Register 注册一个工具 ( 即挂载工具到系统 )。
	Register(tool Tool) error

	// GetAvailableTools 返回所有当前系统挂在的所有可用的工具。
	GetAvailableTools() []schema.ToolDef

	// Execute 执行一个工具调用并返回结果。
	Execute(ctx context.Context, call schema.ToolCall) schema.ToolCallRes
}

var _ Registry = (*DefaultRegistry)(nil)

// DefaultRegistry 默认的工具注册器实现。
type DefaultRegistry struct {
	tools map[string]Tool
}

func NewDefaultRegistry() *DefaultRegistry {
	return &DefaultRegistry{
		tools: make(map[string]Tool),
	}
}

func (r *DefaultRegistry) Register(tool Tool) error {
	name := tool.Name()
	if _, exists := r.tools[name]; exists {
		slog.Warn("[tool registry] 工具已经被注册，将执行覆盖操作。", "name", name)
	}

	r.tools[name] = tool
	slog.Info("[tool registry] 工具注册成功。", "name", name)
	return nil
}

func (r *DefaultRegistry) GetAvailableTools() []schema.ToolDef {
	tds := make([]schema.ToolDef, 0, len(r.tools))
	for _, tool := range r.tools {
		tds = append(tds, tool.Definition())
	}
	return tds
}

func (r *DefaultRegistry) Execute(ctx context.Context, call schema.ToolCall) schema.ToolCallRes {
	// 路由查找。
	tool, ok := r.tools[call.Name]
	if !ok {
		// 找不到工具。
		// 这是因为模型产生了幻觉，直接跑出错误。
		return schema.ToolCallRes{
			ID:      call.ID,
			Output:  fmt.Sprintf("工具 %s 未注册", call.Name),
			IsError: true,
		}
	}

	// 执行工具。
	output, err := tool.Execute(ctx, call.Args)
	if err != nil {
		return schema.ToolCallRes{
			ID:      call.ID,
			Output:  fmt.Sprintf("工具 %s 执行失败: %v", call.Name, err),
			IsError: true,
		}
	}

	return schema.ToolCallRes{
		ID:      call.ID,
		Output:  output,
		IsError: false,
	}
}
