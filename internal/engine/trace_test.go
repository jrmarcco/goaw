package engine

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/jrmarcco/goaw/internal/obs"
	"github.com/jrmarcco/goaw/internal/schema"
	"github.com/jrmarcco/goaw/internal/tools"
)

// recordingExporter 收集整树导出的测试替身导出器。
type recordingExporter struct {
	mu    sync.Mutex
	roots []*obs.Span
}

func (e *recordingExporter) Export(root *obs.Span) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.roots = append(e.roots, root)
}

// exported 返回并发安全的导出快照。
func (e *recordingExporter) exported() []*obs.Span {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]*obs.Span(nil), e.roots...)
}

// findChild 按名称查找直接子节点。
func findChild(t *testing.T, span *obs.Span, name string) *obs.Span {
	t.Helper()

	for _, child := range span.Children {
		if child.Name == name {
			return child
		}
	}
	t.Fatalf("Span %q 下未找到名为 %q 的子节点", span.Name, name)
	return nil
}

// wantAttr 断言 Span 属性值。
func wantAttr(t *testing.T, span *obs.Span, key string, want any) {
	t.Helper()

	if got := span.Attributes[key]; got != want {
		t.Fatalf("Span %q 属性 %q 应为 %v，实际 %v", span.Name, key, want, got)
	}
}

// wantChildren 断言直接子节点数量。
func wantChildren(t *testing.T, span *obs.Span, want int) {
	t.Helper()

	if len(span.Children) != want {
		t.Fatalf("Span %q 应有 %d 个子节点，实际 %d", span.Name, want, len(span.Children))
	}
}

func TestRunProducesThreeLevelSpanTree(t *testing.T) {
	t.Parallel()

	fake := &subFakeTool{}
	llm := &scriptedProvider{gens: []*schema.Generation{
		// 第一轮: 请求工具调用。
		{Message: schema.Message{
			Role: schema.RoleAssistant,
			ToolCalls: []schema.ToolCall{
				{ID: testToolCallID1, Name: fake.Name(), Args: json.RawMessage(`{}`)},
			},
		}},
		// 第二轮: 纯文本收尾，运行结束。
		{Message: schema.Message{Role: schema.RoleAssistant, Content: "done"}},
	}}

	exporter := &recordingExporter{}
	eng, err := NewAgentEngine(llm, tools.NewDefaultRegistry(fake), false,
		AgentEngineWithTraceExporter(exporter),
	)
	if err != nil {
		t.Fatalf("NewAgentEngine() error = %v", err)
	}

	sess := NewSession("trace-tree-test", subWorkspace(t), false)
	sess.Append(schema.Message{Role: schema.RoleUser, Content: "task"})

	if err := eng.Run(context.Background(), sess, nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	exported := exporter.exported()
	if len(exported) != 1 {
		t.Fatalf("应导出且仅导出一棵任务树，实际 %d 棵", len(exported))
	}
	root := exported[0]

	// 第一层: 任务根。
	if root.Name != "agent.run" || root.Error != "" || root.EndedAt == 0 {
		t.Fatalf("根节点应为已结束且无错误的 agent.run，实际 %+v", root)
	}
	wantAttr(t, root, "session", "trace-tree-test")
	wantChildren(t, root, 2)

	// 第二层: Turn。
	turn1 := root.Children[0]
	turn2 := root.Children[1]
	if turn1.Name != "turn" || turn2.Name != "turn" {
		t.Fatalf("第二层应为 Turn 节点，实际 %q / %q", turn1.Name, turn2.Name)
	}
	wantAttr(t, turn1, "turn", 1)
	wantAttr(t, turn2, "turn", 2)

	// 第三层: Turn 内操作叶子 ( 未启用慢思考，第一轮为 act 补全 + 工具调用 )。
	wantChildren(t, turn1, 2)
	wantChildren(t, turn2, 1)

	genSpan := findChild(t, turn1, "llm.generate")
	wantAttr(t, genSpan, "phase", PhaseAct)
	if genSpan.Kind != obs.SpanKindClient {
		t.Fatalf("llm.generate 应标记 client 种类，实际 %q", genSpan.Kind)
	}
	wantChildren(t, genSpan, 0)

	toolSpan := findChild(t, turn1, "tool.execute")
	wantAttr(t, toolSpan, "tool_name", fake.Name())
	wantAttr(t, toolSpan, "is_error", false)
	wantChildren(t, toolSpan, 0)

	wantAttr(t, findChild(t, turn2, "llm.generate"), "phase", PhaseAct)
}

func TestRunRootSpanRecordsError(t *testing.T) {
	t.Parallel()

	// 空脚本: 第一次 Generate 即失败，根 span 应记录失败信号。
	llm := &scriptedProvider{}
	exporter := &recordingExporter{}

	eng, err := NewAgentEngine(llm, tools.NewDefaultRegistry(&subFakeTool{}), false,
		AgentEngineWithTraceExporter(exporter),
	)
	if err != nil {
		t.Fatalf("NewAgentEngine() error = %v", err)
	}

	sess := NewSession("trace-error-test", subWorkspace(t), false)
	sess.Append(schema.Message{Role: schema.RoleUser, Content: "task"})

	if err := eng.Run(context.Background(), sess, nil); err == nil {
		t.Fatal("脚本耗尽时 Run 应返回错误")
	}

	exported := exporter.exported()
	if len(exported) != 1 {
		t.Fatalf("失败路径同样应导出任务树，实际 %d 棵", len(exported))
	}
	if exported[0].Error == "" {
		t.Fatal("根节点应记录运行失败信号")
	}

	// 失败的 llm.generate 叶子也应记录错误。
	if len(exported[0].Children) == 0 {
		t.Fatal("失败前应至少产生一个 Turn 子节点")
	}
	turn := exported[0].Children[0]
	if len(turn.Children) == 0 || turn.Children[0].Error == "" {
		t.Fatal("失败的模型调用叶子应记录错误信号")
	}
}
