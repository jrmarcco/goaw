package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/jrmarcco/goaw/internal/logging"
	"github.com/jrmarcco/goaw/internal/obs"
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
			logging.For("registry").Error("初始工具注册失败", "error", err)
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
		logging.For("registry").Warn("工具重复注册，执行覆盖", "name", name)
	} else {
		r.order = append(r.order, name)
	}

	r.tools[name] = tool
	logging.For("registry").Debug("工具注册成功", "name", name)
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

// Execute 路由并执行一次工具调用，外围建立 tool.execute 追踪叶子
// ( 审批等待等信息经属性汇入本叶子，见 ApprovalManager.Middleware )。
func (r *DefaultRegistry) Execute(ctx context.Context, tc schema.ToolCall) schema.ToolCallResult {
	_, span := obs.StartSpan(ctx, "tool.execute")
	defer span.End()
	span.Attribute("tool_name", tc.Name)

	result := r.execute(ctx, tc)
	span.Attribute("is_error", result.IsError)
	if result.ErrorCode != "" {
		span.Attribute("error_code", result.ErrorCode)
	}
	return result
}

// execute 工具调用的实际执行体: 中间件链拦截 → 工具本体执行。
func (r *DefaultRegistry) execute(ctx context.Context, tc schema.ToolCall) schema.ToolCallResult {
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
		approved, reason := mw(ctx, tc)
		if !approved {
			logging.For("registry").Debug("工具调用被拦截", "tool_name", tc.Name, "reason", reason)
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
