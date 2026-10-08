package tools

import (
	"context"

	"github.com/jrmarcco/goaw/internal/schema"
)

// workspaceCtxKey 工作区在 context 中的键类型。
// 独立的私有结构体类型，保证不与其他包注入的 context 值冲突。
type workspaceCtxKey struct{}

// WithWorkspace 将工作区路径注入 context。
// 工具实例自身不持有工作区状态，执行时统一从 context 解析，
// 使同一组工具实例可以安全地服务于不同工作区的会话。
func WithWorkspace(ctx context.Context, workspace string) context.Context {
	return context.WithValue(ctx, workspaceCtxKey{}, workspace)
}

// WorkspaceFromContext 从 context 解析工作区路径。
// 工作区是工具执行的前提，缺失时直接返回错误。
func WorkspaceFromContext(ctx context.Context) (string, error) {
	workspace, _ := ctx.Value(workspaceCtxKey{}).(string)
	if workspace == "" {
		return "", newToolError(schema.ErrCodeNoWorkspace, nil, "执行上下文中缺少工作区")
	}
	return workspace, nil
}
