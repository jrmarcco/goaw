package engine

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jrmarcco/goaw/internal/provider"
	"github.com/jrmarcco/goaw/internal/schema"
	"github.com/jrmarcco/goaw/internal/tools"
)

type AgentEngine struct {
	WorkDir string // 工作区路径。

	provider provider.LLMProvider
	registry tools.Registry

	enableThinking bool // 是否启用思考。
}

func NewAgentEngine(
	workDir string,
	llmProvider provider.LLMProvider,
	toolRegistry tools.Registry,
	enableThinking bool,
) (*AgentEngine, error) {
	return &AgentEngine{
		WorkDir: workDir,

		provider: llmProvider,
		registry: toolRegistry,

		enableThinking: enableThinking,
	}, nil
}

func (e *AgentEngine) Run(ctx context.Context, userPrompt string) error {
	slog.Info("[engine] Agent 引擎启动, 锁定工作区", "workspace", e.WorkDir)
	slog.Info("[engine] 慢思考模式 ( Thinking Phase )", "enabled", e.enableThinking)

	// 1. 初始化会话 Context。
	contextHistory := []schema.Message{
		{
			Role:    schema.RoleSys,
			Content: "You are Goaw, an expert coding assistant. You have full access to tools in the workspace.",
		},
		{
			Role:    schema.RoleSys,
			Content: "I need you anwser in Chinese.",
		},
		{
			Role:    schema.RoleUser,
			Content: userPrompt,
		},
	}

	turnCnt := 0

	// 2. 开始主循环 ( 标准的 ReAct 循环 )。
	for {
		turnCnt++
		slog.Info("[engine] start turn", "turn", turnCnt)

		// 2.1 慢思考阶段 ( 剥夺工具强制规划 )
		if e.enableThinking {
			slog.Debug("[engine] 剥夺工具访问权，强制进入慢思考与规划阶段...")

			// 传入的 availableTools 为 nil。
			thinkResp, err := e.provider.Generate(ctx, contextHistory, nil)
			if err != nil {
				return fmt.Errorf("failed to generate thinking response: %w", err)
			}

			if thinkResp.Content != "" {
				slog.Debug("[engine] 🧠 [内部思考 Trace] ->", "content", thinkResp.Content)
				contextHistory = append(contextHistory, *thinkResp)
			}
		}

		// 2.2 行动阶段 ( Action )，恢复工具调用。
		slog.Debug("[engine] 恢复工具挂载，等待模型采取行动...")
		// 获取工具。
		availableTools := e.registry.GetAvailableTools()
		actionResp, err := e.provider.Generate(ctx, contextHistory, availableTools)
		if err != nil {
			return fmt.Errorf("failed to generate action response: %w", err)
		}

		if actionResp.Content != "" {
			slog.Debug("[engine] 🤖 [对外回复] ->", "content", actionResp.Content)
		}

		contextHistory = append(contextHistory, *actionResp)

		if len(actionResp.ToolCalls) == 0 {
			slog.Debug("[engine] 没有工具调用，结束回合")
			break
		}

		slog.Info("[engine] 模型请求工具调用...", "tool_count", len(actionResp.ToolCalls))
		for _, tc := range actionResp.ToolCalls {
			slog.Info("[engine] -> 🛠️ 工具调用", "tool_name", tc.Name, "args", string(tc.Args))

			res := e.registry.Exec(ctx, tc)
			if res.Error == "" {
				slog.Info("[engine] -> ✅ 工具调用成功", "return_bytes", len(res.Output))
			} else {
				slog.Info("[engine] -> ❌ 工具调用错误", "error", res.Error)
			}

			obsMsg := schema.Message{
				Role:       schema.RoleUser,
				Content:    res.Output,
				ToolCallID: tc.ID,
			}
			contextHistory = append(contextHistory, obsMsg)
		}

	}
	return nil
}
