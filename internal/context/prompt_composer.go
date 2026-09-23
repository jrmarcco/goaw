package context

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jrmarcco/goaw/internal/schema"
)

// PromptComposer 用于根据工作区环境来动态生成 System Prompt。
type PromptComposer struct {
	workspace   string
	skillLoader *SkillLoader
}

func NewPromptComposer(workspace string, loaders ...*SkillLoader) *PromptComposer {
	loader := NewSkillLoader(workspace)
	if len(loaders) > 0 && loaders[0] != nil {
		loader = loaders[0]
	}
	return &PromptComposer{
		workspace:   workspace,
		skillLoader: loader,
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
	builder.WriteString(`# 核心身份
	你名叫 Goaw。
	一个由驾驭工程驱动的骨灰级研发助手。
	你具备极简主义哲学，拒绝废话。
	你能通过系统提供的内置工具，创建、读取、修改和执行工作区中的代码。

	# 核心纪律 (CRITICAL)
	1. 如需检查文件是否存在，请使用 bash 的 ls 或 test -f 而不是对目录使用 file_reader。
	2. 创建新文件时务必使用 file_writer 并同时提供 path 和 content 参数。
	3. 编辑文件前务必先读取现有文件，以理解上下文。
	4. 无论何时你需要写代码或创建文件，都要直接使用 file_writer 工具。
	5. 遇到工具执行报错时，仔细阅读 stderr 尝试自己修正命令并重试。
	6. 始终用中文回复，以便传达你的进展和想法。
	`)

	// 2.外部化状态。
	// 加载项目专属规范 ( AGENTS.md )。
	builder.WriteString("\n# 项目专属指南 (来自AGENTS.md)\n")
	builder.WriteString("以下是当前工作区特有的架构规范与注意事项，你的行为必须绝对符合以下要求：\n")
	builder.WriteString("```markdown\n")
	builder.WriteString(string(content))
	builder.WriteString("\n```\n")

	// 3.只挂载技能索引；正文由 skill_reader 在需要时按需读取。
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
