package tools

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jrmarcco/goaw/internal/schema"
)

// fakeAgentRunner 记录调用现场的测试替身运行器。
type fakeAgentRunner struct {
	runErr error

	gotPrompt   string
	gotRegistry Registry
	gotReporter SubReporter
}

func (f *fakeAgentRunner) RunSub(
	_ context.Context,
	prompt string,
	registry Registry,
	reporter SubReporter,
) (string, error) {
	f.gotPrompt = prompt
	f.gotRegistry = registry
	f.gotReporter = reporter
	if f.runErr != nil {
		return "", f.runErr
	}
	return "subagent conclusion", nil
}

// recordingReporter 记录消息事件的测试替身上报者。
type recordingReporter struct {
	messages []string
}

func (r *recordingReporter) OnToolCall(_ context.Context, _, _ string) error { return nil }

func (r *recordingReporter) OnToolCallResult(_ context.Context, _, _ string, _ bool) error {
	return nil
}

func (r *recordingReporter) OnMessage(_ context.Context, content string) error {
	r.messages = append(r.messages, content)
	return nil
}

func TestSubagentToolExecuteHappyPath(t *testing.T) {
	t.Parallel()

	runner := &fakeAgentRunner{}
	registry := NewDefaultRegistry()
	fieldReporter := &recordingReporter{}

	tool, err := NewSubagentTool(runner, registry, fieldReporter)
	if err != nil {
		t.Fatalf("NewSubagentTool() error = %v", err)
	}

	// context 注入的运行期上报者优先于构造期兜底。
	ctxReporter := &recordingReporter{}
	ctx := WithSubReporter(context.Background(), ctxReporter)

	output, err := tool.Execute(ctx, []byte(`{"prompt":"调研 workspace 内的模块依赖"}`))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if output != "subagent conclusion" {
		t.Fatalf("Execute() output = %q, want %q", output, "subagent conclusion")
	}
	if runner.gotPrompt != "调研 workspace 内的模块依赖" {
		t.Fatalf("RunSub prompt = %q", runner.gotPrompt)
	}
	if runner.gotRegistry != registry {
		t.Fatal("RunSub did not receive the subagent registry")
	}
	if runner.gotReporter != ctxReporter {
		t.Fatal("RunSub reporter should prefer the context-injected one")
	}
}

func TestSubagentToolExecuteFallsBackToFieldReporter(t *testing.T) {
	t.Parallel()

	runner := &fakeAgentRunner{}
	fieldReporter := &recordingReporter{}

	tool, err := NewSubagentTool(runner, NewDefaultRegistry(), fieldReporter)
	if err != nil {
		t.Fatalf("NewSubagentTool() error = %v", err)
	}

	if _, err = tool.Execute(context.Background(), []byte(`{"prompt":"task"}`)); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if runner.gotReporter != fieldReporter {
		t.Fatal("RunSub reporter should fall back to the constructor one")
	}
}

func TestSubagentToolExecuteEmptyPrompt(t *testing.T) {
	t.Parallel()

	tool, err := NewSubagentTool(&fakeAgentRunner{}, NewDefaultRegistry(), nil)
	if err != nil {
		t.Fatalf("NewSubagentTool() error = %v", err)
	}

	_, err = tool.Execute(context.Background(), []byte(`{"prompt":"   "}`))
	toolErr, ok := errors.AsType[*ToolError](err)
	if !ok {
		t.Fatalf("Execute() error = %v, want *ToolError", err)
	}
	if !toolErr.Soft {
		t.Fatal("empty prompt should be a soft error for model self-correction")
	}
	if toolErr.Code != schema.ErrCodeInvalidArgs {
		t.Fatalf("ErrorCode = %q, want %q", toolErr.Code, schema.ErrCodeInvalidArgs)
	}
}

func TestSubagentToolExecuteRunnerFailure(t *testing.T) {
	t.Parallel()

	runner := &fakeAgentRunner{runErr: fmt.Errorf("provider quota exceeded")}
	tool, err := NewSubagentTool(runner, NewDefaultRegistry(), nil)
	if err != nil {
		t.Fatalf("NewSubagentTool() error = %v", err)
	}

	_, err = tool.Execute(context.Background(), []byte(`{"prompt":"task"}`))
	toolErr, ok := errors.AsType[*ToolError](err)
	if !ok {
		t.Fatalf("Execute() error = %v, want *ToolError", err)
	}
	if !toolErr.Soft {
		t.Fatal("runner failure should be a soft error so the parent agent can retry")
	}
	if toolErr.Code != schema.ErrCodeSubagentFailed {
		t.Fatalf("ErrorCode = %q, want %q", toolErr.Code, schema.ErrCodeSubagentFailed)
	}
	if !errors.Is(err, runner.runErr) {
		t.Fatal("ToolError should preserve the underlying cause chain")
	}
}

func TestSubagentToolExecuteInvalidJSON(t *testing.T) {
	t.Parallel()

	tool, err := NewSubagentTool(&fakeAgentRunner{}, NewDefaultRegistry(), nil)
	if err != nil {
		t.Fatalf("NewSubagentTool() error = %v", err)
	}

	_, err = tool.Execute(context.Background(), []byte(`{"prompt":`))
	toolErr, ok := errors.AsType[*ToolError](err)
	if !ok {
		t.Fatalf("Execute() error = %v, want *ToolError", err)
	}
	if toolErr.Soft {
		t.Fatal("malformed JSON should be a hard error")
	}
	if toolErr.Code != schema.ErrCodeInvalidArgs {
		t.Fatalf("ErrorCode = %q, want %q", toolErr.Code, schema.ErrCodeInvalidArgs)
	}
}

func TestNewSubagentToolValidation(t *testing.T) {
	t.Parallel()

	if _, err := NewSubagentTool(nil, NewDefaultRegistry(), nil); err == nil {
		t.Fatal("nil runner should be rejected")
	}
	if _, err := NewSubagentTool(&fakeAgentRunner{}, nil, nil); err == nil {
		t.Fatal("nil registry should be rejected")
	}
}
