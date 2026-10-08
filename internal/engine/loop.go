package engine

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	icontext "github.com/jrmarcco/goaw/internal/context"
	"github.com/jrmarcco/goaw/internal/provider"
	"github.com/jrmarcco/goaw/internal/schema"
	"github.com/jrmarcco/goaw/internal/tools"
	"golang.org/x/sync/errgroup"
)

// AgentEngine 是与工作区无关的 Agent 执行引擎。
// 单次运行的全部环境 ( 工作区路径 ) 来自 Session，
// 经由 context 流向 System Prompt 构建与所有工具调用。
type AgentEngine struct {
	provider provider.LLMProvider
	registry tools.Registry

	thinkMode bool

	recovery *icontext.RecoveryManager
}

func NewAgentEngine(
	llmProvider provider.LLMProvider,
	toolRegistry tools.Registry,
	thinkMode bool,
) (*AgentEngine, error) {
	if toolRegistry == nil {
		return nil, fmt.Errorf("tool registry is required")
	}

	if err := toolRegistry.Register(tools.NewSkillReader()); err != nil {
		return nil, fmt.Errorf("failed to register skill reader: %w", err)
	}

	return &AgentEngine{
		provider: llmProvider,
		registry: toolRegistry,

		thinkMode: thinkMode,

		recovery: icontext.NewRecoveryManager(),
	}, nil
}

// Run 在指定会话上执行一次 Agent 运行。
// 会话承载运行所需的全部环境与状态: Workspace 决定工具的执行范围和
// System Prompt 的内容，history 跨 Run 持久累积，同会话的多次运行共享上下文。
func (e *AgentEngine) Run(ctx context.Context, sess *Session, reporter Reporter) error {
	if err := checkCanceled(ctx); err != nil {
		return err
	}
	if sess == nil {
		return fmt.Errorf("session is required")
	}
	if sess.Workspace == "" {
		return fmt.Errorf("session workspace is required")
	}
	if !sess.TryStartRun() {
		return fmt.Errorf("session %q is busy: another agent run is in progress", sess.ID)
	}
	defer sess.EndRun()

	// 工作区经由 context 流向所有工具调用。
	ctx = tools.WithWorkspace(ctx, sess.Workspace)

	slog.Info("[engine] Agent 引擎启动", "session", sess.ID, "workspace", sess.Workspace)
	slog.Info("[engine] 慢思考模式", "enabled", e.thinkMode)

	// System Prompt 每次 Run 现场构建，不写入会话历史，
	// 确保 AGENTS.md 与技能索引始终反映工作区的最新状态。
	systemMessage, err := icontext.NewPromptComposer(sess.Workspace, sess.PlanMode).Build()
	if err != nil {
		return fmt.Errorf("failed to build system message: %w", err)
	}

	for turn := 1; ; turn++ {
		done, err := e.runTurn(ctx, sess, systemMessage, reporter, turn)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
	}
}

func (e *AgentEngine) runTurn(
	ctx context.Context,
	sess *Session,
	systemMessage schema.Message,
	reporter Reporter,
	turn int,
) (bool, error) {
	if err := checkCanceled(ctx); err != nil {
		return false, err
	}
	slog.Info("[engine] start turn", "turn", turn)

	var currThinkingContent string
	if e.thinkMode {
		thinkGen, err := e.think(ctx, e.buildRequestHistory(systemMessage, sess), reporter)
		if err != nil {
			return false, err
		}
		// 用真实消耗刷新水位线并校准估算系数。
		sess.compactor.Observe(thinkGen.Usage.PromptTokens)
		if thinkGen.Message.Content != "" {
			currThinkingContent = thinkGen.Message.Content
		}
	}

	actGen, err := e.act(ctx, e.buildRequestHistory(systemMessage, sess), reporter)
	if err != nil {
		return false, err
	}
	sess.compactor.Observe(actGen.Usage.PromptTokens)

	// 合并为单条 Assistant Message。
	finalAssistantMsg := schema.Message{
		Role:      schema.RoleAssistant,
		Content:   strings.TrimSpace(currThinkingContent + "\n" + actGen.Message.Content),
		ToolCalls: actGen.Message.ToolCalls,
	}
	sess.Append(finalAssistantMsg)

	if len(actGen.Message.ToolCalls) == 0 {
		slog.Debug("[engine] 模型没有请求工具调用，任务结束。")
		return true, nil
	}

	observations, err := e.execToolCalls(ctx, actGen.Message.ToolCalls, reporter)
	if err != nil {
		return false, err
	}
	sess.Append(observations...)
	return false, nil
}

// buildRequestHistory 组装一次模型调用的请求上下文:
//
//	System Prompt + 会话工作记忆，再经会话级 Compactor 自适应压缩。
//	工作记忆暂不设条数与 Token 预算，
//	上下文压力统一交给 Compactor 基于真实 Token 水位线处理，
//	避免硬截断丢弃 Compactor 本可仅掩码的内容。
func (e *AgentEngine) buildRequestHistory(systemMessage schema.Message, sess *Session) []schema.Message {
	workingMemory := sess.GetWorkingMemory(0)
	history := make([]schema.Message, 0, len(workingMemory)+1)
	history = append(history, systemMessage)
	history = append(history, workingMemory...)
	return sess.compactor.Compact(history)
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
	gidx int,
) (schema.Message, error) {
	if err := checkCanceled(ctx); err != nil {
		return schema.Message{}, err
	}

	slog.Info(
		"[engine] -> 并发执行工具调用",
		"goroutine_index", gidx,
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

	finalOutput := result.Output
	if result.IsError {
		// 发生错误时，按领域错误码注入救援指南。
		finalOutput = e.recovery.AnalyzeAndInject(toolCall.Name, result.ErrorCode, result.Output)
		slog.Error(
			"[engine] -> 工具调用失败，注入救援指南。",
			"goroutine_index", gidx,
			"tool_name", toolCall.Name,
			"error_code", result.ErrorCode,
			"recovery_hint", finalOutput,
		)
	} else if result.ErrorCode != "" {
		// 软失败 ( 如 bash 命令失败 )：输出照常回传模型自纠，仅按错误码注入救援指南。
		finalOutput = e.recovery.AnalyzeAndInject(toolCall.Name, result.ErrorCode, result.Output)
		slog.Warn(
			"[engine] -> 工具软失败，注入救援指南。",
			"goroutine_index", gidx,
			"tool_name", toolCall.Name,
			"error_code", result.ErrorCode,
			"recovery_hint", finalOutput,
		)
	}

	if reporter != nil {
		_ = reporter.OnToolCallResult(ctx, toolCall.Name, finalOutput, result.IsError)
	}
	if err := checkCanceled(ctx); err != nil {
		return schema.Message{}, err
	}

	return schema.Message{
		Role:       schema.RoleUser,
		Content:    finalOutput,
		ToolCallID: toolCall.ID,
	}, nil
}

func checkCanceled(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("agent run canceled: %w", err)
	}
	return nil
}
