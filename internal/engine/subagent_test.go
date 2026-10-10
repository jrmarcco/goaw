package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jrmarcco/goaw/internal/provider"
	"github.com/jrmarcco/goaw/internal/schema"
	"github.com/jrmarcco/goaw/internal/tools"
)

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

// subWorkspace 创建带 AGENTS.md 的临时工作区 ( PromptComposer 的硬依赖 )。
func subWorkspace(t *testing.T) string {
	t.Helper()

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("# 测试工作区"), 0o600); err != nil {
		t.Fatalf("failed to create AGENTS.md: %v", err)
	}
	return workspace
}

func TestSubAgentRunnerRunSub(t *testing.T) {
	t.Parallel()

	// subFinalConclusion SubAgent 的预期最终结论。
	const subFinalConclusion = "最终结论"

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

	registry := tools.NewDefaultRegistry(fake)
	reporter := &recordingSubReporter{}

	ctx := tools.WithWorkspace(context.Background(), subWorkspace(t))
	conclusion, err := runner.RunSub(ctx, "调研依赖", registry, reporter)
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

	// 请求历史 = System Prompt + 会话工作记忆，prompt 必须紧跟其后作为开场用户消息。
	if len(llm.lastMsgs) < 2 || llm.lastMsgs[1].Role != schema.RoleUser ||
		llm.lastMsgs[1].Content != "调研依赖" {
		t.Fatalf("request history = %+v, want the opening user prompt after the system message", llm.lastMsgs)
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

	ctx := tools.WithWorkspace(context.Background(), subWorkspace(t))
	if _, err = runner.RunSub(ctx, "task", tools.NewDefaultRegistry(), nil); err == nil {
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
	_, err = runner.RunSub(context.Background(), "task", tools.NewDefaultRegistry(), nil)
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Fatalf("RunSub() error = %v, want a workspace error", err)
	}
}

func TestSubAgentRunnerRunSubNilRegistry(t *testing.T) {
	t.Parallel()

	runner, err := NewSubAgentRunner(&scriptedProvider{}, false)
	if err != nil {
		t.Fatalf("NewSubAgentRunner() error = %v", err)
	}

	ctx := tools.WithWorkspace(context.Background(), t.TempDir())
	if _, err = runner.RunSub(ctx, "task", nil, nil); err == nil {
		t.Fatal("RunSub() should reject a nil registry")
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
