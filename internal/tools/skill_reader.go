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
// 工作区路径不随构造绑定，执行时从 context 解析 ( 见 WithWorkspace )。
type SkillReader struct{}

func NewSkillReader() *SkillReader {
	return &SkillReader{}
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

func (r *SkillReader) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	workspace, err := WorkspaceFromContext(ctx)
	if err != nil {
		return "", err
	}

	var input skillReadArgs
	if err = json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("解析参数失败: %w", err)
	}

	// 技能目录由工作区决定，随调用解析。
	skill, err := icontext.NewSkillLoader(workspace).Read(input.Name)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("# Skill: %s\n\n%s", skill.Name, skill.Body), nil
}

type skillReadArgs struct {
	Name string `json:"name"`
}
