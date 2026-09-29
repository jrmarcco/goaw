package engine

import (
	"context"
	"fmt"
	"log/slog"

	icontext "github.com/jrmarcco/goaw/internal/context"
	"github.com/jrmarcco/goaw/internal/provider"
	"github.com/jrmarcco/goaw/internal/schema"
	"github.com/jrmarcco/goaw/internal/tools"
	"golang.org/x/sync/errgroup"
)

type AgentEngine struct {
	workspace string // 工作区路径。

	provider provider.LLMProvider
	registry tools.Registry

	composer *icontext.PromptComposer

	enableThinking bool // 是否启用思考。
}

const (
	// defaultContextWindow Compactor 使用的上下文窗口缺省值 ( token )。
	defaultContextWindow = 128_000

	// defaultReserveTokens 为模型单次补全预留的输出空间 ( token )。
	defaultReserveTokens = 8_192

	// defaultRetainLastMsg Working Memory 保护区的消息条数。
	defaultRetainLastMsg = 20
)

func NewAgentEngine(
	workspace string,
	llmProvider provider.LLMProvider,
	toolRegistry tools.Registry,
	enableThinking bool,
) (*AgentEngine, error) {
	if toolRegistry == nil {
		return nil, fmt.Errorf("tool registry is required")
	}

	skillLoader := icontext.NewSkillLoader(workspace)
	if err := toolRegistry.Register(tools.NewSkillReader(skillLoader)); err != nil {
		return nil, fmt.Errorf("failed to register skill reader: %w", err)
	}

	return &AgentEngine{
		workspace: workspace,

		provider: llmProvider,
		registry: toolRegistry,

		composer: icontext.NewPromptComposer(workspace, skillLoader),

		enableThinking: enableThinking,
	}, nil
}

func (e *AgentEngine) Run(ctx context.Context, userPrompt string, reporter Reporter) error {
	if err := checkCanceled(ctx); err != nil {
		return err
	}

	slog.Info("[engine] Agent 引擎启动, 锁定工作区", "workspace", e.workspace)
	slog.Info("[engine] 慢思考模式", "enabled", e.enableThinking)

	systemMessage, err := e.composer.Build()
	if err != nil {
		return fmt.Errorf("failed to build system message: %w", err)
	}

	contextHistory := []schema.Message{
		systemMessage,
		{Role: schema.RoleUser, Content: userPrompt},
	}

	// Compactor 跟踪的是单次会话的 Token 水位线。
	// 随 Run 独立创建，避免并发的会话互相污染校准状态。
	compactor := icontext.NewCompactor(defaultContextWindow, defaultReserveTokens, defaultRetainLastMsg)

	for turn := 1; ; turn++ {
		nextHistory, done, err := e.runTurn(ctx, contextHistory, reporter, turn, compactor)
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
	compactor *icontext.Compactor,
) ([]schema.Message, bool, error) {
	if err := checkCanceled(ctx); err != nil {
		return nil, false, err
	}
	slog.Info("[engine] start turn", "turn", turn)

	// 发送前先压缩: 用真实 Token 水位线决定是否拦截。
	contextHistory = compactor.Compact(contextHistory)

	if e.enableThinking {
		thinkGen, err := e.think(ctx, contextHistory, reporter)
		if err != nil {
			return nil, false, err
		}
		// 用真实消耗刷新水位线并校准估算系数。
		compactor.Observe(thinkGen.Usage.PromptTokens)
		if thinkGen.Message.Content != "" {
			contextHistory = append(contextHistory, thinkGen.Message)
		}
	}

	actGen, err := e.act(ctx, contextHistory, reporter)
	if err != nil {
		return nil, false, err
	}
	compactor.Observe(actGen.Usage.PromptTokens)
	contextHistory = append(contextHistory, actGen.Message)

	if len(actGen.Message.ToolCalls) == 0 {
		slog.Debug("[engine] 模型没有请求工具调用，任务结束。")
		return contextHistory, true, nil
	}

	observations, err := e.execToolCalls(ctx, actGen.Message.ToolCalls, reporter)
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
) (*schema.Generation, error) {
	slog.Debug("[engine] 剥夺工具访问权，强制进入慢思考与规划阶段...")
	if reporter != nil {
		_ = reporter.OnThinking(ctx)
	}

	thinkGen, err := e.provider.Generate(ctx, contextHistory, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to generate thinking response: %w", err)
	}
	if err := checkCanceled(ctx); err != nil {
		return nil, err
	}

	if thinkGen.Message.Content != "" {
		slog.Debug("[engine][内部思考] ->", "content", thinkGen.Message.Content)
	}
	return thinkGen, nil
}

// act 模型采取行动。
func (e *AgentEngine) act(
	ctx context.Context,
	contextHistory []schema.Message,
	reporter Reporter,
) (*schema.Generation, error) {
	slog.Debug("[engine] 恢复工具挂载，等待模型采取行动...")
	actGen, err := e.provider.Generate(ctx, contextHistory, e.registry.GetAvailableTools())
	if err != nil {
		return nil, fmt.Errorf("failed to generate action response: %w", err)
	}
	if err := checkCanceled(ctx); err != nil {
		return nil, err
	}

	if actGen.Message.Content != "" {
		slog.Debug("[engine][对外回复] ->", "content", actGen.Message.Content)
		if reporter != nil {
			_ = reporter.OnMessage(ctx, actGen.Message.Content)
		}
	}
	if err := checkCanceled(ctx); err != nil {
		return nil, err
	}
	return actGen, nil
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

func checkCanceled(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("agent run canceled: %w", err)
	}
	return nil
}
