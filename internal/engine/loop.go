package engine

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	icontext "github.com/jrmarcco/goaw/internal/context"
	"github.com/jrmarcco/goaw/internal/provider"
	"github.com/jrmarcco/goaw/internal/schema"
	"github.com/jrmarcco/goaw/internal/tools"
	"github.com/jrmarcco/jit/xbean/option"
	"golang.org/x/sync/errgroup"
)

// CostObserver 是成本观测器规范 ( 结构化类型匹配，实现方无需依赖本包 )。
//
// 方法签名必须与本接口完全一致:
// obs.CostTracker 等外部实现靠签名逐个比对隐式满足接口，任何偏差都会失去匹配，
// 与 tools.SubReporter / engine.Reporter 的循环解法同构。
type CostObserver interface {
	// Observe 每次模型调用 ( think/act/final ) 后上报真实消耗与耗时。
	Observe(usage schema.Usage, phase string, took time.Duration)

	// Exceeded 返回 true 表示 token 预算已耗尽，
	// 引擎在轮次边界消费该信号并强制收尾。
	Exceeded() bool
}

// 模型调用阶段标记 ( Observe 的 phase 参数取值 )。
const (
	PhaseThink = "think" // 慢思考补全。
	PhaseAct   = "act"   // 行动补全。
	PhaseFinal = "final" // 预算耗尽后的强制收尾补全。
)

// forcedFinalTurnsReminder 轮次预算耗尽时注入的收尾指令。
const forcedFinalTurnsReminder = "工具调用预算已耗尽。请立即停止调用工具，基于已收集的信息输出最终汇报；信息不足的部分如实说明未确认。"

// forcedFinalTokensReminder token 预算耗尽时注入的收尾指令。
//
//nolint:gosec // G101 误报: 提示词文案包含 "Token" 字样被误判为硬编码凭证，并非密钥。
const forcedFinalTokensReminder = "Token 预算已耗尽。请立即停止调用工具，基于已收集的信息输出最终汇报；信息不足的部分如实说明未确认。"

// RunOption 是 AgentEngine.Run 的运行级选项。
// 选项作用于单次运行 ( 而非引擎构造 )，同一引擎可承载不同角色的运行。
type RunOption func(*runConfig)

// runConfig Run 的运行级配置，由 RunOption 填充。
type runConfig struct {
	// systemComposer 覆盖缺省的 System Prompt 组装，角色化 SubAgent 经由它注入身份。
	systemComposer func(workspace string) (schema.Message, error)

	// maxTurns 轮次预算，小于等于 0 表示不设限。
	maxTurns int

	// cost 成本观测器 ( 引擎构造级继承，系统级策略 )。
	cost CostObserver

	// 以下为 run 本地成本累计，Run 主循环单 goroutine 串行更新，无需加锁。
	promptTokens     int
	completionTokens int
	calls            int
}

// buildSystemMessage 组装本次运行的 System Prompt。
func (c *runConfig) buildSystemMessage(sess *Session) (schema.Message, error) {
	if c.systemComposer != nil {
		return c.systemComposer(sess.Workspace)
	}

	// 缺省组装: System Prompt 每次 Run 现场构建，不写入会话历史，
	// 确保 AGENTS.md 与技能索引始终反映工作区的最新状态。
	systemMessage, err := icontext.NewPromptComposer(sess.Workspace, sess.PlanMode).Build()
	if err != nil {
		return schema.Message{}, fmt.Errorf("failed to build system message: %w", err)
	}
	return systemMessage, nil
}

// exhausted 判定轮次预算是否耗尽。
func (c *runConfig) exhausted(turn int) bool {
	return c.maxTurns > 0 && turn > c.maxTurns
}

// observe 上报一次模型调用成本并累计 run 本地统计 ( 观测器缺省时仅本地累计 )。
func (c *runConfig) observe(usage schema.Usage, phase string, took time.Duration) {
	c.calls++
	c.promptTokens += usage.PromptTokens
	c.completionTokens += usage.CompletionTokens
	if c.cost != nil {
		c.cost.Observe(usage, phase, took)
	}
}

// forcedReminder 判定任一预算是否在轮次边界触发强制收尾，返回对应的收尾指令。
// token 预算优先: 系统级口径比单次运行的轮次预算更重要。
func (c *runConfig) forcedReminder(turn int) (string, bool) {
	if c.cost != nil && c.cost.Exceeded() {
		return forcedFinalTokensReminder, true
	}
	if c.exhausted(turn) {
		return forcedFinalTurnsReminder, true
	}
	return "", false
}

// WithSystemComposer 以自定义组装器替换缺省的 PromptComposer。
func WithSystemComposer(composer func(workspace string) (schema.Message, error)) RunOption {
	return func(cfg *runConfig) {
		cfg.systemComposer = composer
	}
}

// WithMaxTurns 设置轮次预算；耗尽后引擎剥夺工具挂载强制收尾 ( 见 forceFinalTurn )。
func WithMaxTurns(maxTurns int) RunOption {
	return func(cfg *runConfig) {
		cfg.maxTurns = maxTurns
	}
}

// AgentEngine 是与工作区无关的 Agent 执行引擎。
// 单次运行的全部环境 ( 工作区路径 ) 来自 Session，
// 经由 context 流向 System Prompt 构建与所有工具调用。
type AgentEngine struct {
	provider provider.LLMProvider
	registry tools.Registry

	thinkMode bool

	// cost 进程级成本观测器 ( 系统级策略，构造期注入 )，
	// 终端与飞书等入口共用同一引擎实例时天然全覆盖。
	cost CostObserver

	recovery *icontext.RecoveryManager
}

func NewAgentEngine(
	llmProvider provider.LLMProvider,
	toolRegistry tools.Registry,
	thinkMode bool,
	opts ...option.Opt[AgentEngine],
) (*AgentEngine, error) {
	if toolRegistry == nil {
		return nil, fmt.Errorf("tool registry is required")
	}

	if err := toolRegistry.Register(tools.NewSkillReader()); err != nil {
		return nil, fmt.Errorf("failed to register skill reader: %w", err)
	}

	eng := &AgentEngine{
		provider: llmProvider,
		registry: toolRegistry,

		thinkMode: thinkMode,

		recovery: icontext.NewRecoveryManager(),
	}
	option.Apply(eng, opts...)
	return eng, nil
}

// AgentEngineWithCostObserver 为引擎挂载成本观测器 ( 记账 + token 预算闸门 )。
func AgentEngineWithCostObserver(observer CostObserver) option.Opt[AgentEngine] {
	return func(eng *AgentEngine) {
		eng.cost = observer
	}
}

// Run 在指定会话上执行一次 Agent 运行。
//
// 会话承载运行所需的全部环境与状态:
// Workspace 决定工具的执行范围和 System Prompt 的内容，
// history 跨 Run 持久累积，同会话的多次运行共享上下文。
// opts 为本次运行的可选配置 ( 角色化 System Prompt、轮次预算等 )。
func (e *AgentEngine) Run(ctx context.Context, sess *Session, reporter Reporter, opts ...RunOption) error {
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

	var cfg runConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	cfg.cost = e.cost

	// 死循环检测的失败计数语义是 "连续轮次"，不跨 Run:
	// 新一次运行代表新的任务上下文，残留计数会让 "连续" 措辞失真。
	sess.injector.Reset()

	// 工作区与运行期 Reporter 经由 context 流向所有工具调用:
	// 工具实例进程级共享，不持有任何运行态 ( 见 SubagentTool 的事件上报 )。
	ctx = tools.WithWorkspace(ctx, sess.Workspace)
	ctx = tools.WithSubReporter(ctx, reporter)

	slog.Info("[engine] Agent 引擎启动", "session", sess.ID, "workspace", sess.Workspace)
	slog.Info("[engine] 慢思考模式", "enabled", e.thinkMode)

	// 运行摘要覆盖全部返回路径 ( 正常结束、强制收尾、错误 )。
	runStart := time.Now()
	defer func() {
		slog.Info(
			"[engine] 运行结束",
			"session", sess.ID,
			"elapsed", time.Since(runStart),
			"calls", cfg.calls,
			"prompt_tokens", cfg.promptTokens,
			"completion_tokens", cfg.completionTokens,
		)
	}()

	systemMessage, err := cfg.buildSystemMessage(sess)
	if err != nil {
		return err
	}

	for turn := 1; ; turn++ {
		if reminder, forced := cfg.forcedReminder(turn); forced {
			return e.forceFinalTurn(ctx, sess, systemMessage, reporter, reminder, &cfg)
		}
		done, err := e.runTurn(ctx, sess, systemMessage, reporter, turn, &cfg)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
	}
}

// forceFinalTurn 预算耗尽 ( 轮次或 token ) 后的强制收尾。
// 注入预算耗尽提醒后剥夺工具挂载再请求一次补全——无工具时模型结构性只能输出文本，
// 已收集的信息得以保留为最终汇报，而非随硬报错整体作废。
// 收尾补全是真实的计费调用，同样计时并上报成本。
// 收尾输出为空时由调用方 ( 如 RunSub 的结论校验 ) 兜底报错。
func (e *AgentEngine) forceFinalTurn(
	ctx context.Context,
	sess *Session,
	systemMessage schema.Message,
	reporter Reporter,
	reminder string,
	cfg *runConfig,
) error {
	if err := checkCanceled(ctx); err != nil {
		return err
	}

	slog.Warn("[engine] 预算耗尽，剥夺工具挂载，强制收尾汇报")

	sess.Append(schema.Message{
		Role:    schema.RoleUser,
		Content: reminder,
	})

	start := time.Now()
	finalGen, err := e.provider.Generate(ctx, e.buildRequestHistory(systemMessage, sess), nil)
	if err != nil {
		return fmt.Errorf("failed to generate forced final response: %w", err)
	}
	cfg.observe(finalGen.Usage, PhaseFinal, time.Since(start))

	content := strings.TrimSpace(finalGen.Message.Content)
	sess.Append(schema.Message{Role: schema.RoleAssistant, Content: content})
	if content != "" && reporter != nil {
		_ = reporter.OnMessage(ctx, content)
	}
	return nil
}

func (e *AgentEngine) runTurn(
	ctx context.Context,
	sess *Session,
	systemMessage schema.Message,
	reporter Reporter,
	turn int,
	cfg *runConfig,
) (bool, error) {
	if err := checkCanceled(ctx); err != nil {
		return false, err
	}
	slog.Info("[engine] start turn", "turn", turn)

	var currThinkingContent string
	if e.thinkMode {
		start := time.Now()
		thinkGen, err := e.think(ctx, e.buildRequestHistory(systemMessage, sess), reporter)
		if err != nil {
			return false, err
		}
		// 慢思考是真实的计费调用，与其他补全同一口径计时上报。
		cfg.observe(thinkGen.Usage, PhaseThink, time.Since(start))
		// 用真实消耗刷新水位线并校准估算系数。
		sess.compactor.Observe(thinkGen.Usage.PromptTokens)
		if thinkGen.Message.Content != "" {
			currThinkingContent = thinkGen.Message.Content
		}
	}

	actStart := time.Now()
	actGen, err := e.act(ctx, e.buildRequestHistory(systemMessage, sess), reporter)
	if err != nil {
		return false, err
	}
	cfg.observe(actGen.Usage, PhaseAct, time.Since(actStart))
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

	observations, err := e.execToolCalls(ctx, sess, actGen.Message.ToolCalls, reporter)
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

// execToolCalls 并发工具调用。
// 死循环检测 ( CheckAndInject ) 以轮次为单位评估整个批次，且必须留在 Wait 之后的串行路径上: R
//
//	eminderInjector 的失败计数无锁，不可进入并发 goroutine;
//	注入的 Reminder 作为观察结果的最后一条消息追加，保证下一次模型请求中拥有最高的近因效应权重。
func (e *AgentEngine) execToolCalls(
	ctx context.Context,
	sess *Session,
	toolCalls []schema.ToolCall,
	reporter Reporter,
) ([]schema.Message, error) {
	slog.Info("[engine] 模型请求并发执行工具调用...", "tool_count", len(toolCalls))

	observations := make([]schema.Message, len(toolCalls))
	results := make([]schema.ToolCallResult, len(toolCalls))

	errGroup, groupCtx := errgroup.WithContext(ctx)
	for idx, toolCall := range toolCalls {
		errGroup.Go(func() error {
			observation, result, err := e.execToolCall(groupCtx, toolCall, reporter, idx)
			if err != nil {
				return err
			}
			observations[idx] = observation
			results[idx] = result
			return nil
		})
	}

	if err := errGroup.Wait(); err != nil {
		return nil, err
	}
	if err := checkCanceled(ctx); err != nil {
		return nil, err
	}

	if reminder := sess.injector.CheckAndInject(toolCalls, results); reminder != nil {
		observations = append(observations, *reminder)
	}
	return observations, nil
}

// execToolCall 工具调用逻辑。
// 原始的 ToolCallResult 随观察消息一并返回，供串行路径上的死循环检测消费。
func (e *AgentEngine) execToolCall(
	ctx context.Context,
	toolCall schema.ToolCall,
	reporter Reporter,
	gidx int,
) (schema.Message, schema.ToolCallResult, error) {
	if err := checkCanceled(ctx); err != nil {
		return schema.Message{}, schema.ToolCallResult{}, err
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
		return schema.Message{}, schema.ToolCallResult{}, err
	}

	result := e.registry.Execute(ctx, toolCall)
	if err := checkCanceled(ctx); err != nil {
		return schema.Message{}, schema.ToolCallResult{}, err
	}

	finalOutput := result.Output
	if result.IsError {
		// 发生错误时，按领域错误码注入救援指南。
		finalOutput = e.recovery.AnalyzeAndInject(toolCall.Name, result.ErrorCode, result.Output)
		slog.Error(
			"[engine] -> 工具执行失败，注入救援指南。",
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
		return schema.Message{}, schema.ToolCallResult{}, err
	}

	return schema.Message{
		Role:       schema.RoleUser,
		Content:    finalOutput,
		ToolCallID: toolCall.ID,
	}, result, nil
}

func checkCanceled(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("agent run canceled: %w", err)
	}
	return nil
}
