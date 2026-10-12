package context

import (
	_ "embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/jrmarcco/goaw/internal/schema"
	"github.com/jrmarcco/jit/xbean/option"
)

//go:embed prompts/core.md
var corePrompt string

//go:embed prompts/plan_mode.md
var planModePrompt string

//go:embed prompts/subagent_explorer.md
var subAgentExplorerPrompt string

// ExplorerRolePrompt 是 Explorer 角色的 System Prompt core。
// 装配层将它填入 SubAgentSpec.SystemPrompt，由引擎侧组装为角色化提示词。
var ExplorerRolePrompt = subAgentExplorerPrompt

// PromptComposer 用于根据工作区环境来动态生成 System Prompt。
type PromptComposer struct {
	workspace   string
	planMode    bool
	roleCore    string
	skillLoader *SkillLoader
}

// WithRoleCore 启用角色化组装: roleCore 替换通用核心身份段，
// 同时跳过 plan-mode 段与技能索引，并允许 AGENTS.md 缺失 ( 见 readAgentsMD )。
// 供角色化 SubAgent 注入专属身份，主 Agent 路径不使用。
func WithRoleCore(core string) option.Opt[PromptComposer] {
	return func(c *PromptComposer) {
		c.roleCore = core
	}
}

func NewPromptComposer(workspace string, planMode bool, opts ...option.Opt[PromptComposer]) *PromptComposer {
	c := &PromptComposer{
		workspace:   workspace,
		planMode:    planMode,
		skillLoader: NewSkillLoader(workspace),
	}

	option.Apply(c, opts...)
	return c
}

// Build 构建并返回一条完整的 RoleSystem 消息。
func (c *PromptComposer) Build() (schema.Message, error) {
	agentsMD, err := c.readAgentsMD()
	if err != nil {
		return schema.Message{}, err
	}

	var builder strings.Builder

	// 1. 身份段: 角色模式下为角色 core，主模式下为极简内核 + 可选 plan-mode 规范。
	c.writeIdentity(&builder)

	// 2. 项目专属规范 ( AGENTS.md )，角色模式下文件缺失时为空内容、跳过注入。
	writeAgentsMD(&builder, agentsMD)

	// 3. 技能索引 ( 仅主模式 )；
	// 正文由 skill_reader 在需要时按需读取。
	// 角色化 SubAgent 不注入: 技能索引面向任务执行视角，与角色纪律无关。
	if c.roleCore == "" {
		skills, err := c.skillLoader.List()
		if err != nil {
			return schema.Message{}, fmt.Errorf("failed to list skills: %w", err)
		}
		writeSkills(&builder, skills)
	}

	return schema.Message{
		Role:    schema.RoleSystem,
		Content: builder.String(),
	}, nil
}

// readAgentsMD 读取工作区的 AGENTS.md。
//
// 角色模式下文件缺失时返回空内容:
// SubAgent 运行在继承的任意工作区，不应因缺少项目规范文件而失败。
// 主模式下维持硬报错。
func (c *PromptComposer) readAgentsMD() ([]byte, error) {
	root, err := os.OpenRoot(c.workspace)
	if err != nil {
		return nil, fmt.Errorf("failed to open workspace [%s]: %w", c.workspace, err)
	}
	defer func() {
		_ = root.Close()
	}()

	agentMD, err := root.Open("AGENTS.md")
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && c.roleCore != "" {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to open AGENTS.md: %w", err)
	}
	defer func() {
		_ = agentMD.Close()
	}()

	content, err := io.ReadAll(agentMD)
	if err != nil {
		return nil, fmt.Errorf("failed to read AGENTS.md: %w", err)
	}
	return content, nil
}

// writeIdentity 写入身份段。
func (c *PromptComposer) writeIdentity(builder *strings.Builder) {
	if c.roleCore != "" {
		builder.WriteString(c.roleCore)
		return
	}

	// 极简内核 ( Minimal Core ): 确立基本身份与最底线的纪律。
	builder.WriteString(corePrompt)
	if c.planMode {
		builder.WriteString(planModePrompt)
	}
}

// writeAgentsMD 写入项目专属规范段。
func writeAgentsMD(builder *strings.Builder, content []byte) {
	if len(content) == 0 {
		return
	}

	builder.WriteString("\n# 项目专属指南 (来自AGENTS.md)\n")
	builder.WriteString("以下是当前工作区特有的架构规范与注意事项，你的行为必须绝对符合以下要求：\n")
	builder.WriteString("```markdown\n")
	builder.WriteString(string(content))
	builder.WriteString("\n```\n")
}

// writeSkills 只挂载技能索引。
func writeSkills(builder *strings.Builder, skills []SkillMetadata) {
	if len(skills) == 0 {
		return
	}

	builder.WriteString("\n# 可选专业技能 (Agent Skills)\n")
	builder.WriteString("以下仅是技能索引。任务符合 description 时，必须先调用 skill_reader 获取完整执行指南；不要仅凭索引猜测技能内容。\n")
	for _, skill := range skills {
		fmt.Fprintf(builder, "- `%s`: %s\n", skill.Name, skill.Description)
	}
}
