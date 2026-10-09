package tools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/jrmarcco/goaw/internal/schema"
)

var _ Registry = (*DefaultRegistry)(nil)

// DefaultRegistry 默认的工具注册器实现。
type DefaultRegistry struct {
	mu sync.RWMutex

	tools map[string]Tool
	order []string

	middlewares []Middleware
}

func NewDefaultRegistry(initialTools ...Tool) *DefaultRegistry {
	registry := &DefaultRegistry{
		tools: make(map[string]Tool),
		order: make([]string, 0, len(initialTools)),

		middlewares: make([]Middleware, 0),
	}
	for _, tool := range initialTools {
		if err := registry.Register(tool); err != nil {
			slog.Error("[registry] 初始工具注册失败", "error", err)
		}
	}
	return registry
}

func (r *DefaultRegistry) Use(mws ...Middleware) {
	r.middlewares = append(r.middlewares, mws...)
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
		return fmt.Errorf("工具名称 [%q] 与定义名称 [%q] 不一致", name, definitionName)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[name]; exists {
		slog.Warn("[registry] 工具已经被注册，将执行覆盖操作。", "name", name)
	} else {
		r.order = append(r.order, name)
	}

	r.tools[name] = tool
	slog.Info("[registry] 工具注册成功。", "name", name)
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

func (r *DefaultRegistry) Execute(ctx context.Context, tc schema.ToolCall) schema.ToolCallResult {
	// 路由查找。
	r.mu.RLock()
	tool, ok := r.tools[tc.Name]
	r.mu.RUnlock()
	if !ok {
		// 找不到工具。
		// 这是因为模型产生了幻觉，直接跑出错误。
		return schema.ToolCallResult{
			ID:        tc.ID,
			Output:    fmt.Sprintf("工具 [%s] 未注册", tc.Name),
			IsError:   true,
			ErrorCode: schema.ErrCodeToolNotFound,
		}
	}

	// 依次执行 Middleware。
	for _, mw := range r.middlewares {
		allowed, reason := mw(ctx, tc)
		if !allowed {
			slog.Info("[registry] 工具调用被拦截", "tool_name", tc.Name, "reject_reason", reason)
			return schema.ToolCallResult{
				ID:        tc.ID,
				Output:    fmt.Sprintf("系统拦截工具调用，原因：%s", reason),
				IsError:   true,
				ErrorCode: schema.ErrCodeCallIntercepted,
			}
		}
	}

	// 执行工具。
	output, err := tool.Execute(ctx, tc.Args)
	if err != nil {
		// 从错误链中提取领域错误码 ( 见 ToolError )，随结果结构化传输，
		// 下游恢复层按码查表，不再对文案做字符串匹配。
		if toolErr, ok := errors.AsType[*ToolError](err); ok {
			// 软失败 ( Self-Correction 自愈机制 )：
			// 输出照常回传给模型自纠，不标记 IsError，仅随结果传输错误码。
			if toolErr.Soft {
				return schema.ToolCallResult{
					ID:        tc.ID,
					Output:    toolErr.Msg,
					IsError:   false,
					ErrorCode: toolErr.Code,
				}
			}
			// 硬错误：ToolError.Error() 会把错误码 token 渲染进文案，模型可直接看到。
			return schema.ToolCallResult{
				ID:        tc.ID,
				Output:    fmt.Sprintf("工具 [%s] 执行失败: %v", tc.Name, err),
				IsError:   true,
				ErrorCode: toolErr.Code,
			}
		}

		// 未分类的外来 error。
		return schema.ToolCallResult{
			ID:        tc.ID,
			Output:    fmt.Sprintf("工具 [%s] 执行失败: %v", tc.Name, err),
			IsError:   true,
			ErrorCode: schema.ErrCodeUnknown,
		}
	}

	return schema.ToolCallResult{
		ID:      tc.ID,
		Output:  output,
		IsError: false,
	}
}
