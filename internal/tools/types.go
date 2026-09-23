package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/jrmarcco/goaw/internal/schema"
)

const (
	dirPerm  os.FileMode = 0o755 // 目录权限: rwxr-xr-x
	filePerm os.FileMode = 0o644 // 文件权限: rw-r--r--
)

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

// Registry 定义了工具的注册与分发执行接口。
type Registry interface {
	// Register 注册一个工具 ( 即挂载工具到系统 )。
	Register(tool Tool) error

	// GetAvailableTools 返回所有当前系统挂在的所有可用的工具。
	GetAvailableTools() []schema.ToolDefinition

	// Execute 执行一个工具调用并返回结果。
	Execute(ctx context.Context, call schema.ToolCall) schema.ToolCallResult
}

var _ Registry = (*DefaultRegistry)(nil)

// DefaultRegistry 默认的工具注册器实现。
type DefaultRegistry struct {
	mu    sync.RWMutex
	tools map[string]Tool
	order []string
}

func NewDefaultRegistry(initialTools ...Tool) *DefaultRegistry {
	registry := &DefaultRegistry{
		tools: make(map[string]Tool),
		order: make([]string, 0, len(initialTools)),
	}
	for _, tool := range initialTools {
		if err := registry.Register(tool); err != nil {
			slog.Error("[tool registry] 初始工具注册失败", "error", err)
		}
	}
	return registry
}

func (r *DefaultRegistry) Register(tool Tool) error {
	if tool == nil {
		return fmt.Errorf("工具不能为空")
	}
	name := strings.TrimSpace(tool.Name())
	if name == "" {
		return fmt.Errorf("工具名称不能为空")
	}
	if definitionName := tool.Definition().Name; definitionName != name {
		return fmt.Errorf("工具名称 %q 与定义名称 %q 不一致", name, definitionName)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[name]; exists {
		slog.Warn("[tool registry] 工具已经被注册，将执行覆盖操作。", "name", name)
	} else {
		r.order = append(r.order, name)
	}

	r.tools[name] = tool
	slog.Info("[tool registry] 工具注册成功。", "name", name)
	return nil
}

func (r *DefaultRegistry) GetAvailableTools() []schema.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tds := make([]schema.ToolDefinition, 0, len(r.order))
	for _, name := range r.order {
		tds = append(tds, r.tools[name].Definition())
	}
	return tds
}

func (r *DefaultRegistry) Execute(ctx context.Context, call schema.ToolCall) schema.ToolCallResult {
	// 路由查找。
	r.mu.RLock()
	tool, ok := r.tools[call.Name]
	r.mu.RUnlock()
	if !ok {
		// 找不到工具。
		// 这是因为模型产生了幻觉，直接跑出错误。
		return schema.ToolCallResult{
			ID:      call.ID,
			Output:  fmt.Sprintf("工具 %s 未注册", call.Name),
			IsError: true,
		}
	}

	// 执行工具。
	output, err := tool.Execute(ctx, call.Args)
	if err != nil {
		return schema.ToolCallResult{
			ID:      call.ID,
			Output:  fmt.Sprintf("工具 %s 执行失败: %v", call.Name, err),
			IsError: true,
		}
	}

	return schema.ToolCallResult{
		ID:      call.ID,
		Output:  output,
		IsError: false,
	}
}
