package engine

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/jrmarcco/goaw/internal/provider"
	"github.com/jrmarcco/goaw/internal/schema"
	"github.com/jrmarcco/goaw/internal/tools"
)

var _ tools.AgentRunner = (*SubAgentRunner)(nil)

// 编译期断言: engine.Reporter 必须隐式满足 tools.SubReporter。
// 这是循环引用解法成立的前提 —— 一旦有人改动 Reporter 的方法签名，
// 该断言立即编译失败，提醒同步维护 tools.SubReporter。
var _ tools.SubReporter = Reporter(nil)

// SubAgentRunner 是 tools.AgentRunner 的 engine 侧实现，以 AgentEngine 为执行内核。
// 接口定义在消费方 tools 包、实现在本包，依赖方向保持 engine → tools，
// 两个包之间不再存在反向 import。
type SubAgentRunner struct {
	provider  provider.LLMProvider
	thinkMode bool

	seq atomic.Int64 // SubAgent 会话序号，保证会话 ID 唯一

	// 引擎与注册表绑定 ( SkillReader 在 NewAgentEngine 中注册 )，
	// 同一注册表重复建引擎会重复触发覆盖告警，这里按注册表身份缓存复用。
	mu             sync.Mutex
	cachedEng      *AgentEngine
	cachedRegistry tools.Registry
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
// 正常结束后返回 SubAgent 的最终结论。
func (r *SubAgentRunner) RunSub(
	ctx context.Context,
	prompt string,
	registry tools.Registry,
	reporter tools.SubReporter,
) (string, error) {
	if registry == nil {
		return "", fmt.Errorf("subagent tool registry is required")
	}

	workspace, err := tools.WorkspaceFromContext(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to resolve workspace for subagent: %w", err)
	}

	eng, err := r.engineFor(registry)
	if err != nil {
		return "", err
	}

	subReporter := &subRunReporter{downstream: reporter}
	sess := NewSession(fmt.Sprintf("subagent-%d", r.seq.Add(1)), workspace, false)
	sess.Append(schema.Message{Role: schema.RoleUser, Content: prompt})

	if err := eng.Run(ctx, sess, subReporter); err != nil {
		return "", fmt.Errorf("subagent run failed: %w", err)
	}
	if !subReporter.hasFinal {
		return "", fmt.Errorf("subagent finished without a final message")
	}
	return subReporter.final, nil
}

// engineFor 返回与给定注册表绑定的引擎，同一注册表 ( 指针相同 ) 时复用缓存。
func (r *SubAgentRunner) engineFor(registry tools.Registry) (*AgentEngine, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.cachedEng != nil && r.cachedRegistry == registry {
		return r.cachedEng, nil
	}

	eng, err := NewAgentEngine(r.provider, registry, r.thinkMode)
	if err != nil {
		return nil, fmt.Errorf("failed to create subagent engine: %w", err)
	}

	r.cachedEng, r.cachedRegistry = eng, registry
	return eng, nil
}

// subRunReporter 把 tools.SubReporter 桥接为 engine.Reporter，
// 同时收集最终结论: 引擎每次 act 产生文本都会回调 OnMessage，
// Run 正常返回后最后一次回调的内容即 SubAgent 的结论。
// 引擎不共享父会话历史，中间文本与最终结论都经由 downstream 透传给上层。
type subRunReporter struct {
	downstream tools.SubReporter

	hasFinal bool
	final    string
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
	r.hasFinal = true
	r.final = content
	if r.downstream != nil {
		return r.downstream.OnMessage(ctx, content)
	}
	return nil
}
