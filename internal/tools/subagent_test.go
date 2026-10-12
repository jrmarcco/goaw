package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jrmarcco/goaw/internal/schema"
)

// 测试用角色标识 ( goconst: 测试内多处引用 )。
const testRoleExplorer = "explorer"

// fakeAgentRunner 记录调用现场的测试替身运行器。
type fakeAgentRunner struct {
	runErr error

	gotSpec     SubAgentSpec
	gotPrompt   string
	gotReporter SubReporter
}

func (f *fakeAgentRunner) RunSub(
	_ context.Context,
	spec SubAgentSpec,
	prompt string,
	reporter SubReporter,
) (string, error) {
	f.gotSpec = spec
	f.gotPrompt = prompt
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

// explorerSpec 构造绑定指定注册表的 Explorer 测试规格。
func explorerSpec(registry Registry) SubAgentSpec {
	return SubAgentSpec{
		Name:         testRoleExplorer,
		Description:  "只读信息收集专员",
		SystemPrompt: "你是测试 Explorer。",
		MaxTurns:     5,
		Registry:     registry,
	}
}

func TestSubagentToolExecuteHappyPath(t *testing.T) {
	t.Parallel()

	runner := &fakeAgentRunner{}
	registry := NewDefaultRegistry()
	fieldReporter := &recordingReporter{}

	tool, err := NewSubagentTool(runner, []SubAgentSpec{explorerSpec(registry)}, fieldReporter)
	if err != nil {
		t.Fatalf("NewSubagentTool() error = %v", err)
	}

	// context 注入的运行期上报者优先于构造期兜底。
	ctxReporter := &recordingReporter{}
	ctx := WithSubReporter(context.Background(), ctxReporter)

	output, err := tool.Execute(ctx, []byte(`{"role":"explorer","prompt":"调研 workspace 内的模块依赖"}`))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	// 执行结果带委派上下文前缀回传给主 Agent。
	const wantOutput = "【子智能体返回任务执行结果】:\nsubagent conclusion"
	if output != wantOutput {
		t.Fatalf("Execute() output = %q, want %q", output, wantOutput)
	}
	if runner.gotPrompt != "调研 workspace 内的模块依赖" {
		t.Fatalf("RunSub prompt = %q", runner.gotPrompt)
	}
	if runner.gotSpec.Name != testRoleExplorer {
		t.Fatalf("RunSub spec name = %q, want %q", runner.gotSpec.Name, testRoleExplorer)
	}
	if runner.gotSpec.Registry != registry {
		t.Fatal("RunSub did not receive the spec-bound registry")
	}
	if runner.gotReporter != ctxReporter {
		t.Fatal("RunSub reporter should prefer the context-injected one")
	}
}

func TestSubagentToolExecuteFallsBackToFieldReporter(t *testing.T) {
	t.Parallel()

	runner := &fakeAgentRunner{}
	fieldReporter := &recordingReporter{}

	tool, err := NewSubagentTool(runner, []SubAgentSpec{explorerSpec(NewDefaultRegistry())}, fieldReporter)
	if err != nil {
		t.Fatalf("NewSubagentTool() error = %v", err)
	}

	if _, err = tool.Execute(context.Background(), []byte(`{"role":"explorer","prompt":"task"}`)); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if runner.gotReporter != fieldReporter {
		t.Fatal("RunSub reporter should fall back to the constructor one")
	}
}

func TestSubagentToolExecuteUnknownRole(t *testing.T) {
	t.Parallel()

	tool, err := NewSubagentTool(&fakeAgentRunner{}, []SubAgentSpec{explorerSpec(NewDefaultRegistry())}, nil)
	if err != nil {
		t.Fatalf("NewSubagentTool() error = %v", err)
	}

	_, err = tool.Execute(context.Background(), []byte(`{"role":"coder","prompt":"task"}`))
	toolErr, ok := errors.AsType[*ToolError](err)
	if !ok {
		t.Fatalf("Execute() error = %v, want *ToolError", err)
	}
	if !toolErr.Soft {
		t.Fatal("unknown role should be a soft error for model self-correction")
	}
	if toolErr.Code != schema.ErrCodeInvalidArgs {
		t.Fatalf("ErrorCode = %q, want %q", toolErr.Code, schema.ErrCodeInvalidArgs)
	}
	// 文案包含合法角色，模型才能完成自纠。
	if !strings.Contains(toolErr.Msg, testRoleExplorer) {
		t.Fatalf("error message %q should list the valid role %q", toolErr.Msg, testRoleExplorer)
	}
}

func TestSubagentToolExecuteEmptyPrompt(t *testing.T) {
	t.Parallel()

	tool, err := NewSubagentTool(&fakeAgentRunner{}, []SubAgentSpec{explorerSpec(NewDefaultRegistry())}, nil)
	if err != nil {
		t.Fatalf("NewSubagentTool() error = %v", err)
	}

	_, err = tool.Execute(context.Background(), []byte(`{"role":"explorer","prompt":"   "}`))
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
	tool, err := NewSubagentTool(runner, []SubAgentSpec{explorerSpec(NewDefaultRegistry())}, nil)
	if err != nil {
		t.Fatalf("NewSubagentTool() error = %v", err)
	}

	_, err = tool.Execute(context.Background(), []byte(`{"role":"explorer","prompt":"task"}`))
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

	tool, err := NewSubagentTool(&fakeAgentRunner{}, []SubAgentSpec{explorerSpec(NewDefaultRegistry())}, nil)
	if err != nil {
		t.Fatalf("NewSubagentTool() error = %v", err)
	}

	_, err = tool.Execute(context.Background(), []byte(`{"role":`))
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

func TestSubagentToolDefinition(t *testing.T) {
	t.Parallel()

	tool, err := NewSubagentTool(
		&fakeAgentRunner{},
		[]SubAgentSpec{
			explorerSpec(NewDefaultRegistry()),
			{
				Name:        "reviewer",
				Description: "代码审查专员",
				Registry:    NewDefaultRegistry(),
			},
		},
		nil,
	)
	if err != nil {
		t.Fatalf("NewSubagentTool() error = %v", err)
	}

	def := tool.Definition()
	if !strings.Contains(def.Description, testRoleExplorer) || !strings.Contains(def.Description, "reviewer") {
		t.Fatalf("Definition() description %q should enumerate all roles", def.Description)
	}

	props, ok := def.InputSchema.(map[string]any)[schema.KeyProperties].(map[string]any)
	if !ok {
		t.Fatalf("InputSchema properties = %#v, want a map", def.InputSchema)
	}
	roleProp, ok := props["role"].(map[string]any)
	if !ok {
		t.Fatalf("role property = %#v, want a map", props["role"])
	}
	enum, ok := roleProp[schema.KeyEnum].([]string)
	if !ok || len(enum) != 2 {
		t.Fatalf("role enum = %#v, want both role names", roleProp[schema.KeyEnum])
	}

	required, ok := def.InputSchema.(map[string]any)[schema.KeyRequired].([]string)
	if !ok || len(required) != 2 {
		t.Fatalf("required = %#v, want [role prompt]", def.InputSchema)
	}
}

func TestNewSubagentToolValidation(t *testing.T) {
	t.Parallel()

	if _, err := NewSubagentTool(nil, []SubAgentSpec{explorerSpec(NewDefaultRegistry())}, nil); err == nil {
		t.Fatal("nil runner should be rejected")
	}
	if _, err := NewSubagentTool(&fakeAgentRunner{}, nil, nil); err == nil {
		t.Fatal("empty specs should be rejected")
	}
	if _, err := NewSubagentTool(&fakeAgentRunner{}, []SubAgentSpec{{Name: "explorer"}}, nil); err == nil {
		t.Fatal("spec without registry should be rejected")
	}
	if _, err := NewSubagentTool(
		&fakeAgentRunner{},
		[]SubAgentSpec{
			explorerSpec(NewDefaultRegistry()),
			explorerSpec(NewDefaultRegistry()),
		},
		nil,
	); err == nil {
		t.Fatal("duplicate role names should be rejected")
	}
	if _, err := NewSubagentTool(
		&fakeAgentRunner{},
		[]SubAgentSpec{{Name: "  ", Registry: NewDefaultRegistry()}},
		nil,
	); err == nil {
		t.Fatal("blank role name should be rejected")
	}
}
