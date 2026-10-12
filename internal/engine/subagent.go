package engine

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	icontext "github.com/jrmarcco/goaw/internal/context"
	"github.com/jrmarcco/goaw/internal/provider"
	"github.com/jrmarcco/goaw/internal/schema"
	"github.com/jrmarcco/goaw/internal/tools"
)

var _ tools.AgentRunner = (*SubAgentRunner)(nil)

// defaultSubAgentMaxTurns SubAgent 轮次预算缺省值。
// SubAgent 是父运行同步等待的一次性委派，必须兜住 "一直成功地探索" 的失控场景
// ( ReminderInjector 只拦截连续失败的工具调用死循环 )。
const defaultSubAgentMaxTurns = 30

// SubAgentRunner 是 tools.AgentRunner 的 engine 侧实现，以 AgentEngine 为执行内核。
// 接口定义在消费方 tools 包、实现在本包，依赖方向保持 engine → tools。
type SubAgentRunner struct {
	mu sync.Mutex

	provider  provider.LLMProvider
	thinkMode bool

	seq atomic.Int64 // SubAgent 会话序号，保证会话 ID 唯一

	// 引擎与注册表绑定 ( SkillReader 在 NewAgentEngine 中注册 )，
	// 同一注册表重复建引擎会重复触发覆盖告警，这里按注册表身份缓存复用；
	// 多角色 ( 多注册表 ) 并存时各自持有独立引擎。
	cachedEngines map[tools.Registry]*AgentEngine
}

func NewSubAgentRunner(llmProvider provider.LLMProvider, thinkMode bool) (*SubAgentRunner, error) {
	if llmProvider == nil {
		return nil, fmt.Errorf("llm provider is required")
	}
	return &SubAgentRunner{
		provider:  llmProvider,
		thinkMode: thinkMode,
	}, nil
}

// RunSub 运行一个一次性的 SubAgent:
// 工作区继承自父运行 ( 从 context 解析 )，上下文历史独立 ( 全新 Session )，
// System Prompt 与轮次预算来自角色规格 ( SubAgentSpec )，
// 正常结束后返回 SubAgent 的最终结论。
func (r *SubAgentRunner) RunSub(
	ctx context.Context,
	spec tools.SubAgentSpec,
	prompt string,
	reporter tools.SubReporter,
) (string, error) {
	if spec.Registry == nil {
		return "", fmt.Errorf("subagent spec registry is required")
	}

	workspace, err := tools.WorkspaceFromContext(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to resolve workspace for subagent: %w", err)
	}

	eng, err := r.engineFor(spec.Registry)
	if err != nil {
		return "", err
	}

	subReporter := &subRunReporter{downstream: reporter}
	sess := NewSession(fmt.Sprintf("subagent-%d", r.seq.Add(1)), workspace, false)
	sess.Append(schema.Message{Role: schema.RoleUser, Content: prompt})

	if err := eng.Run(ctx, sess, subReporter, subRunOptions(spec)...); err != nil {
		return "", fmt.Errorf("subagent run failed: %w", err)
	}
	return subConclusion(sess)
}

// subRunOptions 把角色规格映射为引擎运行选项。
func subRunOptions(spec tools.SubAgentSpec) []RunOption {
	maxTurns := spec.MaxTurns
	if maxTurns <= 0 {
		maxTurns = defaultSubAgentMaxTurns
	}

	composeRole := func(workspace string) (schema.Message, error) {
		// SystemPrompt 为空时回退主 Agent 的默认组装。
		if strings.TrimSpace(spec.SystemPrompt) == "" {
			return icontext.NewPromptComposer(workspace, false).Build()
		}
		return icontext.NewPromptComposer(workspace, false, icontext.WithRoleCore(spec.SystemPrompt)).Build()
	}

	return []RunOption{
		WithSystemComposer(composeRole),
		WithMaxTurns(maxTurns),
	}
}

// subConclusion 从会话提取最终结论。
// 正常结束 ( 含强制收尾 ) 时最后一条消息是无工具调用的 Assistant 文本；
// 结构性排除了 "中间轮次的闲聊文本被当作汇报" 的情况
// ( 引擎每次 act 产生文本都会回调 OnMessage，不区分是否伴随工具调用 )。
func subConclusion(sess *Session) (string, error) {
	last := sess.Last()
	if last.Role != schema.RoleAssistant || len(last.ToolCalls) != 0 || strings.TrimSpace(last.Content) == "" {
		return "", fmt.Errorf("subagent finished without a final message")
	}
	return last.Content, nil
}

// engineFor 返回与给定注册表绑定的引擎，同一注册表 ( 指针相同 ) 时复用缓存。
func (r *SubAgentRunner) engineFor(registry tools.Registry) (*AgentEngine, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if eng, ok := r.cachedEngines[registry]; ok {
		return eng, nil
	}

	eng, err := NewAgentEngine(r.provider, registry, r.thinkMode)
	if err != nil {
		return nil, fmt.Errorf("failed to create subagent engine: %w", err)
	}

	if r.cachedEngines == nil {
		r.cachedEngines = make(map[tools.Registry]*AgentEngine)
	}
	r.cachedEngines[registry] = eng
	return eng, nil
}

var _ tools.SubReporter = Reporter(nil)

// subRunReporter 把 tools.SubReporter 桥接为 engine.Reporter，
// 引擎不共享父会话历史，中间文本与工具事件都经由 downstream 透传给上层；
// 最终结论不经此处收集，由 RunSub 从会话最后一条消息结构性提取 ( 见 subConclusion )。
type subRunReporter struct {
	downstream tools.SubReporter
}

// OnThinking 子代理的内部思考不上报。
// SubReporter 未约定思考事件，静默吞掉。
func (r *subRunReporter) OnThinking(_ context.Context) error {
	return nil
}

func (r *subRunReporter) OnToolCall(ctx context.Context, toolName, args string) error {
	if r.downstream != nil {
		return r.downstream.OnToolCall(ctx, toolName, args)
	}
	return nil
}

func (r *subRunReporter) OnToolCallResult(ctx context.Context, toolName, result string, isError bool) error {
	if r.downstream != nil {
		return r.downstream.OnToolCallResult(ctx, toolName, result, isError)
	}
	return nil
}

func (r *subRunReporter) OnMessage(ctx context.Context, content string) error {
	if r.downstream != nil {
		return r.downstream.OnMessage(ctx, content)
	}
	return nil
}
