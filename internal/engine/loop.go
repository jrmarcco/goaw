package engine

import (
	"context"
	"fmt"

	"github.com/jrmarcco/goaw/internal/provider"
	"github.com/jrmarcco/goaw/internal/schema"
	"github.com/jrmarcco/goaw/internal/tools"
	"go.uber.org/zap"
)

type AgentEngine struct {
	WorkDir string // 工作区路径。

	provider provider.LLMProvider
	registry tools.Registry

	logger *zap.Logger
}

func NewAgentEngine(
	workDir string,
	llmProvider provider.LLMProvider,
	toolRegistry tools.Registry,
	logger *zap.Logger,
) (*AgentEngine, error) {
	return &AgentEngine{
		WorkDir: workDir,

		provider: llmProvider,
		registry: toolRegistry,

		logger: logger,
	}, nil
}

func (e *AgentEngine) Run(ctx context.Context, userPrompt string) error {
	e.logger.Info("[engine] start running, lock workspace", zap.String("workspace", e.WorkDir))

	// 1. 初始化会话 Context。
	contextHistory := []schema.Message{
		{
			Role:    schema.RoleSys,
			Content: "You are Goaw, an expert coding assistant. You have full access to tools in the workspace.",
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
		e.logger.Info("[engine] start turn", zap.Int("turn", turnCnt))

		// 获取工具。
		availableTools := e.registry.GetAvailableTools()

		// 发起推理请求。
		e.logger.Info("[engine] start reasoning ...")
		resp, err := e.provider.Generate(ctx, contextHistory, availableTools)
		if err != nil {
			return fmt.Errorf("failed to generate response: %w", err)
		}

		contextHistory = append(contextHistory, *resp)

		if resp.Content != "" {
			e.logger.Info("[engine] LLM response", zap.String("response", resp.Content))
		}

		if len(resp.ToolCalls) == 0 {
			e.logger.Info("[engine] no tool calls, end turn")
			break
		}

		e.logger.Info("[engine] start tool calls ...", zap.Int("tool_count", len(resp.ToolCalls)))
		for _, tc := range resp.ToolCalls {
			e.logger.Info("[engine] -> 🛠️ start tool call", zap.String("tool_name", tc.Name), zap.String("args", string(tc.Args)))

			res := e.registry.Exec(ctx, tc)
			if res.Error == "" {
				e.logger.Info("[engine] -> ✅ tool call success", zap.Int("return_bytes", len(res.Output)))
			} else {
				e.logger.Info("[engine] -> ❌ tool call failed", zap.String("error", res.Error))
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
