package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jrmarcco/goaw/internal/schema"
)

// promptWorkspace 创建临时工作区，agentsMD 为空时表示不落 AGENTS.md。
func promptWorkspace(t *testing.T, agentsMD string) string {
	t.Helper()

	workspace := t.TempDir()
	if agentsMD == "" {
		return workspace
	}
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte(agentsMD), 0o600); err != nil {
		t.Fatalf("failed to create AGENTS.md: %v", err)
	}
	return workspace
}

// promptSkill 在工作区内落一个符合规范的技能，验证技能索引的注入行为。
func promptSkill(t *testing.T, workspace, name string) {
	t.Helper()

	dir := filepath.Join(workspace, ".goaw", "skills", name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("failed to create skill dir: %v", err)
	}
	content := "---\nname: " + name + "\ndescription: " + name + " 的测试技能\n---\n正文"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatalf("failed to create SKILL.md: %v", err)
	}
}

func TestPromptComposerMainMode(t *testing.T) {
	t.Parallel()

	workspace := promptWorkspace(t, "# 主模式规范")
	promptSkill(t, workspace, "demo")

	msg, err := NewPromptComposer(workspace, false).Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if msg.Role != schema.RoleSystem {
		t.Fatalf("role = %q, want %q", msg.Role, schema.RoleSystem)
	}
	if !strings.Contains(msg.Content, "Goaw") {
		t.Fatal("main mode should embed the core identity")
	}
	if !strings.Contains(msg.Content, "# 主模式规范") {
		t.Fatal("main mode should embed AGENTS.md")
	}
	if !strings.Contains(msg.Content, "可选专业技能") {
		t.Fatal("main mode should embed the skill index")
	}
}

func TestPromptComposerMainModeMissingAgentsMD(t *testing.T) {
	t.Parallel()

	// 主模式维持硬报错 ( 行为回归约束 )。
	if _, err := NewPromptComposer(promptWorkspace(t, ""), false).Build(); err == nil {
		t.Fatal("Build() should fail when AGENTS.md is missing in main mode")
	}
}

func TestPromptComposerRoleCore(t *testing.T) {
	t.Parallel()

	const roleCore = "你是测试 Explorer，只读信息收集专员。"

	workspace := promptWorkspace(t, "# 角色模式规范")
	promptSkill(t, workspace, "demo")

	msg, err := NewPromptComposer(workspace, false, WithRoleCore(roleCore)).Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if !strings.Contains(msg.Content, roleCore) {
		t.Fatal("role mode should embed the role core")
	}
	if strings.Contains(msg.Content, "Goaw") {
		t.Fatal("role core should replace the generic identity")
	}
	if !strings.Contains(msg.Content, "# 角色模式规范") {
		t.Fatal("role mode should still embed AGENTS.md when present")
	}
	if strings.Contains(msg.Content, "可选专业技能") {
		t.Fatal("role mode should skip the skill index")
	}
}

func TestPromptComposerRoleCoreMissingAgentsMD(t *testing.T) {
	t.Parallel()

	// 角色模式下 AGENTS.md 缺失软跳过: SubAgent 运行在继承的任意工作区。
	msg, err := NewPromptComposer(promptWorkspace(t, ""), false, WithRoleCore("角色身份")).Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if strings.Contains(msg.Content, "项目专属指南") {
		t.Fatal("role mode should skip the AGENTS.md section when the file is missing")
	}
}
