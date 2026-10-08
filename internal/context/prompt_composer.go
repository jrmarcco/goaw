package context

import (
	_ "embed"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jrmarcco/goaw/internal/schema"
)

//go:embed prompts/core.md
var corePrompt string

//go:embed prompts/plan_mode.md
var planModePrompt string

// PromptComposer 用于根据工作区环境来动态生成 System Prompt。
type PromptComposer struct {
	workspace   string
	planMode    bool
	skillLoader *SkillLoader
}

func NewPromptComposer(workspace string, planMode bool) *PromptComposer {
	return &PromptComposer{
		workspace:   workspace,
		planMode:    planMode,
		skillLoader: NewSkillLoader(workspace),
	}
}

// Build 构建并返回一条完整的 RoleSystem 消息。
func (c *PromptComposer) Build() (schema.Message, error) {
	// 读取 AGENTS.md 文件。
	root, err := os.OpenRoot(c.workspace)
	if err != nil {
		return schema.Message{}, fmt.Errorf("failed to open workspace [%s]: %w", c.workspace, err)
	}
	defer func() {
		_ = root.Close()
	}()

	agentMD, err := root.Open("AGENTS.md")
	if err != nil {
		return schema.Message{}, fmt.Errorf("failed to open AGENTS.md: %w", err)
	}
	defer func() {
		_ = agentMD.Close()
	}()

	content, err := io.ReadAll(agentMD)
	if err != nil {
		return schema.Message{}, fmt.Errorf("failed to read AGENTS.md: %w", err)
	}

	// 构建系统提示词。
	var builder strings.Builder

	// 1.极简内核 ( Minimal Core)。
	// 确立基本身份与最底线的纪律。
	builder.WriteString(corePrompt)

	if c.planMode {
		// 2.引入状态嗅探与断点续传。
		builder.WriteString(planModePrompt)
	}

	// 3.外部化状态。
	// 加载项目专属规范 ( AGENTS.md )。
	builder.WriteString("\n# 项目专属指南 (来自AGENTS.md)\n")
	builder.WriteString("以下是当前工作区特有的架构规范与注意事项，你的行为必须绝对符合以下要求：\n")
	builder.WriteString("```markdown\n")
	builder.WriteString(string(content))
	builder.WriteString("\n```\n")

	// 4.只挂载技能索引；正文由 skill_reader 在需要时按需读取。
	skills, err := c.skillLoader.List()
	if err != nil {
		return schema.Message{}, fmt.Errorf("failed to list skills: %w", err)
	}
	if len(skills) > 0 {
		builder.WriteString("\n# 可选专业技能 (Agent Skills)\n")
		builder.WriteString("以下仅是技能索引。任务符合 description 时，必须先调用 skill_reader 获取完整执行指南；不要仅凭索引猜测技能内容。\n")
		for _, skill := range skills {
			fmt.Fprintf(&builder, "- `%s`: %s\n", skill.Name, skill.Description)
		}
	}

	return schema.Message{
		Role:    schema.RoleSystem,
		Content: builder.String(),
	}, nil
}
