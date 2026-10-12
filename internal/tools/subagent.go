package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jrmarcco/goaw/internal/schema"
)

// SubReporter 定义了 SubAgent 运行过程中向上层上报进度的规范。
//
// 增加 SubReporter 是解决直接引用 engine.Reporter 会导致循环引用 ( tools ↔ engine)。
//
// SubReporter 方法签名必须与 engine.Reporter 的同名方法完全一致:
// Go 的结构化类型匹配按签名逐个比对，任何偏差都会让 engine.Reporter 失去对本接口的隐式满足。
type SubReporter interface {
	// OnToolCall 当 SubAgent 执行工具调用时，调用该方法。
	OnToolCall(ctx context.Context, toolName, args string) error

	// OnToolCallResult 当 SubAgent 的工具执行返回结果时，调用该方法。
	OnToolCallResult(ctx context.Context, toolName, result string, isError bool) error

	// OnMessage 当 SubAgent 产生对外文本时，调用该方法。
	OnMessage(ctx context.Context, content string) error
}

// SubAgentSpec 定义一个 SubAgent 角色的完整规格 ( 纯数据 )。
// 一个角色 = 一份 spec:
// 角色标识、面向模型的描述、System Prompt、轮次预算与专属工具集。
// 新增角色只需在装配层追加一份 spec，无需触碰接口与引擎。
type SubAgentSpec struct {
	// Name 角色唯一标识，作为 spawn_subagent 的 role 参数合法值。
	Name string

	// Description 面向模型的角色能力描述，聚合进工具定义帮助模型选型。
	Description string

	// SystemPrompt 角色 System Prompt core ( 替换通用身份段 )，
	// 为空时回退主 Agent 的默认组装。
	SystemPrompt string

	// MaxTurns 轮次预算，耗尽后引擎剥夺工具挂载强制收尾；小于等于 0 时使用引擎默认值。
	MaxTurns int

	// Registry 该角色专属的工具注册表 ( 含各自的 Middleware 链 )。
	Registry Registry
}

// AgentRunner 定义了运行 SubAgent 的规范。
// 接口定义在消费方 ( tools )，实现位于 engine 包，
// 依赖方向保持 engine → tools，消除包级循环引用。
type AgentRunner interface {
	RunSub(ctx context.Context, spec SubAgentSpec, prompt string, reporter SubReporter) (string, error)
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
// 角色规格 ( SubAgentSpec ) 决定 SubAgent 的 System Prompt 与工具集，
// 各角色注册表不含本工具，天然无法再派生 ( 无递归 )。
type SubagentTool struct {
	runner AgentRunner

	specs []SubAgentSpec
	index map[string]SubAgentSpec

	reporter SubReporter // 兜底上报者，优先使用 context 注入的运行期实例
}

// NewSubagentTool 创建 SubagentTool。
// specs 至少包含一个角色规格，角色名称唯一且必须绑定非空注册表。
//
// reporter 允许为 nil:
// 引擎运行时会经由 context 注入当次运行的 Reporter，
// 构造期传入的 reporter 仅作为脱离引擎运行 ( 如测试 ) 时的兜底。
func NewSubagentTool(runner AgentRunner, specs []SubAgentSpec, reporter SubReporter) (*SubagentTool, error) {
	if runner == nil {
		return nil, fmt.Errorf("agent runner 不能为空")
	}
	if len(specs) == 0 {
		return nil, fmt.Errorf("至少需要提供一个 SubAgent 角色规格")
	}

	index := make(map[string]SubAgentSpec, len(specs))
	for _, spec := range specs {
		if strings.TrimSpace(spec.Name) == "" {
			return nil, fmt.Errorf("SubAgent 角色名称不能为空")
		}
		if spec.Registry == nil {
			return nil, fmt.Errorf("SubAgent 角色 [%s] 的工具注册表不能为空", spec.Name)
		}
		if _, exists := index[spec.Name]; exists {
			return nil, fmt.Errorf("SubAgent 角色名称 [%s] 重复", spec.Name)
		}
		index[spec.Name] = spec
	}

	return &SubagentTool{
		runner:   runner,
		specs:    specs,
		index:    index,
		reporter: reporter,
	}, nil
}

func (t *SubagentTool) Name() string {
	return "spawn_subagent"
}

func (t *SubagentTool) Definition() schema.ToolDefinition {
	const (
		propRole   = "role"
		propPrompt = "prompt"
	)

	return schema.ToolDefinition{
		Name: t.Name(),
		Description: "派生一个 SubAgent 并委派任务。SubAgent 拥有独立的上下文，在所属角色的 System Prompt 与工具集约束内完成任务后，" +
			"将最终结论作为观察结果返回。可用角色:\n" + t.describeRoles(),
		InputSchema: map[string]any{
			schema.KeyType: schema.TypeObject,
			schema.KeyProperties: map[string]any{
				propRole: map[string]any{
					schema.KeyType:        schema.TypeString,
					schema.KeyDescription: "SubAgent 角色标识，决定其 System Prompt 与可用工具集。",
					schema.KeyEnum:        t.roleNames(),
				},
				propPrompt: map[string]any{
					schema.KeyType:        schema.TypeString,
					schema.KeyDescription: "交给 SubAgent 的完整任务描述。不共享主 Agent 上下文，背景信息必须自包含。",
				},
			},
			schema.KeyRequired: []string{propRole, propPrompt},
		},
	}
}

// describeRoles 聚合各角色能力描述，作为模型的选型依据。
func (t *SubagentTool) describeRoles() string {
	var builder strings.Builder
	for _, spec := range t.specs {
		fmt.Fprintf(&builder, "- %s: %s\n", spec.Name, spec.Description)
	}
	return builder.String()
}

func (t *SubagentTool) roleNames() []string {
	names := make([]string, 0, len(t.specs))
	for _, spec := range t.specs {
		names = append(names, spec.Name)
	}
	return names
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

	spec, ok := t.index[input.Role]
	if !ok {
		// 软失败: 列出合法角色，模型修正 role 后重试即可自愈。
		return "", &ToolError{
			Code: schema.ErrCodeInvalidArgs,
			Msg: fmt.Sprintf("未知的 SubAgent 角色 [%s]，可用角色: %s",
				input.Role, strings.Join(t.roleNames(), ", ")),
			Soft: true,
		}
	}

	reporter := SubReporterFromContext(ctx)
	if reporter == nil {
		reporter = t.reporter
	}

	slog.Debug("[subagent] 系统发起委派", "role", spec.Name, "prompt", input.Prompt)

	output, err := t.runner.RunSub(ctx, spec, input.Prompt, reporter)
	if err != nil {
		// 软失败: 失败原因随文案回传，主 Agent 可调整任务描述后重试。
		return "", &ToolError{
			Code:  schema.ErrCodeSubagentFailed,
			Msg:   "SubAgent 运行失败",
			Cause: err,
			Soft:  true,
		}
	}

	slog.Debug("[subagent] 委派任务执行完成", "role", spec.Name, "output", output)
	return fmt.Sprintf("【子智能体返回任务执行结果】:\n%s", output), nil
}

type subagentArgs struct {
	Role   string `json:"role"`   // 角色标识。
	Prompt string `json:"prompt"` // 任务描述。
}
