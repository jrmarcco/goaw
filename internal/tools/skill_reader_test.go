package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	icontext "github.com/jrmarcco/goaw/internal/context"
)

func TestSkillReaderExecute(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	skillDir := filepath.Join(workspace, ".goaw", "skills", "review")
	//nolint:gosec // 测试需要。
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `---
	name: review
	description: 审查代码时使用。
	---

	Follow the review checklist.
	`
	//nolint:gosec // 测试需要。
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	reader := NewSkillReader(icontext.NewSkillLoader(workspace))
	output, err := reader.Execute(context.Background(), []byte(`{"name":"review"}`))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(output, "# Skill: review") ||
		!strings.Contains(output, "Follow the review checklist.") {
		t.Fatalf("Execute() output = %q", output)
	}
}

func TestDefaultRegistryPreservesRegistrationOrder(t *testing.T) {
	t.Parallel()

	registry := NewDefaultRegistry(
		NewFileReader(t.TempDir()),
		NewSkillReader(icontext.NewSkillLoader(t.TempDir())),
	)

	definitions := registry.GetAvailableTools()
	if len(definitions) != 2 {
		t.Fatalf("GetAvailableTools() returned %d definitions", len(definitions))
	}
	if definitions[0].Name != "file_reader" || definitions[1].Name != "skill_reader" {
		t.Fatalf("tool order = %q, %q", definitions[0].Name, definitions[1].Name)
	}
}
