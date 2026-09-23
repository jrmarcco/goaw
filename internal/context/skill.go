package context

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	skillFileName     = "SKILL.md"
	skillContentParts = 3
)

// SkillMetadata 是允许在 System Prompt 中披露的技能索引信息。
type SkillMetadata struct {
	Name        string
	Description string
}

// Skill 是按需读取后得到的完整技能。
type Skill struct {
	SkillMetadata
	Body string
}

// SkillLoader 负责从本地文件系统中加载并解析符合规范的技能。
type SkillLoader struct {
	skillsDir string
}

func NewSkillLoader(workspace string) *SkillLoader {
	return &SkillLoader{
		skillsDir: filepath.Join(workspace, ".goaw", "skills"),
	}
}

// List 扫描技能目录，只返回可用于发现技能的元数据。
// 技能正文必须通过 Read 按需加载，避免在首轮上下文中注入所有技能。
func (l *SkillLoader) List() ([]SkillMetadata, error) {
	root, err := os.OpenRoot(l.skillsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("打开技能目录失败: %w", err)
	}
	defer func() { _ = root.Close() }()

	skills := make([]SkillMetadata, 0)
	rootFS := root.FS()
	err = fs.WalkDir(rootFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != skillFileName {
			return nil
		}

		content, err := fs.ReadFile(rootFS, path)
		if err != nil {
			return fmt.Errorf("读取技能文件 %q 失败: %w", path, err)
		}
		skill, err := parseSkillMD(string(content))
		if err != nil {
			return fmt.Errorf("解析技能文件 %q 失败: %w", path, err)
		}
		skills = append(skills, skill.SkillMetadata)
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(skills, func(i, j int) bool {
		return skills[i].Name < skills[j].Name
	})
	for i := 1; i < len(skills); i++ {
		if skills[i-1].Name == skills[i].Name {
			return nil, fmt.Errorf("技能名称重复: %q", skills[i].Name)
		}
	}
	return skills, nil
}

// Read 根据技能名称读取完整指令。
func (l *SkillLoader) Read(name string) (Skill, error) {
	if strings.TrimSpace(name) == "" {
		return Skill{}, errors.New("技能名称不能为空")
	}

	root, err := os.OpenRoot(l.skillsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return Skill{}, fmt.Errorf("技能 %q 不存在", name)
		}
		return Skill{}, fmt.Errorf("打开技能目录失败: %w", err)
	}
	defer func() { _ = root.Close() }()

	var matched *Skill
	err = fs.WalkDir(root.FS(), ".", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || d.Name() != skillFileName {
			return nil
		}

		content, readErr := fs.ReadFile(root.FS(), path)
		if readErr != nil {
			return fmt.Errorf("读取技能文件 %q 失败: %w", path, readErr)
		}
		skill, parseErr := parseSkillMD(string(content))
		if parseErr != nil {
			return fmt.Errorf("解析技能文件 %q 失败: %w", path, parseErr)
		}
		if skill.Name != name {
			return nil
		}
		if matched != nil {
			return fmt.Errorf("技能名称重复: %q", name)
		}
		matched = &skill
		return nil
	})
	if err != nil {
		return Skill{}, err
	}
	if matched == nil {
		return Skill{}, fmt.Errorf("技能 %q 不存在", name)
	}
	return *matched, nil
}

func parseSkillMD(content string) (Skill, error) {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	if !strings.HasPrefix(normalized, "---\n") {
		return Skill{}, errors.New("缺少 YAML front matter")
	}
	parts := strings.SplitN(normalized, "---", skillContentParts)
	if len(parts) != skillContentParts {
		return Skill{}, errors.New("YAML front matter 未闭合")
	}

	skill := Skill{Body: strings.TrimSpace(parts[2])}
	for _, line := range strings.Split(parts[1], "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "name":
			skill.Name = strings.TrimSpace(value)
		case "description":
			skill.Description = strings.TrimSpace(value)
		}
	}

	if skill.Name == "" {
		return Skill{}, errors.New("front matter 缺少 name")
	}
	if skill.Description == "" {
		return Skill{}, errors.New("front matter 缺少 description")
	}
	if skill.Body == "" {
		return Skill{}, errors.New("技能正文为空")
	}
	return skill, nil
}
