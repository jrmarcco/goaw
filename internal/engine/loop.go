package engine

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jrmarcco/goaw/internal/provider"
	"github.com/jrmarcco/goaw/internal/schema"
	"github.com/jrmarcco/goaw/internal/tools"
	"golang.org/x/sync/errgroup"
)

type AgentEngine struct {
	Workspace string // 工作区路径。

	provider provider.LLMProvider
	registry tools.Registry

	enableThinking bool // 是否启用思考。
}

func NewAgentEngine(
	workspace string,
	llmProvider provider.LLMProvider,
	toolRegistry tools.Registry,
	enableThinking bool,
) (*AgentEngine, error) {
	return &AgentEngine{
		Workspace: workspace,

		provider: llmProvider,
		registry: toolRegistry,

		enableThinking: enableThinking,
	}, nil
}

func (e *AgentEngine) Run(ctx context.Context, userPrompt string, reporter Reporter) error {
	if err := checkCanceled(ctx); err != nil {
		return err
	}

	slog.Info("[engine] Agent 引擎启动, 锁定工作区", "workspace", e.Workspace)
	slog.Info("[engine] 慢思考模式", "enabled", e.enableThinking)

	contextHistory := initContext(userPrompt)
	for turn := 1; ; turn++ {
		nextHistory, done, err := e.runTurn(ctx, contextHistory, reporter, turn)
		if err != nil {
			return err
		}
		contextHistory = nextHistory
		if done {
			return nil
		}
	}
}

func (e *AgentEngine) runTurn(
	ctx context.Context,
	contextHistory []schema.Message,
	reporter Reporter,
	turn int,
) ([]schema.Message, bool, error) {
	if err := checkCanceled(ctx); err != nil {
		return nil, false, err
	}
	slog.Info("[engine] start turn", "turn", turn)

	contextHistory, err := e.think(ctx, contextHistory, reporter)
	if err != nil {
		return nil, false, err
	}

	actionResp, err := e.act(ctx, contextHistory, reporter)
	if err != nil {
		return nil, false, err
	}
	contextHistory = append(contextHistory, *actionResp)

	if len(actionResp.ToolCalls) == 0 {
		slog.Debug("[engine] 模型没有请求工具调用，任务结束。")
		return contextHistory, true, nil
	}

	observations, err := e.execToolCalls(ctx, actionResp.ToolCalls, reporter)
	if err != nil {
		return nil, false, err
	}
	return append(contextHistory, observations...), false, nil
}

// think 模型进行思考。
func (e *AgentEngine) think(
	ctx context.Context,
	contextHistory []schema.Message,
	reporter Reporter,
) ([]schema.Message, error) {
	if !e.enableThinking {
		return contextHistory, nil
	}

	slog.Debug("[engine] 剥夺工具访问权，强制进入慢思考与规划阶段...")
	if reporter != nil {
		_ = reporter.OnThinking(ctx)
	}

	thinkResp, err := e.provider.Generate(ctx, contextHistory, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to generate thinking response: %w", err)
	}
	if err := checkCanceled(ctx); err != nil {
		return nil, err
	}
	if thinkResp.Content == "" {
		return contextHistory, nil
	}

	slog.Debug("[engine][内部思考] ->", "content", thinkResp.Content)
	return append(contextHistory, *thinkResp), nil
}

// act 模型采取行动。
func (e *AgentEngine) act(
	ctx context.Context,
	contextHistory []schema.Message,
	reporter Reporter,
) (*schema.Message, error) {
	slog.Debug("[engine] 恢复工具挂载，等待模型采取行动...")
	actionResp, err := e.provider.Generate(ctx, contextHistory, e.registry.GetAvailableTools())
	if err != nil {
		return nil, fmt.Errorf("failed to generate action response: %w", err)
	}
	if err := checkCanceled(ctx); err != nil {
		return nil, err
	}

	if actionResp.Content != "" {
		slog.Debug("[engine][对外回复] ->", "content", actionResp.Content)
		if reporter != nil {
			_ = reporter.OnMessage(ctx, actionResp.Content)
		}
	}
	if err := checkCanceled(ctx); err != nil {
		return nil, err
	}
	return actionResp, nil
}

// execToolCalls 并发执行工具调用。
func (e *AgentEngine) execToolCalls(
	ctx context.Context,
	toolCalls []schema.ToolCall,
	reporter Reporter,
) ([]schema.Message, error) {
	slog.Info("[engine] 模型请求并发执行工具调用...", "tool_count", len(toolCalls))

	observations := make([]schema.Message, len(toolCalls))
	errGroup, groupCtx := errgroup.WithContext(ctx)
	for idx, toolCall := range toolCalls {
		errGroup.Go(func() error {
			observation, err := e.execToolCall(groupCtx, toolCall, reporter, idx)
			if err != nil {
				return err
			}
			observations[idx] = observation
			return nil
		})
	}

	if err := errGroup.Wait(); err != nil {
		return nil, err
	}
	if err := checkCanceled(ctx); err != nil {
		return nil, err
	}
	return observations, nil
}

// execToolCall 工具调用执行逻辑。
func (e *AgentEngine) execToolCall(
	ctx context.Context,
	toolCall schema.ToolCall,
	reporter Reporter,
	index int,
) (schema.Message, error) {
	if err := checkCanceled(ctx); err != nil {
		return schema.Message{}, err
	}

	slog.Info(
		"[engine] -> 并发执行工具调用",
		"goroutine_index", index,
		"tool_name", toolCall.Name,
		"args", string(toolCall.Args),
	)
	if reporter != nil {
		_ = reporter.OnToolCall(ctx, toolCall.Name, string(toolCall.Args))
	}
	if err := checkCanceled(ctx); err != nil {
		return schema.Message{}, err
	}

	result := e.registry.Execute(ctx, toolCall)
	if err := checkCanceled(ctx); err != nil {
		return schema.Message{}, err
	}
	if reporter != nil {
		_ = reporter.OnToolCallResult(ctx, toolCall.Name, result.Output, result.IsError)
	}
	if err := checkCanceled(ctx); err != nil {
		return schema.Message{}, err
	}

	return schema.Message{
		Role:       schema.RoleUser,
		Content:    result.Output,
		ToolCallID: toolCall.ID,
	}, nil
}

func initContext(userPrompt string) []schema.Message {
	return []schema.Message{
		{
			Role:    schema.RoleSystem,
			Content: "You are Goaw, an expert coding assistant. You have full access to tools in the workspace.",
		},
		{
			Role:    schema.RoleSystem,
			Content: "I need you anwser in Chinese.",
		},
		{
			Role:    schema.RoleUser,
			Content: userPrompt,
		},
	}
}

func checkCanceled(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("agent run canceled: %w", err)
	}
	return nil
}
