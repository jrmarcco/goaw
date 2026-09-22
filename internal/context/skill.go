package context

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	skillContentParts  = 3
	minSkillContextLen = 159
)

// Skill 定义了从 SKILL.md 中解析出来的标准化技能结构。
type Skill struct {
	Name        string
	Description string

	Body string // Markdown 正文指令
}

// SkillLoader 负责从本地文件系统中加载并解析符合规范的技能。
type SkillLoader struct {
	workspace string
}

func NewSkillLoader(workspace string) *SkillLoader {
	return &SkillLoader{
		workspace: workspace,
	}
}

// Load 扫描 .goaw/sklls 目录并解析 SKILL.md 并格式化为字符串，用于后续注入 Context。
func (l *SkillLoader) Load() (string, error) {
	baseDir := filepath.Join(l.workspace, ".goaw", "skills")

	root, err := os.OpenRoot(baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}

	defer func() {
		_ = root.Close()
	}()

	var builder strings.Builder
	builder.WriteString("\n### 可选专业技能 (Agent Skills)\n")
	builder.WriteString("以下是你拥有的标准化外挂技能，请在符合 description 描述的场景下严格遵循其正文指令：\n\n")

	rootFS := root.FS()
	err = fs.WalkDir(rootFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// 仅处理名为 skillFileName ( 默认 SKILL.md ) 的文件。
		if !d.IsDir() && d.Name() == "SKILL.md" {
			content, err := fs.ReadFile(rootFS, path)
			if err != nil {
				return err
			}

			skill := parseSkillMD(string(content))

			fmt.Fprintf(&builder, "#### 技能名称: %s\n", skill.Name)
			fmt.Fprintf(&builder, "##### 触发条件: %s\n\n", skill.Description)
			fmt.Fprintf(&builder, "**执行指南**:\n")
			fmt.Fprintf(&builder, "%s\n\n---\n", skill.Body)
		}

		return nil
	})
	if err != nil {
		return "", err
	}
	if builder.Len() < minSkillContextLen {
		return "", nil
	}

	return builder.String(), nil
}

func parseSkillMD(content string) Skill {
	skill := Skill{
		Name:        "Unknown Skill",
		Description: "No description provided",
		Body:        content, // 默认使用全量 content 作为 body
	}

	if strings.HasPrefix(content, "---\n") || strings.HasPrefix(content, "---\r\n") {
		parts := strings.SplitN(content, "---", skillContentParts)
		if len(parts) == skillContentParts {
			frontMatter := parts[1]

			// 逐行读取 metadata
			lines := strings.Split(frontMatter, "\n")
			for _, line := range lines {
				text := strings.TrimSpace(line)
				if strings.HasPrefix(text, "name:") {
					skill.Name = strings.TrimPrefix(text, "name:")
				}

				if strings.HasPrefix(text, "description:") {
					skill.Description = strings.TrimPrefix(text, "description:")
				}
			}

			skill.Body = strings.TrimSpace(parts[2])
		}
	}

	return skill
}
