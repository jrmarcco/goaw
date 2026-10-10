package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jrmarcco/goaw/internal/schema"
)

// SubReporter 定义了 SubAgent 运行过程中向上层上报进度的规范。
// 方法签名必须与 engine.Reporter 的同名方法完全一致:
// Go 的结构化类型匹配按签名逐个比对，任何偏差都会让 engine.Reporter
// 失去对本接口的隐式满足，循环引用的解法即告失效。
type SubReporter interface {
	// OnToolCall 当 SubAgent 执行工具调用时，调用该方法。
	OnToolCall(ctx context.Context, toolName, args string) error

	// OnToolCallResult 当 SubAgent 的工具执行返回结果时，调用该方法。
	OnToolCallResult(ctx context.Context, toolName, result string, isError bool) error

	// OnMessage 当 SubAgent 产生对外文本时，调用该方法。
	OnMessage(ctx context.Context, content string) error
}

// AgentRunner 定义了运行 SubAgent 的规范。
// 接口定义在消费方 ( tools )，实现位于 engine 包，
// 依赖方向保持 engine → tools，消除包级循环引用。
type AgentRunner interface {
	RunSub(ctx context.Context, prompt string, registry Registry, reporter SubReporter) (string, error)
}

// subReporterCtxKey 运行期 Reporter 在 context 中的键类型。
type subReporterCtxKey struct{}

// WithSubReporter 将本次运行的 Reporter 注入 context。
// 工具实例进程级共享、不持有运行态，运行期的事件上报者
// 经由 context 从引擎流向工具，与 WithWorkspace 同构。
func WithSubReporter(ctx context.Context, reporter SubReporter) context.Context {
	return context.WithValue(ctx, subReporterCtxKey{}, reporter)
}

// SubReporterFromContext 从 context 解析本次运行的 Reporter，缺失时返回 nil。
func SubReporterFromContext(ctx context.Context) SubReporter {
	reporter, _ := ctx.Value(subReporterCtxKey{}).(SubReporter)
	return reporter
}

var _ Tool = (*SubagentTool)(nil)

// SubagentTool 派生 SubAgent 的工具。
// 主 Agent 经由它把子任务委派给一次独立的 Agent 运行，
// SubAgent 在只读注册表内工作，其中不含本工具，天然无法再派生 ( 无递归 )。
type SubagentTool struct {
	runner AgentRunner

	registry Registry // Subagent 专属的 "只读" 注册表

	reporter SubReporter // 兜底上报者，优先使用 context 注入的运行期实例
}

// NewSubagentTool 创建 SubagentTool。
// reporter 允许为 nil: 引擎运行时会经由 context 注入当次运行的 Reporter，
// 构造期传入的 reporter 仅作为脱离引擎运行 ( 如测试 ) 时的兜底。
func NewSubagentTool(runner AgentRunner, registry Registry, reporter SubReporter) (*SubagentTool, error) {
	if runner == nil {
		return nil, fmt.Errorf("agent runner 不能为空")
	}
	if registry == nil {
		return nil, fmt.Errorf("subagent 工具注册表不能为空")
	}
	return &SubagentTool{
		runner:   runner,
		registry: registry,
		reporter: reporter,
	}, nil
}

func (t *SubagentTool) Name() string {
	return "spawn_subagent"
}

func (t *SubagentTool) Definition() schema.ToolDefinition {
	const propPrompt = "prompt"

	return schema.ToolDefinition{
		Name: t.Name(),
		Description: "派生一个 SubAgent 并委派任务。SubAgent 拥有独立的上下文，在只读工具集内完成任务后，" +
			"将最终结论作为观察结果返回。适用于可并行的独立子任务，如检索、调研与总结。",
		InputSchema: map[string]any{
			schema.KeyType: schema.TypeObject,
			schema.KeyProperties: map[string]any{
				propPrompt: map[string]any{
					schema.KeyType:        schema.TypeString,
					schema.KeyDescription: "交给 SubAgent 的完整任务描述。不共享主 Agent 上下文，背景信息必须自包含。",
				},
			},
			schema.KeyRequired: []string{propPrompt},
		},
	}
}

func (t *SubagentTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input subagentArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return "", newToolError(schema.ErrCodeInvalidArgs, err, "解析参数失败")
	}
	if strings.TrimSpace(input.Prompt) == "" {
		// 软失败: 模型补全 prompt 后重试即可自愈，无需判定为硬错误。
		return "", &ToolError{
			Code: schema.ErrCodeInvalidArgs,
			Msg:  "prompt 不能为空，请描述交给 SubAgent 的任务",
			Soft: true,
		}
	}

	reporter := SubReporterFromContext(ctx)
	if reporter == nil {
		reporter = t.reporter
	}

	output, err := t.runner.RunSub(ctx, input.Prompt, t.registry, reporter)
	if err != nil {
		// 软失败: 失败原因随文案回传，主 Agent 可调整任务描述后重试。
		return "", &ToolError{
			Code:  schema.ErrCodeSubagentFailed,
			Msg:   "SubAgent 运行失败",
			Cause: err,
			Soft:  true,
		}
	}
	return output, nil
}

type subagentArgs struct {
	Prompt string `json:"prompt"`
}
