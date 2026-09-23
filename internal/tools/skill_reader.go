package tools

import (
	"context"
	"encoding/json"
	"fmt"

	icontext "github.com/jrmarcco/goaw/internal/context"
	"github.com/jrmarcco/goaw/internal/schema"
)

var _ Tool = (*SkillReader)(nil)

// SkillReader 按名称披露技能的完整执行指南。
type SkillReader struct {
	loader *icontext.SkillLoader
}

func NewSkillReader(loader *icontext.SkillLoader) *SkillReader {
	return &SkillReader{loader: loader}
}

func (r *SkillReader) Name() string {
	return "skill_reader"
}

func (r *SkillReader) Definition() schema.ToolDefinition {
	const propName = "name"

	return schema.ToolDefinition{
		Name:        r.Name(),
		Description: "按名称读取一个技能的完整执行指南。仅当任务符合 System Prompt 中该技能的 description 时调用。",
		InputSchema: map[string]any{
			schema.KeyType: schema.TypeObject,
			schema.KeyProperties: map[string]any{
				propName: map[string]any{
					schema.KeyType:        schema.TypeString,
					schema.KeyDescription: "System Prompt 技能索引中的技能名称",
				},
			},
			schema.KeyRequired: []string{propName},
		},
	}
}

func (r *SkillReader) Execute(_ context.Context, args json.RawMessage) (string, error) {
	if r.loader == nil {
		return "", fmt.Errorf("技能加载器未配置")
	}

	var input skillReadArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("解析参数失败: %w", err)
	}

	skill, err := r.loader.Read(input.Name)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("# Skill: %s\n\n%s", skill.Name, skill.Body), nil
}

type skillReadArgs struct {
	Name string `json:"name"`
}
