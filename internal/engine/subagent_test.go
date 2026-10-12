package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jrmarcco/goaw/internal/provider"
	"github.com/jrmarcco/goaw/internal/schema"
	"github.com/jrmarcco/goaw/internal/tools"
)

// fakeCostThreshold 测试观测器的 token 预算阈值。
const fakeCostThreshold = 1_000_000

// fakeCostObserver 记录上报序列的测试替身观测器，累计总量达到阈值即报告耗尽。
type fakeCostObserver struct {
	mu     sync.Mutex
	phases []string
	total  int
}

func (f *fakeCostObserver) Observe(usage schema.Usage, phase string, _ time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.phases = append(f.phases, phase)
	f.total += usage.PromptTokens + usage.CompletionTokens
}

func (f *fakeCostObserver) Exceeded() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.total >= fakeCostThreshold
}

// snapshot 返回并发安全的阶段序列与总量。
func (f *fakeCostObserver) snapshot() (phases []string, total int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.phases...), f.total
}

// scriptedProvider 按脚本顺序回放的测试替身模型提供者。
type scriptedProvider struct {
	mu    sync.Mutex
	gens  []*schema.Generation
	calls int

	// lastMsgs 记录最近一次请求的消息序列。
	lastMsgs []schema.Message
}

func (p *scriptedProvider) Generate(
	_ context.Context,
	msgs []schema.Message,
	_ []schema.ToolDefinition,
) (*schema.Generation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.calls >= len(p.gens) {
		return nil, context.DeadlineExceeded
	}
	gen := p.gens[p.calls]
	p.calls++
	p.lastMsgs = msgs
	return gen, nil
}

// callsCount 返回并发安全的历史调用次数。
func (p *scriptedProvider) callsCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// subFakeTool 固定输出的测试替身工具。
type subFakeTool struct {
	calls int
}

func (t *subFakeTool) Name() string { return "ping" }

func (t *subFakeTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{Name: t.Name()}
}

func (t *subFakeTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	t.calls++
	return "pong", nil
}

// recordingSubReporter 记录事件的 tools.SubReporter 测试替身。
type recordingSubReporter struct {
	mu          sync.Mutex
	toolCalls   []string
	toolResults []string
	messages    []string
}

func (r *recordingSubReporter) OnToolCall(_ context.Context, toolName, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.toolCalls = append(r.toolCalls, toolName)
	return nil
}

func (r *recordingSubReporter) OnToolCallResult(_ context.Context, toolName, result string, _ bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.toolResults = append(r.toolResults, toolName+":"+result)
	return nil
}

func (r *recordingSubReporter) OnMessage(_ context.Context, content string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages = append(r.messages, content)
	return nil
}

// subWorkspace 创建带 AGENTS.md 的临时工作区 ( 主模式组装的硬依赖 )。
func subWorkspace(t *testing.T) string {
	t.Helper()

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("# 测试工作区"), 0o600); err != nil {
		t.Fatalf("failed to create AGENTS.md: %v", err)
	}
	return workspace
}

// explorerSpec 构造绑定指定注册表与角色提示词的测试规格。
func explorerSpec(systemPrompt string, maxTurns int, registry tools.Registry) tools.SubAgentSpec {
	return tools.SubAgentSpec{
		Name:         "explorer",
		Description:  "只读信息收集专员",
		SystemPrompt: systemPrompt,
		MaxTurns:     maxTurns,
		Registry:     registry,
	}
}

func TestSubAgentRunnerRunSub(t *testing.T) {
	t.Parallel()

	// subFinalConclusion SubAgent 的预期最终结论。
	const subFinalConclusion = "最终结论"
	const roleCore = "你是测试 Explorer，只读信息收集专员。"

	fake := &subFakeTool{}
	llm := &scriptedProvider{gens: []*schema.Generation{
		// 第一轮: 请求工具调用。
		{Message: schema.Message{
			Role: schema.RoleAssistant,
			ToolCalls: []schema.ToolCall{
				{ID: testToolCallID1, Name: fake.Name(), Args: []byte(`{}`)},
			},
		}},
		// 第二轮: 给出最终结论，运行结束。
		{Message: schema.Message{Role: schema.RoleAssistant, Content: subFinalConclusion}},
	}}

	runner, err := NewSubAgentRunner(llm, false)
	if err != nil {
		t.Fatalf("NewSubAgentRunner() error = %v", err)
	}

	reporter := &recordingSubReporter{}
	spec := explorerSpec(roleCore, 0, tools.NewDefaultRegistry(fake))

	ctx := tools.WithWorkspace(context.Background(), subWorkspace(t))
	conclusion, err := runner.RunSub(ctx, spec, "调研依赖", reporter)
	if err != nil {
		t.Fatalf("RunSub() error = %v", err)
	}

	if conclusion != subFinalConclusion {
		t.Fatalf("RunSub() conclusion = %q, want %q", conclusion, subFinalConclusion)
	}
	if fake.calls != 1 {
		t.Fatalf("subagent tool executed %d times, want 1", fake.calls)
	}
	if len(reporter.toolCalls) != 1 || reporter.toolCalls[0] != fake.Name() {
		t.Fatalf("OnToolCall events = %v, want [%s]", reporter.toolCalls, fake.Name())
	}
	if len(reporter.toolResults) != 1 || !strings.Contains(reporter.toolResults[0], "pong") {
		t.Fatalf("OnToolCallResult events = %v, want one pong", reporter.toolResults)
	}
	if len(reporter.messages) != 1 || reporter.messages[0] != subFinalConclusion {
		t.Fatalf("OnMessage events = %v, want [%s]", reporter.messages, subFinalConclusion)
	}

	// 请求历史 = 角色化 System Prompt + 会话工作记忆:
	// role core 替换通用身份、AGENTS.md 保留注入、prompt 紧随其后作为开场用户消息。
	if len(llm.lastMsgs) < 2 {
		t.Fatalf("request history too short: %d messages", len(llm.lastMsgs))
	}
	sysMsg := llm.lastMsgs[0]
	if sysMsg.Role != schema.RoleSystem {
		t.Fatalf("first message role = %q, want system", sysMsg.Role)
	}
	if !strings.Contains(sysMsg.Content, roleCore) {
		t.Fatalf("system message should embed the role core")
	}
	if !strings.Contains(sysMsg.Content, "# 测试工作区") {
		t.Fatalf("system message should embed AGENTS.md content")
	}
	if strings.Contains(sysMsg.Content, "Goaw") {
		t.Fatalf("role core should replace the generic identity")
	}
	if llm.lastMsgs[1].Role != schema.RoleUser || llm.lastMsgs[1].Content != "调研依赖" {
		t.Fatalf("request history = %+v, want the opening user prompt after the system message", llm.lastMsgs)
	}
}

func TestSubAgentRunnerRunSubDefaultComposer(t *testing.T) {
	t.Parallel()

	// SystemPrompt 为空时回退主 Agent 默认组装 ( 通用身份 + AGENTS.md )。
	llm := &scriptedProvider{gens: []*schema.Generation{
		{Message: schema.Message{Role: schema.RoleAssistant, Content: "结论"}},
	}}

	runner, err := NewSubAgentRunner(llm, false)
	if err != nil {
		t.Fatalf("NewSubAgentRunner() error = %v", err)
	}

	spec := explorerSpec("", 0, tools.NewDefaultRegistry())
	ctx := tools.WithWorkspace(context.Background(), subWorkspace(t))
	if _, err = runner.RunSub(ctx, spec, "task", nil); err != nil {
		t.Fatalf("RunSub() error = %v", err)
	}

	sysMsg := llm.lastMsgs[0]
	if sysMsg.Role != schema.RoleSystem || !strings.Contains(sysMsg.Content, "Goaw") {
		t.Fatalf("system message = %+v, want the default composition", sysMsg)
	}
}

func TestSubAgentRunnerRunSubForcedFinal(t *testing.T) {
	t.Parallel()

	// subForcedConclusion 轮次预算耗尽后强制收尾的预期汇报。
	const subForcedConclusion = "预算耗尽后的收尾汇报"

	fake := &subFakeTool{}
	llm := &scriptedProvider{gens: []*schema.Generation{
		// 第一轮: 请求工具调用 ( MaxTurns=1，下一轮即耗尽 )。
		{Message: schema.Message{
			Role: schema.RoleAssistant,
			ToolCalls: []schema.ToolCall{
				{ID: testToolCallID1, Name: fake.Name(), Args: []byte(`{}`)},
			},
		}},
		// 强制收尾轮: 无工具挂载，模型只能输出文本。
		{Message: schema.Message{Role: schema.RoleAssistant, Content: subForcedConclusion}},
	}}

	runner, err := NewSubAgentRunner(llm, false)
	if err != nil {
		t.Fatalf("NewSubAgentRunner() error = %v", err)
	}

	spec := explorerSpec("角色身份", 1, tools.NewDefaultRegistry(fake))
	ctx := tools.WithWorkspace(context.Background(), subWorkspace(t))
	conclusion, err := runner.RunSub(ctx, spec, "task", nil)
	if err != nil {
		t.Fatalf("RunSub() error = %v", err)
	}

	if conclusion != subForcedConclusion {
		t.Fatalf("RunSub() conclusion = %q, want the forced final report %q", conclusion, subForcedConclusion)
	}
	if fake.calls != 1 {
		t.Fatalf("subagent tool executed %d times, want 1", fake.calls)
	}
	if llm.callsCount() != 2 {
		t.Fatalf("provider called %d times, want 2 (one tool turn + one forced final)", llm.callsCount())
	}

	// 强制收尾请求的历史中必须携带预算耗尽提醒。
	hasReminder := false
	for _, msg := range llm.lastMsgs {
		if strings.Contains(msg.Content, "工具调用预算已耗尽") {
			hasReminder = true
			break
		}
	}
	if !hasReminder {
		t.Fatalf("forced final request history = %+v, want the budget-exhausted reminder", llm.lastMsgs)
	}
}

func TestSubAgentRunnerRunSubTokenBudgetForcedFinal(t *testing.T) {
	t.Parallel()

	// subTokenConclusion token 预算耗尽后强制收尾的预期汇报。
	//
	//nolint:gosec // G101 误报: 测试断言文案包含 "Token" 字样被误判为硬编码凭证，并非密钥。
	const subTokenConclusion = "Token 预算耗尽后的收尾汇报"

	// 单次大用量直接击穿预算 ( 900k + 200k > 1M 阈值 )。
	const (
		bigPrompt     = 900_000
		bigCompletion = 200_000
	)

	fake := &subFakeTool{}
	llm := &scriptedProvider{gens: []*schema.Generation{
		// 第一轮: 请求工具调用 ( 未设轮次预算，只有 token 预算触发收尾 )。
		{Message: schema.Message{
			Role: schema.RoleAssistant,
			ToolCalls: []schema.ToolCall{
				{ID: testToolCallID1, Name: fake.Name(), Args: []byte(`{}`)},
			},
		}, Usage: schema.Usage{PromptTokens: bigPrompt, CompletionTokens: bigCompletion}},
		// 强制收尾轮: 无工具挂载，模型只能输出文本。
		{Message: schema.Message{Role: schema.RoleAssistant, Content: subTokenConclusion}},
	}}

	observer := &fakeCostObserver{}
	runner, err := NewSubAgentRunner(llm, false, SubAgentRunnerWithCostObserver(observer))
	if err != nil {
		t.Fatalf("NewSubAgentRunner() error = %v", err)
	}

	spec := explorerSpec("角色身份", 0, tools.NewDefaultRegistry(fake))
	ctx := tools.WithWorkspace(context.Background(), subWorkspace(t))
	conclusion, err := runner.RunSub(ctx, spec, "task", nil)
	if err != nil {
		t.Fatalf("RunSub() error = %v", err)
	}

	if conclusion != subTokenConclusion {
		t.Fatalf("RunSub() conclusion = %q, want %q", conclusion, subTokenConclusion)
	}
	if fake.calls != 1 {
		t.Fatalf("subagent tool executed %d times, want 1", fake.calls)
	}
	if llm.callsCount() != 2 {
		t.Fatalf("provider called %d times, want 2 (one tool turn + one forced final)", llm.callsCount())
	}

	// 观测序列 = act ( 击穿预算 ) + final ( 收尾补全，同样计费 )。
	phases, total := observer.snapshot()
	if len(phases) != 2 || phases[0] != PhaseAct || phases[1] != PhaseFinal {
		t.Fatalf("observed phases = %v, want [%s %s]", phases, PhaseAct, PhaseFinal)
	}
	if total != bigPrompt+bigCompletion {
		t.Fatalf("observed total = %d, want %d", total, bigPrompt+bigCompletion)
	}

	// 强制收尾请求的历史中必须携带 token 预算耗尽提醒。
	hasReminder := false
	for _, msg := range llm.lastMsgs {
		if strings.Contains(msg.Content, "Token 预算已耗尽") {
			hasReminder = true
			break
		}
	}
	if !hasReminder {
		t.Fatalf("forced final request history = %+v, want the token-budget reminder", llm.lastMsgs)
	}
}

func TestSubAgentRunnerRunSubWithoutFinalMessage(t *testing.T) {
	t.Parallel()

	llm := &scriptedProvider{gens: []*schema.Generation{
		// 空内容且无工具调用: 运行立即结束，但没有可回收的结论。
		{Message: schema.Message{Role: schema.RoleAssistant}},
	}}

	runner, err := NewSubAgentRunner(llm, false)
	if err != nil {
		t.Fatalf("NewSubAgentRunner() error = %v", err)
	}

	spec := explorerSpec("角色身份", 0, tools.NewDefaultRegistry())
	ctx := tools.WithWorkspace(context.Background(), subWorkspace(t))
	if _, err = runner.RunSub(ctx, spec, "task", nil); err == nil {
		t.Fatal("RunSub() should fail when the subagent produces no final message")
	}
}

func TestSubAgentRunnerRunSubMissingWorkspace(t *testing.T) {
	t.Parallel()

	runner, err := NewSubAgentRunner(&scriptedProvider{}, false)
	if err != nil {
		t.Fatalf("NewSubAgentRunner() error = %v", err)
	}

	// 缺少工作区: 子代理与父运行必须共享同一执行范围。
	spec := explorerSpec("角色身份", 0, tools.NewDefaultRegistry())
	_, err = runner.RunSub(context.Background(), spec, "task", nil)
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Fatalf("RunSub() error = %v, want a workspace error", err)
	}
}

func TestSubAgentRunnerRunSubNilSpecRegistry(t *testing.T) {
	t.Parallel()

	runner, err := NewSubAgentRunner(&scriptedProvider{}, false)
	if err != nil {
		t.Fatalf("NewSubAgentRunner() error = %v", err)
	}

	ctx := tools.WithWorkspace(context.Background(), t.TempDir())
	if _, err = runner.RunSub(ctx, tools.SubAgentSpec{Name: "explorer"}, "task", nil); err == nil {
		t.Fatal("RunSub() should reject a spec without registry")
	}
}

func TestNewSubAgentRunnerNilProvider(t *testing.T) {
	t.Parallel()

	if _, err := NewSubAgentRunner(nil, false); err == nil {
		t.Fatal("NewSubAgentRunner() should reject a nil provider")
	}
}

// provider.LLMProvider 接口一致性编译期校验。
var _ provider.LLMProvider = (*scriptedProvider)(nil)
