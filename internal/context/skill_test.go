package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillLoaderProgressiveDisclosure(t *testing.T) {
	t.Parallel()

	content := `---
	name: deploy
	description: 发布服务时使用。
	---

	# Deployment

	secret body instruction
	`

	workspace := t.TempDir()
	writeTestSkill(t, workspace, "deploy", content)

	loader := NewSkillLoader(workspace)
	skills, err := loader.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("List() returned %d skills, want 1", len(skills))
	}
	if skills[0].Name != "deploy" || skills[0].Description != "发布服务时使用。" {
		t.Fatalf("List() metadata = %#v", skills[0])
	}

	skill, err := loader.Read("deploy")
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if !strings.Contains(skill.Body, "secret body instruction") {
		t.Fatalf("Read() body = %q", skill.Body)
	}
}

func TestPromptComposerOnlyIncludesSkillIndex(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	//nolint:gosec // 测试需要。
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("project rules"), 0o644); err != nil {
		t.Fatal(err)
	}

	content := `---
	name: deploy
	description: 发布服务时使用。
	---

	secret body instruction
	`
	writeTestSkill(t, workspace, "deploy", content)

	message, err := NewPromptComposer(workspace).Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if !strings.Contains(message.Content, "`deploy`: 发布服务时使用。") {
		t.Fatalf("system prompt missing skill metadata: %q", message.Content)
	}
	if strings.Contains(message.Content, "secret body instruction") {
		t.Fatalf("system prompt disclosed skill body: %q", message.Content)
	}
}

func writeTestSkill(t *testing.T, workspace, directory, content string) {
	t.Helper()
	dir := filepath.Join(workspace, ".goaw", "skills", directory)
	//nolint:gosec // 测试需要。
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // 测试需要。
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
