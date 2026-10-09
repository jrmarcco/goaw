package engine

import (
	"strings"
	"testing"

	"github.com/jrmarcco/goaw/internal/schema"
)

func TestCheckAndInject(t *testing.T) {
	t.Parallel()

	injector := NewReminderInjector(ReminderInjectorWithThreshold(3))

	stuck := schema.ToolCall{ID: testToolCallID1, Name: testToolName, Args: []byte(`{"cmd":"cargo build"}`)}
	failure := schema.ToolCallResult{ID: testToolCallID1, IsError: true}
	succeed := schema.ToolCallResult{ID: testToolCallID1}

	// 前两轮失败不注入。
	if msg := injector.CheckAndInject([]schema.ToolCall{stuck}, []schema.ToolCallResult{failure}); msg != nil {
		t.Fatalf("unexpected reminder before threshold: %+v", msg)
	}
	if msg := injector.CheckAndInject([]schema.ToolCall{stuck}, []schema.ToolCallResult{failure}); msg != nil {
		t.Fatalf("unexpected reminder before threshold: %+v", msg)
	}

	// 连续第三轮失败触发注入。
	msg := injector.CheckAndInject([]schema.ToolCall{stuck}, []schema.ToolCallResult{failure})
	if msg == nil {
		t.Fatal("expected reminder at threshold")
	}
	if msg.Role != schema.RoleUser {
		t.Fatalf("unexpected role: %q", msg.Role)
	}
	// go vet 无法校验嵌入模板的格式化动词，缺失动词会渲染出 %!d(MISSING) 垃圾。
	if strings.Contains(msg.Content, "%!") || strings.Contains(msg.Content, "MISSING") {
		t.Fatalf("reminder template rendered garbage: %q", msg.Content)
	}
	if !strings.Contains(msg.Content, testToolName) || !strings.Contains(msg.Content, "3") {
		t.Fatalf("reminder missing tool name or fail count: %q", msg.Content)
	}

	// 同一指纹本轮未失败即清零，需重新累计满阈值。
	if msg := injector.CheckAndInject([]schema.ToolCall{stuck}, []schema.ToolCallResult{succeed}); msg != nil {
		t.Fatalf("unexpected reminder on success: %+v", msg)
	}
	if msg := injector.CheckAndInject([]schema.ToolCall{stuck}, []schema.ToolCallResult{failure}); msg != nil {
		t.Fatalf("unexpected reminder after counter reset: %+v", msg)
	}
	if msg := injector.CheckAndInject([]schema.ToolCall{stuck}, []schema.ToolCallResult{failure}); msg != nil {
		t.Fatalf("unexpected reminder after counter reset: %+v", msg)
	}
	if msg := injector.CheckAndInject([]schema.ToolCall{stuck}, []schema.ToolCallResult{failure}); msg == nil {
		t.Fatal("expected reminder after 3 consecutive failing turns post-reset")
	}
}

func TestCheckAndInjectInterleavedSuccess(t *testing.T) {
	t.Parallel()

	// 交错型死循环: 每轮 read 成功与 edit 失败同批出现。
	// 其他工具的成功不得清空在途指纹的计数。
	injector := NewReminderInjector(ReminderInjectorWithThreshold(3))

	reader := schema.ToolCall{ID: testToolCallID2, Name: "file_reader", Args: []byte(`{"path":"main.go"}`)}
	editor := schema.ToolCall{ID: testToolCallID1, Name: testToolName, Args: []byte(`{"path":"main.go","content":"x"}`)}
	calls := []schema.ToolCall{reader, editor}
	results := []schema.ToolCallResult{
		{ID: testToolCallID2},
		{ID: testToolCallID1, IsError: true},
	}

	for range 2 {
		if msg := injector.CheckAndInject(calls, results); msg != nil {
			t.Fatalf("unexpected reminder before threshold: %+v", msg)
		}
	}
	if msg := injector.CheckAndInject(calls, results); msg == nil {
		t.Fatal("expected reminder for the interleaved failing tool")
	}
}

func TestCheckAndInjectDifferentArgs(t *testing.T) {
	t.Parallel()

	// 相同工具不同参数: 指纹互不累计，各自独立计数。
	injector := NewReminderInjector(ReminderInjectorWithThreshold(2))

	callA := schema.ToolCall{ID: testToolCallID1, Name: testToolName, Args: []byte(`{"cmd":"ls"}`)}
	callB := schema.ToolCall{ID: testToolCallID2, Name: testToolName, Args: []byte(`{"cmd":"pwd"}`)}
	failure := schema.ToolCallResult{IsError: true}

	if msg := injector.CheckAndInject(
		[]schema.ToolCall{callA, callB},
		[]schema.ToolCallResult{failure, failure},
	); msg != nil {
		t.Fatalf("unexpected reminder: %+v", msg)
	}

	// 本轮仅 callA 失败: callB 未失败被清零，callA 累计满阈值触发。
	if msg := injector.CheckAndInject([]schema.ToolCall{callA}, []schema.ToolCallResult{failure}); msg == nil {
		t.Fatal("expected reminder after 2 consecutive failing turns of identical args")
	}
}

func TestCheckAndInjectIntraTurnDedup(t *testing.T) {
	t.Parallel()

	// 同一指纹在同一轮内失败多次只计一个轮次。
	injector := NewReminderInjector(ReminderInjectorWithThreshold(2))

	stuck := schema.ToolCall{ID: testToolCallID1, Name: testToolName, Args: []byte(`{}`)}
	failure := schema.ToolCallResult{IsError: true}

	if msg := injector.CheckAndInject(
		[]schema.ToolCall{stuck, stuck},
		[]schema.ToolCallResult{failure, failure},
	); msg != nil {
		t.Fatalf("unexpected reminder: %+v", msg)
	}
	if msg := injector.CheckAndInject([]schema.ToolCall{stuck}, []schema.ToolCallResult{failure}); msg == nil {
		t.Fatal("expected reminder at the 2nd failing turn")
	}
}

func TestReminderInjectorReset(t *testing.T) {
	t.Parallel()

	injector := NewReminderInjector(ReminderInjectorWithThreshold(2))

	stuck := schema.ToolCall{ID: testToolCallID1, Name: testToolName, Args: []byte(`{"cmd":"ls"}`)}
	failure := schema.ToolCallResult{IsError: true}

	// 累计一轮后 Reset，计数清零，需重新累计满阈值。
	if msg := injector.CheckAndInject([]schema.ToolCall{stuck}, []schema.ToolCallResult{failure}); msg != nil {
		t.Fatalf("unexpected reminder: %+v", msg)
	}
	injector.Reset()
	if msg := injector.CheckAndInject([]schema.ToolCall{stuck}, []schema.ToolCallResult{failure}); msg != nil {
		t.Fatalf("unexpected reminder after reset: %+v", msg)
	}
	if msg := injector.CheckAndInject([]schema.ToolCall{stuck}, []schema.ToolCallResult{failure}); msg == nil {
		t.Fatal("expected reminder after re-accumulating to threshold")
	}
}

func TestGenFingerprintBoundary(t *testing.T) {
	t.Parallel()

	injector := NewReminderInjector()

	// NUL 分隔符保证 name/args 拼接边界无歧义: ("ab","c") 与 ("a","bc") 不得同指纹。
	if injector.genFingerprint("ab", []byte("c")) == injector.genFingerprint("a", []byte("bc")) {
		t.Fatal("fingerprint collision across name/args boundary")
	}
}
